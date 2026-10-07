package server

import (
	"reflect"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/events"
)

// Events taken from the inbox of a thread that closed meanwhile go back,
// for whoever opens the session next.
func TestInboxEventsRequeuedWhenThreadClosed(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	th := &thread{id: "s", s: &Server{}}
	th.startLane()
	th.stopLane()
	<-th.done
	evs := []events.Event{
		{Time: time.Now().Add(-time.Second), Source: "job", Text: "finished", Title: "first"},
		{Time: time.Now(), Source: "timer", Text: "check", Title: "second"},
	}
	for _, ev := range evs {
		if err := events.Push(th.id, ev); err != nil {
			t.Fatal(err)
		}
	}
	want := events.Drain(th.id)
	for _, ev := range want {
		if err := events.Push(th.id, ev); err != nil {
			t.Fatal(err)
		}
	}
	th.pollInbox()
	if got := events.Drain(th.id); !reflect.DeepEqual(got, want) {
		t.Fatalf("inbox lost: got %v, want %v", got, want)
	}
}
