package core

import (
	"runtime/debug"
	"sync"
	"time"
)

const idleMemoryDelay = 30 * time.Second
const idleMemoryInterval = 5 * time.Minute

// IdleMemory returns unused heap pages to the OS after work ends. Begin and
// End bracket turns and input handling; overlapping work keeps it awake.
// Close cancels the pending release. It is safe to call from any goroutine.
type IdleMemory interface {
	Begin()
	End()
	Close()
}

type idleTimer interface {
	Stop() bool
}

type idleMemory struct {
	mu     sync.Mutex
	active int
	closed bool
	seq    uint64
	timer  idleTimer
	last   time.Time
	now    func() time.Time
	after  func(time.Duration, func()) idleTimer
	free   func()
}

// NewIdleMemory creates a reclaimer with no timer until work has ended.
func NewIdleMemory() IdleMemory {
	return &idleMemory{
		now: time.Now,
		after: func(d time.Duration, f func()) idleTimer {
			return time.AfterFunc(d, f)
		},
		free: debug.FreeOSMemory,
	}
}

func (m *idleMemory) cancel() {
	m.seq++ // a stopped timer's callback may already be waiting for mu
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
}

func (m *idleMemory) Begin() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel()
	m.active++
}

func (m *idleMemory) End() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	if m.active != 0 || m.closed {
		return
	}
	m.cancel()
	delay := idleMemoryDelay
	if !m.last.IsZero() {
		delay = max(delay, m.last.Add(idleMemoryInterval).Sub(m.now()))
	}
	seq := m.seq
	m.timer = m.after(delay, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed || m.active != 0 || seq != m.seq {
			return
		}
		m.timer = nil
		// Hold mu through the release: new work cannot start midway through
		// a forced GC. There is no timer during work, or after this release.
		m.free()
		m.last = m.now()
	})
}

func (m *idleMemory) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.cancel()
}
