package core

import (
	"context"
	"errors"
	"sync"
)

// ErrQueueFull is returned when all active slots and waiting positions are full.
var ErrQueueFull = errors.New("request queue is full")

type chatAdmission struct {
	mu          sync.Mutex
	concurrency int
	queueSize   int
	active      int
	waiters     []*chatAdmissionWaiter
}

type chatAdmissionWaiter struct {
	ready   chan struct{}
	granted bool // guarded by chatAdmission.mu
	settled bool // guarded by chatAdmission.mu; terminal state prevents double release
}

func newChatAdmission(concurrency, queueSize int) *chatAdmission {
	if concurrency < 1 {
		concurrency = 1
	}
	if queueSize < 0 {
		queueSize = 0
	}
	return &chatAdmission{concurrency: concurrency, queueSize: queueSize}
}

// acquire reserves an active slot, waiting in FIFO order when necessary.
// onQueued is called exactly once, without the admission lock, only if this
// request had to join the wait queue.
func (a *chatAdmission) acquire(ctx context.Context, onQueued func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	if err := ctx.Err(); err != nil {
		a.mu.Unlock()
		return err
	}
	if len(a.waiters) == 0 && a.active < a.concurrency {
		a.active++
		a.mu.Unlock()
		if err := ctx.Err(); err != nil {
			a.release()
			return err
		}
		return nil
	}
	if len(a.waiters) >= a.queueSize {
		a.mu.Unlock()
		return ErrQueueFull
	}
	waiter := &chatAdmissionWaiter{ready: make(chan struct{})}
	a.waiters = append(a.waiters, waiter)
	a.mu.Unlock()

	stopCancelCleanup := context.AfterFunc(ctx, func() { a.abandon(waiter) })
	defer stopCancelCleanup()

	if err := ctx.Err(); err != nil {
		a.abandon(waiter)
		return err
	}
	if onQueued != nil {
		if err := onQueued(); err != nil {
			a.abandon(waiter)
			return err
		}
	}

	select {
	case <-waiter.ready:
		return a.settle(waiter, ctx)
	case <-ctx.Done():
		a.abandon(waiter)
		return ctx.Err()
	}
}

// settle linearizes a granted slot's handoff to the caller against cancellation
// cleanup. A nil result transfers responsibility for release to the caller.
func (a *chatAdmission) settle(waiter *chatAdmissionWaiter, ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		a.abandonLocked(waiter)
		return err
	}
	if waiter.settled {
		return context.Canceled
	}
	waiter.settled = true
	return nil
}

// abandon removes a queued waiter, or returns a slot if it was granted while
// the caller was running onQueued or observing cancellation.
func (a *chatAdmission) abandon(waiter *chatAdmissionWaiter) {
	a.mu.Lock()
	a.abandonLocked(waiter)
	a.mu.Unlock()
}

func (a *chatAdmission) abandonLocked(waiter *chatAdmissionWaiter) {
	if waiter.settled {
		return
	}
	waiter.settled = true
	if waiter.granted {
		waiter.granted = false
		a.active--
		a.dispatchLocked()
		return
	}
	for i, queued := range a.waiters {
		if queued == waiter {
			a.waiters = append(a.waiters[:i], a.waiters[i+1:]...)
			break
		}
	}
	a.dispatchLocked()
}

// release returns one acquired active slot. Call it once for each successful
// acquire.
func (a *chatAdmission) release() {
	a.mu.Lock()
	if a.active > 0 {
		a.active--
	}
	a.dispatchLocked()
	a.mu.Unlock()
}

// update changes the limits without disturbing active requests or existing
// waiters. Existing waiters retain their order when queueSize is reduced.
func (a *chatAdmission) update(concurrency, queueSize int) {
	if concurrency < 1 {
		concurrency = 1
	}
	if queueSize < 0 {
		queueSize = 0
	}

	a.mu.Lock()
	a.concurrency = concurrency
	a.queueSize = queueSize
	a.dispatchLocked()
	a.mu.Unlock()
}

func (a *chatAdmission) snapshot() (active, queued int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active, len(a.waiters)
}

// dispatchLocked grants every available slot to the oldest waiters. The
// caller must hold a.mu.
func (a *chatAdmission) dispatchLocked() {
	for a.active < a.concurrency && len(a.waiters) > 0 {
		waiter := a.waiters[0]
		a.waiters[0] = nil
		a.waiters = a.waiters[1:]
		waiter.granted = true
		a.active++
		close(waiter.ready)
	}
}
