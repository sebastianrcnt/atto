package server

import (
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/events"
)

func TestStaleInboxPollRequeues(t *testing.T) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	// A tick can already have taken events when the lane closes. It must
	// put those events back, not lose them or hand them to another thread.
	if err := th.call(func() error { th.closing = true; return nil }); err != nil {
		t.Fatal(err)
	}
	evs := []events.Event{{Text: "task"}, {Source: events.SourceReload}}
	th.call(func() error { th.inboxTick(false, evs, 0, 0); return nil })
	got := events.Drain(h.id)
	if len(got) != 2 || got[0].Text != "task" || got[1].Source != events.SourceReload {
		t.Fatalf("lost events %+v", got)
	}
	th.call(func() error { th.closing = false; return nil })
}

func TestQuietJobExitWaitsForNextTurn(t *testing.T) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	e := events.Event{Source: "job", Text: "Interrupted job 1 exited", Quiet: true}
	if err := events.Push(h.id, e); err != nil {
		t.Fatal(err)
	}
	th.pollInbox()
	th.call(func() error {
		if th.turns.Busy || len(th.agent.Messages()) != 0 || len(th.turns.PendingEvents) != 0 {
			t.Fatal("quiet exit started a turn")
		}
		th.turns.PendingEvents = events.Drain(h.id)
		th.deliverEvents()
		if th.turns.Busy || len(th.turns.PendingEvents) != 0 {
			t.Fatal("direct delivery started a turn")
		}
		th.turns.Busy, th.runKind = true, "turn"
		return nil
	})
	th.pollInbox()
	th.call(func() error {
		steers := th.agent.DrainSteers()
		if len(steers) != 1 || !strings.Contains(steers[0], e.Text) {
			t.Fatalf("quiet event missing %q", steers)
		}
		th.turns.Busy, th.runKind = false, ""
		return nil
	})
}
