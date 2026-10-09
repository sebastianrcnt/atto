package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
)

type memoryCalls struct {
	mu     sync.Mutex
	active int
	begins int
	closed bool
}

func (m *memoryCalls) Begin() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active++
	m.begins++
}

func (m *memoryCalls) End() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
}

func (m *memoryCalls) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
}

func (m *memoryCalls) state() (int, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active, m.begins, m.closed
}

func TestIdleMemoryConcurrentTurns(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	m := &memoryCalls{}
	s := &Server{memory: m, Notify: func(string, map[string]any) {}}
	var threads []*thread
	var finishes []chan struct{}
	for i, err := range []error{nil, context.Canceled, errors.New("failed")} {
		th := &thread{s: s, id: string(rune('a' + i)), agent: agent.New(config.ModelRef{}, "", t.TempDir()), sess: session.New(t.TempDir())}
		th.startLane()
		t.Cleanup(func() { th.sess.Close(); th.stopLane() })
		finish := make(chan struct{})
		if _, e := beginForTest(s, th, func(context.Context, func(any)) error {
			<-finish
			return err
		}); e != nil {
			t.Fatal(e)
		}
		threads = append(threads, th)
		finishes = append(finishes, finish)
	}
	if active, _, _ := m.state(); active != 3 {
		t.Fatalf("active %d, want 3 turns", active)
	}
	if _, err := beginForTest(s, threads[0], nil); err == nil {
		t.Fatal("accepted a second turn on a busy thread")
	}
	for i, th := range threads {
		close(finishes[i])
		select {
		case <-th.runDone:
		case <-time.After(5 * time.Second):
			t.Fatal("turn did not finish")
		}
		if active, begins, _ := m.state(); active != 2-i || begins != 3 {
			t.Fatalf("after turn %d: active %d, begins %d", i, active, begins)
		}
	}
}

func TestIdleMemoryRequestsAndClose(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	m := &memoryCalls{}
	s := &Server{memory: m, threads: map[string]*thread{}, stop: make(chan struct{})}
	for _, method := range []string{"initialize", "thread/read", "job/list", "job/output", "agent/list", "agent/read"} {
		_, _ = s.call(context.Background(), method, nil)
	}
	if _, begins, _ := m.state(); begins != 0 {
		t.Fatal("read-only polling counted as input")
	}
	for _, method := range []string{"thread/resume", "thread/rollback", "turn/start", "turn/steer", "job/stop"} {
		_, _ = s.call(context.Background(), method, nil) // rejected input is still activity
	}
	if active, begins, _ := m.state(); active != 0 || begins != 5 {
		t.Fatalf("requests: active %d, begins %d", active, begins)
	}
	s.Close()
	if _, _, closed := m.state(); !closed {
		t.Fatal("Close left the idle timer running")
	}
}
