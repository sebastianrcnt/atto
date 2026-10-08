package app

import (
	"context"
	"errors"
	"testing"

	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
)

// memoryCalls records activity on the UI goroutine, without forcing a GC.
type memoryCalls struct {
	active, begins, ends int
	closed               bool
}

func (m *memoryCalls) Begin() { m.active++; m.begins++ }
func (m *memoryCalls) End()   { m.active--; m.ends++ }
func (m *memoryCalls) Close() { m.closed = true }

func TestIdleMemoryTurn(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
	}{{"completed", nil}, {"interrupted", context.Canceled}, {"failed", errors.New("failed")}} {
		t.Run(c.name, func(t *testing.T) {
			a := treeApp(t)
			m := &memoryCalls{}
			a.memory = m
			finish := make(chan struct{})
			a.ui.Do(func() {
				a.start("Thinking", func(context.Context, func(any)) error {
					<-finish
					return c.err
				})
				if m.active != 1 {
					t.Fatalf("active %d during turn", m.active)
				}
				a.onInput("x")
				if m.active != 1 {
					t.Fatal("input made a running turn idle")
				}
			})
			close(finish)
			waitIdle(t, a)
			a.ui.Do(func() {
				if m.active != 0 || m.begins != 2 || m.ends != 2 {
					t.Fatalf("activity after turn: %+v", m)
				}
			})
		})
	}
}

func TestIdleMemoryInput(t *testing.T) {
	a := treeApp(t)
	m := &memoryCalls{}
	a.memory = m
	a.onInput("x")
	a.modal = tui.NewEditor("prompt")
	a.onInput("x") // focused modals handle the input after onInput returns
	a.modal = nil
	a.submit("/help", nil)
	if m.active != 0 || m.begins != 3 || m.ends != 3 {
		t.Fatalf("input activity: %+v", m)
	}

	r := &remote{}
	a.remote = r
	l := remoteSession{a, r}
	if _, err := l.Thread(false, nil); err != nil {
		t.Fatal(err)
	}
	l.Model()
	if m.begins != 3 {
		t.Fatal("remote polling counted as input")
	}
	_, _ = l.SetEffort("invalid")
	_ = l.Answer("missing", server.PromptAnswer{})
	if m.active != 0 || m.begins != 5 || m.ends != 5 {
		t.Fatalf("remote input activity: %+v", m)
	}
}

func TestIdleMemoryDroppedShell(t *testing.T) {
	a := treeApp(t)
	m := &memoryCalls{}
	a.memory = m
	a.ui.Do(func() {
		a.submit("!echo hello", nil)
		if m.active != 1 {
			t.Fatalf("active %d while shell runs", m.active)
		}
		a.dropShell() // its completion must balance Begin even after dropping it
	})
	within(t, a, "shell activity to end", func() bool { return m.active == 0 })
	a.ui.Do(func() {
		if m.begins != 2 || m.ends != 2 {
			t.Fatalf("shell activity: %+v", m)
		}
	})
}
