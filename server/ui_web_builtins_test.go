package server

import (
	"context"
	"github.com/sebastianrcnt/atto/ui"
	"strings"
	"testing"
)

func TestSharedQueueAndContextTrees(t *testing.T) {
	q := queueTree(&PendingInput{Steers: []string{"steer"}, Queued: []string{"queue"}, Paused: true})
	if q == nil {
		t.Fatal("empty")
	}
	if e := ui.Validate(ui.Band, *q); e != nil {
		t.Fatal(e)
	}
	text := ui.PlainText(*q)
	for _, s := range []string{"steer", "queue", "Resume queue"} {
		if !strings.Contains(text, s) {
			t.Fatal(text)
		}
	}
	if queueTree(nil) != nil || queueTree(&PendingInput{}) != nil {
		t.Fatal("empty queue")
	}
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	if e := th.call(func() error {
		c := th.contextInfo("")
		if c.Tree == nil {
			t.Fatal("context drawing missing")
		}
		return ui.Validate(ui.Pane, *c.Tree)
	}); e != nil {
		t.Fatal(e)
	}
}
func TestUIBlockFreshBindingFlag(t *testing.T) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	if e := th.call(func() error {
		r := th.uiRegistry()
		m := ui.Match{Site: ui.Transcript, ID: "atto/bound"}
		r.Bind("atto", m, "go", ui.Press, func(_ context.Context, _ ui.Action) error { return nil })
		n := ui.Button(ui.ButtonProps{Key: "go", Label: "Go"})
		if e := r.OpenDefault("atto", ui.OpenOptions{Site: ui.Transcript, ID: m.ID}, nil, &n); e != nil {
			return e
		}
		s := th.snapshot()
		for _, i := range s.Items {
			if i.Type == ItemUIBlock {
				if !i.ActionsEnabled {
					t.Fatal("live block not bound")
				}
				return nil
			}
		}
		t.Fatal("no block")
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
