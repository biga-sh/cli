package watch

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Batcher coalesces changed paths into batches. A batch is flushed once
// changes stop for the idle duration, or after maxWait during a sustained
// burst. Flushes run one at a time, in the order their batches closed, so
// checkpoint uploads never overlap or reorder.
//
// A Batcher starts paused: paths added before [Batcher.Start] accumulate and
// flush once it is called.
type Batcher struct {
	idle    time.Duration
	maxWait time.Duration
	flush   func(ctx context.Context, paths []string)

	mu      sync.Mutex
	ctx     context.Context
	pending map[string]struct{}
	running bool
	closed  bool
	idleT   *time.Timer
	maxT    *time.Timer

	// flushing is a one-slot semaphore held for the whole of each flush, from
	// taking the batch to the callback returning. A channel rather than a
	// mutex, because waiters can be parked behind a slow upload.
	flushing chan struct{}
}

// NewBatcher returns a paused batcher that hands each batch to flush. The
// callback may call [Batcher.Add] to requeue paths that failed to sync.
func NewBatcher(idle, maxWait time.Duration, flush func(ctx context.Context, paths []string)) *Batcher {
	return &Batcher{
		idle:     idle,
		maxWait:  maxWait,
		flush:    flush,
		pending:  make(map[string]struct{}),
		flushing: make(chan struct{}, 1),
	}
}

// Add marks paths as changed. It is a no-op after [Batcher.Close].
func (b *Batcher) Add(paths ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for _, p := range paths {
		b.pending[p] = struct{}{}
	}
	b.armLocked()
}

// Start begins flushing; ctx is passed to every flush.
func (b *Batcher) Start(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ctx = ctx
	b.running = true
	if len(b.pending) > 0 {
		b.armLocked()
	}
}

// Close flushes whatever is pending, waits for any flush in progress, and
// stops the batcher. Paths requeued by that final flush are dropped.
func (b *Batcher) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.stopTimersLocked()
	b.mu.Unlock()

	b.flushing <- struct{}{}
	defer func() { <-b.flushing }()
	if ctx, paths := b.take(); len(paths) > 0 {
		b.flush(ctx, paths)
	}
}

func (b *Batcher) armLocked() {
	if !b.running || b.closed {
		return
	}
	if b.idleT != nil {
		b.idleT.Stop()
	}
	b.idleT = time.AfterFunc(b.idle, b.flushPending)
	if b.maxT == nil {
		b.maxT = time.AfterFunc(b.maxWait, b.flushPending)
	}
}

func (b *Batcher) stopTimersLocked() {
	if b.idleT != nil {
		b.idleT.Stop()
		b.idleT = nil
	}
	if b.maxT != nil {
		b.maxT.Stop()
		b.maxT = nil
	}
}

// flushPending runs on a timer. Taking the batch while holding the flush
// semaphore is what keeps batches in order.
func (b *Batcher) flushPending() {
	b.flushing <- struct{}{}
	defer func() { <-b.flushing }()

	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return
	}
	if ctx, paths := b.take(); len(paths) > 0 {
		b.flush(ctx, paths)
	}
}

// take removes and returns the pending paths, sorted for determinism.
func (b *Batcher) take() (context.Context, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopTimersLocked()
	if !b.running || len(b.pending) == 0 {
		return nil, nil
	}
	paths := make([]string, 0, len(b.pending))
	for p := range b.pending {
		paths = append(paths, p)
	}
	clear(b.pending)
	slices.Sort(paths)
	return b.ctx, paths
}
