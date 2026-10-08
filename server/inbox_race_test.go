package server

import (
	"reflect"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/events"
)

func TestInboxEventsRequeuedWhenBeginRefuses(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	s := &Server{Notify: func(string, map[string]any) {}}
	for _, closing := range []bool{false, true} {
		th := &thread{s: s, id: "s", closing: closing}
		th.startLane()
		t.Cleanup(th.stopLane)
		th.turns.Busy = !closing
		evs := []events.Event{
			{Time: time.Now().Add(-time.Second), Source: "job", Text: "finished", Title: "first"},
			{Time: time.Now(), Source: "timer", Text: "check", Title: "second"},
		}
		for _, ev := range evs {
			if err := events.Push(th.id, ev); err != nil {
				t.Fatal(err)
			}
		}
		drained := events.Drain(th.id)
		_ = th.call(func() error { th.beginInboxTurn(drained); return nil })
		got := events.Drain(th.id)
		if !reflect.DeepEqual(got, drained) {
			t.Fatalf("inbox lost on refused begin: got %v, want %v", got, drained)
		}
	}
}
