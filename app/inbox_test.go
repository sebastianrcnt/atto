package app

import (
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
