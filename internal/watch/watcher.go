// Package watch reports changes under a directory tree. It is deliberately
// coarse: it reports which paths may have changed, and leaves deciding what
// actually changed to the caller, which compares content hashes. That keeps
// it correct across the many ways editors and tools write files (in place,
// write-then-rename, delete-then-create) without per-platform special cases.
package watch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ErrOverflow is reported when the OS dropped events, so changes may have
// been missed. See [Options.OnError].
var ErrOverflow = fsnotify.ErrEventOverflow

// Options configures a [Watcher].
type Options struct {
	// Root is the absolute directory to watch.
	Root string
	// Ignore reports whether a slash-separated path relative to Root is
	// excluded. Ignored directories are not descended into.
	Ignore func(rel string, isDir bool) bool
	// Stability is how long a path must go without events before it is
	// reported, so a file is not read while it is still being written.
	Stability time.Duration
	// OnChange receives the relative path of each file that may have been
	// created, modified, or removed, including directories that were removed
	// or renamed away. It is called from timer goroutines.
	OnChange func(rel string)
	// OnError receives watch errors. On [ErrOverflow] the watcher has already
	// started a rescan that reports every existing file; the caller should
	// also re-check files it knows of, since deletions cannot be rediscovered.
	OnError func(error)
}

// Watcher watches a directory tree recursively.
type Watcher struct {
	opts Options
	fsw  *fsnotify.Watcher
	done chan struct{}

	mu     sync.Mutex
	closed bool
	dirs   map[string]struct{}    // absolute paths of watched directories
	settle map[string]*time.Timer // per-path stability timers
}

// New starts watching opts.Root and every directory beneath it that is not
// ignored. Existing files are not reported.
func New(opts Options) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		opts:   opts,
		fsw:    fsw,
		done:   make(chan struct{}),
		dirs:   make(map[string]struct{}),
		settle: make(map[string]*time.Timer),
	}
	if err := w.addTree(opts.Root, false); err != nil {
		fsw.Close()
		return nil, err
	}
	go w.loop()
	return w, nil
}

// Rescan reports every file in the tree as changed and watches any directory
// that was missed.
func (w *Watcher) Rescan() {
	if err := w.addTree(w.opts.Root, true); err != nil {
		w.opts.OnError(err)
	}
}

// Close stops watching. Paths still inside their stability window are
// reported before Close returns, so a save made just before shutdown is not
// lost; no OnChange calls start after that.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	// Every path still in the map has a timer whose callback has not run its
	// locked section yet, and now never will report it.
	settling := make([]string, 0, len(w.settle))
	for rel, t := range w.settle {
		t.Stop()
		settling = append(settling, rel)
	}
	clear(w.settle)
	w.mu.Unlock()

	err := w.fsw.Close()
	<-w.done
	for _, rel := range settling {
		w.opts.OnChange(rel)
	}
	return err
}

func (w *Watcher) loop() {
	defer close(w.done)
	for {
		select {
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			if errors.Is(err, ErrOverflow) {
				// Off the event loop, so the walk does not cause more drops.
				go w.Rescan()
			}
			w.opts.OnError(err)
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	rel, ok := w.rel(ev.Name)
	if !ok {
		return
	}

	if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
		wasDir := w.unwatchTree(ev.Name)
		if !w.opts.Ignore(rel, wasDir) {
			w.touch(rel)
		}
	}
	if !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Chmod) {
		return
	}

	info, err := os.Lstat(ev.Name)
	switch {
	case err != nil:
		// Gone again already; the caller will find it missing.
		if !w.opts.Ignore(rel, false) {
			w.touch(rel)
		}
	case info.IsDir():
		// Directory writes only mean a child changed, which arrives as its
		// own event. A new directory may already have contents by the time
		// it is watched, so report those.
		if ev.Has(fsnotify.Create) && !w.opts.Ignore(rel, true) {
			if err := w.addTree(ev.Name, true); err != nil {
				w.opts.OnError(err)
			}
		}
	default:
		if !w.opts.Ignore(rel, false) {
			w.touch(rel)
		}
	}
}

// addTree watches dir and its non-ignored subdirectories. When report is
// set, every non-ignored regular file found is reported as changed.
func (w *Watcher) addTree(dir string, report bool) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			return nil // unreadable entries are skipped, not fatal
		}
		rel, ok := w.rel(path)
		if !ok && path != w.opts.Root {
			return nil
		}
		if d.IsDir() {
			if rel != "" && w.opts.Ignore(rel, true) {
				return filepath.SkipDir
			}
			return w.watchDir(path)
		}
		if report && d.Type().IsRegular() && !w.opts.Ignore(rel, false) {
			w.touch(rel)
		}
		return nil
	})
}

func (w *Watcher) watchDir(dir string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if _, ok := w.dirs[dir]; ok {
		return nil
	}
	if err := w.fsw.Add(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // removed while we were walking
		}
		return err
	}
	w.dirs[dir] = struct{}{}
	return nil
}

// unwatchTree drops the watches on dir and everything beneath it, reporting
// whether dir itself was a watched directory.
func (w *Watcher) unwatchTree(dir string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, wasDir := w.dirs[dir]
	prefix := dir + string(filepath.Separator)
	for d := range w.dirs {
		if d == dir || strings.HasPrefix(d, prefix) {
			// The OS usually dropped the watch already; errors are expected.
			_ = w.fsw.Remove(d)
			delete(w.dirs, d)
		}
	}
	return wasDir
}

// touch reports rel once it has gone the stability duration without events.
func (w *Watcher) touch(rel string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if t, ok := w.settle[rel]; ok {
		t.Reset(w.opts.Stability)
		return
	}
	w.settle[rel] = time.AfterFunc(w.opts.Stability, func() {
		w.mu.Lock()
		delete(w.settle, rel)
		closed := w.closed
		w.mu.Unlock()
		if !closed {
			w.opts.OnChange(rel)
		}
	})
}

// rel converts an absolute path under Root into its slash-separated relative
// form. The root itself maps to "" and reports false, as do outside paths.
func (w *Watcher) rel(path string) (string, bool) {
	rel, err := filepath.Rel(w.opts.Root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
