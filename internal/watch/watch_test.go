package watch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type recorder struct {
	mu      sync.Mutex
	batches [][]string
}

func (r *recorder) flush(_ context.Context, paths []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, paths)
}

func (r *recorder) get() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.batches)
}

func TestBatcherFlushesAfterIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var rec recorder
		b := NewBatcher(100*time.Millisecond, time.Second, rec.flush)
		b.Start(t.Context())

		b.Add("b.txt")
		time.Sleep(50 * time.Millisecond)
		b.Add("a.txt", "b.txt")
		time.Sleep(99 * time.Millisecond)
		synctest.Wait()
		if got := rec.get(); len(got) != 0 {
			t.Fatalf("flushed before idle elapsed: %v", got)
		}

		time.Sleep(2 * time.Millisecond)
		synctest.Wait()
		got := rec.get()
		if len(got) != 1 || !slices.Equal(got[0], []string{"a.txt", "b.txt"}) {
			t.Fatalf("batches = %v, want one sorted, deduplicated batch", got)
		}
		b.Close()
	})
}

func TestBatcherMaxWaitBoundsBursts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var rec recorder
		b := NewBatcher(100*time.Millisecond, 300*time.Millisecond, rec.flush)
		b.Start(t.Context())

		// A change every 50ms never leaves 100ms of quiet.
		for range 7 {
			b.Add("busy.txt")
			time.Sleep(50 * time.Millisecond)
		}
		synctest.Wait()
		if got := rec.get(); len(got) != 1 {
			t.Fatalf("batches = %v, want exactly one flush forced by maxWait", got)
		}
		b.Close()
	})
}

func TestBatcherHoldsUntilStarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var rec recorder
		b := NewBatcher(10*time.Millisecond, 50*time.Millisecond, rec.flush)
		b.Add("early.txt")
		time.Sleep(time.Second)
		synctest.Wait()
		if got := rec.get(); len(got) != 0 {
			t.Fatalf("paused batcher flushed: %v", got)
		}

		b.Start(t.Context())
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		if got := rec.get(); len(got) != 1 || got[0][0] != "early.txt" {
			t.Fatalf("batches = %v", got)
		}
		b.Close()
	})
}

func TestBatcherCloseFlushesPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var rec recorder
		b := NewBatcher(time.Hour, time.Hour, rec.flush)
		b.Start(t.Context())
		b.Add("last-save.txt")
		b.Close()
		if got := rec.get(); len(got) != 1 {
			t.Fatalf("Close did not flush pending paths: %v", got)
		}
		b.Add("after-close.txt")
		time.Sleep(2 * time.Hour)
		synctest.Wait()
		if got := rec.get(); len(got) != 1 {
			t.Fatalf("Add after Close flushed: %v", got)
		}
	})
}

func TestBatcherSerializesFlushes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu       sync.Mutex
			inFlight int
			maxSeen  int
			order    []string
		)
		b := NewBatcher(10*time.Millisecond, 10*time.Millisecond, func(_ context.Context, paths []string) {
			mu.Lock()
			inFlight++
			maxSeen = max(maxSeen, inFlight)
			order = append(order, paths...)
			mu.Unlock()

			time.Sleep(100 * time.Millisecond) // a slow upload

			mu.Lock()
			inFlight--
			mu.Unlock()
		})
		b.Start(t.Context())

		b.Add("1")
		time.Sleep(20 * time.Millisecond) // first flush is now in progress
		b.Add("2")
		time.Sleep(20 * time.Millisecond)
		b.Add("3")
		time.Sleep(time.Second)
		synctest.Wait()
		b.Close()

		if maxSeen != 1 {
			t.Errorf("%d flushes overlapped", maxSeen)
		}
		if !slices.Equal(order, []string{"1", "2", "3"}) {
			t.Errorf("flush order = %v", order)
		}
	})
}

// changes collects OnChange paths from a real watcher.
type changes struct {
	mu   sync.Mutex
	seen map[string]int
	ch   chan string
}

func newChanges() *changes {
	return &changes{seen: map[string]int{}, ch: make(chan string, 256)}
}

func (c *changes) add(rel string) {
	c.mu.Lock()
	c.seen[rel]++
	c.mu.Unlock()
	c.ch <- rel
}

func (c *changes) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case got := <-c.ch:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("no change reported for %s", want)
		}
	}
}

func (c *changes) count(rel string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[rel]
}

func startWatcher(t *testing.T, root string, c *changes) *Watcher {
	t.Helper()
	w, err := New(Options{
		Root:      root,
		Ignore:    func(rel string, _ bool) bool { return strings.HasPrefix(rel, "ignored") },
		Stability: 20 * time.Millisecond,
		OnChange:  c.add,
		OnError:   func(err error) { t.Errorf("watch error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func TestWatcherReportsChanges(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newChanges()
	startWatcher(t, root, c)

	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "existing.txt")

	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "new.txt")

	// Files inside a new directory are reported even if they were created
	// before the directory's watch was in place.
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b", "deep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "a/b/deep.txt")

	if err := os.RemoveAll(filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "a")

	if err := os.Remove(filepath.Join(root, "new.txt")); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "new.txt")
}

func TestWatcherSkipsIgnoredPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newChanges()
	startWatcher(t, root, c)

	if err := os.WriteFile(filepath.Join(root, "ignored", "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "seen.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "seen.txt")
	if n := c.count("ignored/x.txt"); n != 0 {
		t.Errorf("ignored path reported %d times", n)
	}
}

func TestWatcherRescanReportsEverything(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newChanges()
	w := startWatcher(t, root, c)
	w.Rescan()
	c.waitFor(t, "sub/f.txt")
}

func TestWatcherCloseReportsSettlingPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := newChanges()
	w, err := New(Options{
		Root:      root,
		Ignore:    func(string, bool) bool { return false },
		Stability: time.Hour, // nothing settles on its own
		OnChange:  c.add,
		OnError:   func(err error) { t.Errorf("watch error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "last.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Give the event time to arrive and start settling.
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.mu.Lock()
		n := len(w.settle)
		w.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("event never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if c.count("last.txt") != 1 {
		t.Errorf("settling path reported %d times on Close, want 1", c.count("last.txt"))
	}
}
