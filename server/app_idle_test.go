package server

import (
	"github.com/sebastianrcnt/atto/provider/providertest"
	"testing"
	"time"
)

func TestIdleMemoryTurn(t *testing.T) {
	for _, name := range []string{"completed", "interrupted", "failed"} {
		t.Run(name, func(t *testing.T) {
			gate := make(chan struct{})
			rep := providertest.Reply{Text: "done", Gate: gate}
			if name == "failed" {
				rep.Status = 404
			}
			h := newHarness(t, rep)
			h.s.memory.Close()
			m := &memoryCalls{}
			h.s.memory = m
			h.call("input/submit", map[string]any{"input": "work"})
			if h.m.Started(5*time.Second) == 0 {
				t.Fatal("model did not start")
			}
			if active, _, _ := m.state(); active != 1 {
				t.Fatalf("active %d during turn", active)
			}
			h.call("client/gate", map[string]any{"open": true})
			if active, _, _ := m.state(); active != 1 {
				t.Fatal("picker made a running turn idle")
			}
			if name == "interrupted" {
				h.call("turn/interrupt", map[string]any{"mode": "cancel"})
			}
			close(gate)
			h.completed()
			deadline := time.Now().Add(5 * time.Second)
			for {
				active, begins, _ := m.state()
				if active == 0 {
					want := 2
					if name == "interrupted" {
						want++
					}
					if begins != want {
						t.Fatalf("activity: %d begins", begins)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("turn did not become idle")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestIdleMemoryInput(t *testing.T) {
	h := newHarness(t)
	h.s.memory.Close()
	m := &memoryCalls{}
	h.s.memory = m
	for _, method := range []string{"thread/read", "job/list", "models/list"} {
		h.call(method, nil)
	}
	if _, begins, _ := m.state(); begins != 0 {
		t.Fatal("polling counted as input")
	}
	h.call("input/submit", map[string]any{"input": "/help"})
	if active, begins, _ := m.state(); active != 0 || begins != 1 {
		t.Fatalf("input: active %d, begins %d", active, begins)
	}
	_, _ = h.try(h.c, "thread/setEffort", map[string]any{"effort": "invalid"})
	if active, begins, _ := m.state(); active != 0 || begins != 2 {
		t.Fatalf("rejected input: active %d, begins %d", active, begins)
	}
}

func TestIdleMemoryDroppedShell(t *testing.T) {
	h := newHarness(t)
	h.s.memory.Close()
	m := &memoryCalls{}
	h.s.memory = m
	h.call("shell/start", map[string]any{"command": "echo hello"})
	th, err := h.s.thread(h.id)
	if err != nil {
		t.Fatal(err)
	}
	// Close waits for shell completion even after dropping it, balancing Begin.
	h.s.closeThread(th, closeMode{reason: "exit"})
	if active, begins, _ := m.state(); active != 0 || begins != 2 {
		t.Fatalf("shell: active %d, begins %d", active, begins)
	}
}
