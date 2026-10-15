package ui

import (
	"io"
	"strings"
	"sync"
	"time"
)

const spinnerInterval = 80 * time.Millisecond

// Spinner shows progress for a step that takes a while. Off a terminal it
// stays silent until it stops, so logs and pipes get only the outcome.
type Spinner struct {
	ui    *UI
	msg   string
	stop  chan struct{}
	done  chan struct{}
	ended sync.Once
}

// Spin starts a spinner labelled msg. Call [Spinner.Stop] or
// [Spinner.Fail] to end it.
func (u *UI) Spin(msg string) *Spinner {
	s := &Spinner{ui: u, msg: msg, stop: make(chan struct{}), done: make(chan struct{})}
	u.print(gray.Render(u.sym.bar) + "\n")
	if !u.tty {
		close(s.done)
		return s
	}

	u.mu.Lock()
	u.spin = s
	u.mu.Unlock()
	go s.run()
	return s
}

func (s *Spinner) run() {
	defer close(s.done)
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()
	for frame := 0; ; frame++ {
		s.draw(frame)
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
	}
}

func (s *Spinner) draw(frame int) {
	u := s.ui
	symbol := magenta.Render(u.sym.frames[frame%len(u.sym.frames)])
	// Trailing dots cycle once every 8 frames, like clack's spinner.
	dots := strings.Repeat(".", frame/8%4)

	u.mu.Lock()
	defer u.mu.Unlock()
	io.WriteString(u.out, "\r\x1b[K"+symbol+"  "+s.msg+dots)
}

// Stop ends the spinner with a success line. Only the first Stop or Fail
// has an effect.
func (s *Spinner) Stop(msg string) { s.end(green.Render(s.ui.sym.done), msg) }

// Fail ends the spinner with a failure line.
func (s *Spinner) Fail(msg string) { s.end(red.Render(s.ui.sym.failure), msg) }

func (s *Spinner) end(symbol, msg string) {
	s.ended.Do(func() { s.finish(symbol, msg) })
}

func (s *Spinner) finish(symbol, msg string) {
	u := s.ui
	if u.tty {
		close(s.stop)
		<-s.done
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.spin == s {
		u.spin = nil
		io.WriteString(u.out, "\r\x1b[K")
	}
	io.WriteString(u.out, symbol+"  "+msg+"\n")
}
