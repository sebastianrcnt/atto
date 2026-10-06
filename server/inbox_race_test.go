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
		th := &thread{id: "s", busy: !closing, closing: closing}
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
		s.beginInboxTurn(th, drained)
		got := events.Drain(th.id)
		if !reflect.DeepEqual(got, drained) {
			t.Fatalf("inbox lost on refused begin: got %v, want %v", got, drained)
		}
	}
}
