package server

import (
	"context"
	"errors"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
)

func TestTurnCancellationCauses(t *testing.T) {
	for _, tc := range []struct {
		name string
		user bool
		stop func(*thread)
	}{
		{"Esc", true, func(th *thread) { th.interrupt("") }},
		{"Ctrl+C", true, func(th *thread) { th.interrupt("cancel") }},
		{"Ctrl+Enter", true, func(th *thread) { th.submitReplace("client", "now", nil) }},
		{"remote interrupt", true, func(th *thread) { th.interrupt("") }},
		{"cancel task", true, func(th *thread) { th.interrupt("cancel") }},
		{"quit", false, func(th *thread) { th.turns.Cancel(nil) }},
		{"navigation", false, func(th *thread) { th.moveTo("old", nil, "client") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			th, _ := h.s.thread(h.id)
			th.call(func() error {
				ctx, err := th.turns.Begin(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				th.runKind = "turn"
				tc.stop(th)
				if ctx.Err() == nil || errors.Is(context.Cause(ctx), agent.ErrUserInterrupt) != tc.user {
					t.Fatalf("cause %v want user=%v", context.Cause(ctx), tc.user)
				}
				th.turns.End()
				th.pendingTree = ""
				th.turns.SendNow = nil
				th.runKind = ""
				return nil
			})
		})
	}
}
