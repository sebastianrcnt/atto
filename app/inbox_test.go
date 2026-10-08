package app

import (
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/events"
)

func TestStaleInboxPollRequeues(t *testing.T) {
	a := treeApp(t)
	for _, e := range []events.Event{{Text: "task"}, {Source: events.SourceReload}} {
		if err := events.Push("old-session", e); err != nil {
			t.Fatal(err)
		}
	}
	a.pollInbox("old-session")
	evs := events.Drain("old-session")
	if len(evs) != 2 || evs[0].Text != "task" || evs[1].Source != events.SourceReload {
		t.Fatalf("stale poll lost events: %+v", evs)
	}
	if len(a.pendingEvents) != 0 {
		t.Fatal("events delivered to the wrong session")
	}
}

func TestQuietJobExitWaitsForNextTurn(t *testing.T) {
	a := treeApp(t)
	e := events.Event{Source: "job", Text: "Interrupted job 1 exited", Quiet: true}
	if err := events.Push(a.sess.ID, e); err != nil {
		t.Fatal(err)
	}
	a.pollInbox(a.sess.ID)
	if a.busy || len(a.agent.Messages()) != 0 || len(a.pendingEvents) != 0 {
		t.Fatal("quiet exit started a turn")
	}
	// A quiet event held while busy can become deliverable just as the
	// turn ends, without going through another poll.
	a.pendingEvents = events.Drain(a.sess.ID)
	a.deliverEvents()
	if a.busy || len(a.pendingEvents) != 0 {
		t.Fatal("direct delivery started a turn")
	}
	// The next running turn takes it after its current step.
	a.busy, a.runKind = true, "turn"
	a.pollInbox(a.sess.ID)
	if steers := a.agent.DrainSteers(); len(steers) != 1 || !strings.Contains(steers[0], e.Text) {
		t.Fatalf("quiet event missing from next turn: %q", steers)
	}
}
