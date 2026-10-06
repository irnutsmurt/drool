// Package lock provides per-host shared/exclusive locks with FIFO fairness
// (ARCHITECTURE-ALPHA §11.3). Locks are in memory: after a controller
// restart nothing is running, so nothing needs to be held.
package lock

import (
	"context"
	"sync"

	"drool/internal/model"
)

// Manager hands out host locks.
type Manager struct {
	mu    sync.Mutex
	hosts map[string]*hostLock
}

type hostLock struct {
	shared    int
	exclusive bool
	holders   map[string]int // owner (job id) -> count, for display
	queue     []*waiter
}

type waiter struct {
	mode  model.LockMode
	owner string
	ready chan struct{}
}

func New() *Manager { return &Manager{hosts: map[string]*hostLock{}} }

func (m *Manager) get(host string) *hostLock {
	h := m.hosts[host]
	if h == nil {
		h = &hostLock{holders: map[string]int{}}
		m.hosts[host] = h
	}
	return h
}

func (h *hostLock) compatible(mode model.LockMode) bool {
	if mode == model.LockExclusive {
		return !h.exclusive && h.shared == 0
	}
	return !h.exclusive
}

func (h *hostLock) take(w *waiter) {
	if w.mode == model.LockExclusive {
		h.exclusive = true
	} else {
		h.shared++
	}
	h.holders[w.owner]++
}

// grant wakes queued waiters from the front while they are compatible, so
// a waiting exclusive request blocks later shared requests (no starvation).
func (h *hostLock) grant() {
	for len(h.queue) > 0 && h.compatible(h.queue[0].mode) {
		w := h.queue[0]
		h.queue = h.queue[1:]
		h.take(w)
		close(w.ready)
	}
}

// TryAcquire takes the lock immediately if possible.
func (m *Manager) TryAcquire(host, owner string, mode model.LockMode) (release func(), ok bool) {
	if mode == model.LockNone {
		return func() {}, true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.get(host)
	if len(h.queue) > 0 || !h.compatible(mode) {
		return nil, false
	}
	w := &waiter{mode: mode, owner: owner}
	h.take(w)
	return m.releaser(host, w), true
}

// Acquire waits for the lock in FIFO order until ctx ends.
func (m *Manager) Acquire(ctx context.Context, host, owner string, mode model.LockMode) (func(), error) {
	if rel, ok := m.TryAcquire(host, owner, mode); ok {
		return rel, nil
	}
	m.mu.Lock()
	h := m.get(host)
	w := &waiter{mode: mode, owner: owner, ready: make(chan struct{})}
	h.queue = append(h.queue, w)
	h.grant()
	m.mu.Unlock()
	select {
	case <-w.ready:
		return m.releaser(host, w), nil
	case <-ctx.Done():
		m.mu.Lock()
		defer m.mu.Unlock()
		select {
		case <-w.ready: // granted concurrently; give it back
			m.release(host, w)
		default:
			for i, q := range h.queue {
				if q == w {
					h.queue = append(h.queue[:i], h.queue[i+1:]...)
					break
				}
			}
			h.grant()
		}
		return nil, ctx.Err()
	}
}

func (m *Manager) releaser(host string, w *waiter) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.release(host, w)
		})
	}
}

// release is called with mu held.
func (m *Manager) release(host string, w *waiter) {
	h := m.hosts[host]
	if w.mode == model.LockExclusive {
		h.exclusive = false
	} else {
		h.shared--
	}
	if h.holders[w.owner]--; h.holders[w.owner] <= 0 {
		delete(h.holders, w.owner)
	}
	h.grant()
	if h.shared == 0 && !h.exclusive && len(h.queue) == 0 {
		delete(m.hosts, host)
	}
}

// Holders returns the owners currently holding a host's lock.
func (m *Manager) Holders(host string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.hosts[host]
	if h == nil {
		return nil
	}
	out := make([]string, 0, len(h.holders))
	for o := range h.holders {
		out = append(out, o)
	}
	return out
}
