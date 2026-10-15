package backdraft

import (
	"container/list"
	"context"
	"errors"
	"sync"
)

// ErrQueueFull is returned when a request arrives to a full queue.
var ErrQueueFull = errors.New("backdraft: queue full")

// Limiter bounds concurrent work with a FIFO wait queue. Capacity can change
// at runtime, which lets the control plane model a capacity drop.
type Limiter struct {
	mu         sync.Mutex
	capacity   int // 0 = unlimited
	queueLimit int // 0 = unbounded
	inUse      int
	waiters    list.List // of *waiter
}

type waiter struct {
	ch      chan struct{}
	granted bool
}

func NewLimiter(capacity, queueLimit int) *Limiter {
	return &Limiter{capacity: capacity, queueLimit: queueLimit}
}

// Acquire waits for a worker. If abandon is true, a request whose context
// ends while queued leaves the queue and Acquire returns the context error;
// otherwise it waits regardless, as a server that never looks at
// cancellation does.
func (l *Limiter) Acquire(ctx context.Context, abandon bool) (release func(), err error) {
	l.mu.Lock()
	if l.waiters.Len() == 0 && (l.capacity == 0 || l.inUse < l.capacity) {
		l.inUse++
		l.mu.Unlock()
		return l.release, nil
	}
	if l.queueLimit > 0 && l.waiters.Len() >= l.queueLimit {
		l.mu.Unlock()
		return nil, ErrQueueFull
	}
	w := &waiter{ch: make(chan struct{})}
	elem := l.waiters.PushBack(w)
	l.mu.Unlock()

	if !abandon {
		<-w.ch
		return l.release, nil
	}
	select {
	case <-w.ch:
		return l.release, nil
	case <-ctx.Done():
		l.mu.Lock()
		if w.granted {
			// Granted while we were giving up: hand the slot back.
			l.inUse--
			l.grantLocked()
		} else {
			l.waiters.Remove(elem)
		}
		l.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (l *Limiter) release() {
	l.mu.Lock()
	l.inUse--
	l.grantLocked()
	l.mu.Unlock()
}

func (l *Limiter) grantLocked() {
	for l.waiters.Len() > 0 && (l.capacity == 0 || l.inUse < l.capacity) {
		w := l.waiters.Remove(l.waiters.Front()).(*waiter)
		w.granted = true
		l.inUse++
		close(w.ch)
	}
}

// Resize changes capacity and queue limit. Requests already queued beyond
// a lowered queue limit stay queued.
func (l *Limiter) Resize(capacity, queueLimit int) {
	l.mu.Lock()
	l.capacity, l.queueLimit = capacity, queueLimit
	l.grantLocked()
	l.mu.Unlock()
}

// Stats returns requests holding a worker, requests waiting, and capacity.
func (l *Limiter) Stats() (inUse, queued, capacity int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inUse, l.waiters.Len(), l.capacity
}
