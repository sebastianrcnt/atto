package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
)

func TestTurnRunnerCancellation(t *testing.T) {
	for _, tt := range []struct {
		name string
		stop func(*TurnRunner[string])
		want error
	}{
		{"interrupt", func(r *TurnRunner[string]) { r.Interrupt(true) }, agent.ErrUserInterrupt},
		{"compaction interrupt", func(r *TurnRunner[string]) { r.Interrupt(false) }, context.Canceled},
		{"shutdown", func(r *TurnRunner[string]) { r.Cancel(nil) }, context.Canceled},
		{"navigation", func(r *TurnRunner[string]) { r.Cancel(nil) }, context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var r TurnRunner[string]
			ctx, err := r.Begin(context.Background())
			if err != nil || !r.Busy {
				t.Fatalf("begin: %v, busy %v", err, r.Busy)
			}
			if _, err := r.Begin(context.Background()); !errors.Is(err, ErrTurnBusy) {
				t.Fatalf("second begin: %v", err)
			}
			tt.stop(&r)
			if !errors.Is(context.Cause(ctx), tt.want) || !r.Busy {
				t.Fatalf("cause %v, busy %v", context.Cause(ctx), r.Busy)
			}
			r.End()
			if r.Busy || r.Cancel != nil || !errors.Is(context.Cause(ctx), tt.want) {
				t.Fatal("end lost the cause or kept the turn busy")
			}
			r.Interrupt(true) // idle is a no-op
			ctx, err = r.Begin(context.Background())
			if err != nil || ctx.Err() != nil {
				t.Fatal("new turn inherited cancellation")
			}
			r.End()
		})
	}
}

func TestTurnRunnerParentCancellation(t *testing.T) {
	for _, cause := range []error{agent.ErrUserInterrupt, context.Canceled} {
		parent, cancel := context.WithCancelCause(context.Background())
		var r TurnRunner[string]
		ctx, err := r.Begin(parent)
		if err != nil {
			t.Fatal(err)
		}
		cancel(cause) // external worker interrupt, or print Ctrl+C
		if !errors.Is(context.Cause(ctx), cause) {
			t.Fatalf("parent cause lost: %v", context.Cause(ctx))
		}
		r.End()
	}
}

func TestTurnRunnerPendingOrder(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	ag := new(agent.Agent)
	var r TurnRunner[string]
	ctx, err := r.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r.Enqueue("queued first")
	r.Enqueue("queued second")
	r.Steer(ag, "s", "first steer", false)
	r.Steer(ag, "s", "second steer", false)
	if _, ok := r.NextQueued(); ok {
		t.Fatal("queued follow-up ran during the current turn")
	}
	now := "replacement"
	r.SendNow, r.SendSteersAfterInterrupt = &now, true
	r.Interrupt(true)
	r.End()
	follow := r.Settle(ag, ctx.Err())
	if !follow.Send || !slices.Equal(follow.Steers, []string{"first steer", "second steer"}) || follow.Now == nil || *follow.Now != now {
		t.Fatalf("replacement lost typing order: %+v", follow)
	}
	if r.QueuePaused || len(r.Steers) != 0 || r.SendNow != nil || r.SendSteersAfterInterrupt || len(ag.DrainSteers()) != 0 {
		t.Fatal("replacement retained state or paused the queued messages")
	}
	// Pending events are delivered before queued input at the turn boundary.
	r.PendingEvents = []events.Event{{Text: "job finished"}}
	delivery := r.DeliverEvents("s", false, true)
	if !delivery.Start || len(delivery.Events) != 1 {
		t.Fatalf("event delivery: %+v", delivery)
	}
	if _, err := r.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.NextQueued(); ok {
		t.Fatal("queue overtook the event turn")
	}
	r.End()
	for _, want := range []string{"queued first", "queued second"} {
		if got, ok := r.NextQueued(); !ok || got != want {
			t.Fatalf("queued input %q, %v, want %q", got, ok, want)
		}
	}
	if _, ok := r.NextQueued(); ok {
		t.Fatal("queue did not drain")
	}
}

func TestTurnRunnerSettle(t *testing.T) {
	failure := errors.New("model failed")
	for _, tt := range []struct {
		name        string
		err         error
		esc, now    bool
		send, pause bool
	}{
		{"success", nil, false, false, true, false},
		{"Esc", context.Canceled, true, false, true, false},
		{"Ctrl+C", context.Canceled, false, false, false, true},
		{"Ctrl+Enter", context.Canceled, false, true, true, false},
		{"failure", failure, true, false, false, true},
		{"failed replacement", failure, true, true, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ag := new(agent.Agent)
			var r TurnRunner[string]
			r.Enqueue("queued")
			r.Steer(ag, "", "draft", false)
			g, _ := goal.New("ship it")
			ag.Steer(g.Continuation()) // goal messages never start another turn
			r.SendSteersAfterInterrupt = tt.esc
			if tt.now {
				now := "replacement"
				r.SendNow = &now
			}
			follow := r.Settle(ag, tt.err)
			if follow.Send != tt.send || r.QueuePaused != tt.pause || !slices.Equal(follow.Steers, []string{"draft"}) || (follow.Now != nil) != tt.now {
				t.Fatalf("follow %+v, paused %v", follow, r.QueuePaused)
			}
			if tt.pause {
				if _, ok := r.NextQueued(); ok {
					t.Fatal("failed turn sent queued input")
				}
				r.QueuePaused = false // Enter on an empty prompt
			}
			if got, ok := r.NextQueued(); !ok || got != "queued" {
				t.Fatalf("queue was not retained: %q, %v", got, ok)
			}
		})
	}
	// An interrupt with no steers still pauses the queue.
	var r TurnRunner[string]
	r.Enqueue("queued")
	r.Settle(new(agent.Agent), context.Canceled)
	if !r.QueuePaused {
		t.Fatal("empty interrupted turn did not pause the queue")
	}
}

func TestTurnRunnerRetractAndCommit(t *testing.T) {
	ag := new(agent.Agent)
	var r TurnRunner[string]
	for _, text := range []string{"duplicate", "middle", "duplicate"} {
		r.Steer(ag, "", text, false)
	}
	if text, ok := r.EditLastSteer(ag); !ok || text != "duplicate" || !slices.Equal(r.Steers, []string{"duplicate", "middle"}) {
		t.Fatalf("last duplicate: %q %v %q", text, ok, r.Steers)
	}
	if !r.Unsteer(ag, "middle") || !slices.Equal(r.Steers, []string{"duplicate"}) {
		t.Fatal("remote retraction failed")
	}
	if !slices.Equal(ag.DrainSteers(), []string{"duplicate"}) {
		t.Fatal("agent steers disagree with pending input")
	}
	// The agent has taken it already: Shift+Left must leave display alone
	// until the committed event reaches the front end.
	if _, ok := r.EditLastSteer(ag); ok || r.Unsteer(ag, "duplicate") {
		t.Fatal("retracted a committed steer")
	}
	if !r.Committed("duplicate") || r.Committed("event") || len(r.Steers) != 0 {
		t.Fatal("committed user steers were not removed")
	}
}

func TestTurnRunnerEvents(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for _, tt := range []struct {
		name          string
		busy, quiet   bool
		blocked, turn bool
		start, steer  bool
		requeue, held bool
	}{
		{"idle event", false, false, false, true, true, false, false, false},
		{"idle interrupt exit", false, true, false, true, false, false, true, false},
		{"busy event", true, false, false, true, false, true, false, false},
		{"busy quiet event", true, true, false, true, false, true, false, false},
		{"picker", false, false, true, true, false, false, false, true},
		{"paused queue", true, false, true, true, false, false, false, true},
		{"TUI compaction", true, false, false, false, false, false, false, true},
		{"daemon compaction", true, false, false, true, false, true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ag := new(agent.Agent)
			var r TurnRunner[string]
			r.Busy = tt.busy
			evs := []events.Event{{Text: "job 1 exited", Quiet: tt.quiet}}
			r.PendingEvents = slices.Clone(evs)
			delivery := r.DeliverEvents("s", tt.blocked, tt.turn)
			r.SteerEvents(ag, delivery)
			steers := ag.DrainSteers()
			if delivery.Start != tt.start || (len(steers) > 0) != tt.steer || (len(r.PendingEvents) > 0) != tt.held {
				t.Fatalf("delivery %+v, steers %q, pending %+v", delivery, steers, r.PendingEvents)
			}
			if got := events.Drain("s"); (len(got) > 0) != tt.requeue {
				t.Fatalf("requeued %+v", got)
			}
			if tt.start || tt.steer {
				if !reflect.DeepEqual(delivery.Events, evs) || delivery.Text != events.Format(evs) {
					t.Fatal("delivery changed the event payload")
				}
			}
		})
	}
}

func TestTurnRunnerQuietMixedWithWakingEvents(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	var r TurnRunner[string]
	r.PendingEvents = []events.Event{{Text: "quiet", Quiet: true}, {Text: "timer"}}
	if r.RequeueQuiet("s") {
		t.Fatal("waking event was held with quiet messages")
	}
	if got := r.DeliverEvents("s", false, true); !got.Start || len(got.Events) != 2 {
		t.Fatalf("mixed events: %+v", got)
	}
	r.PendingEvents = []events.Event{{Text: "quiet", Quiet: true}}
	if !r.RequeueQuiet("s") || len(r.PendingEvents) != 0 || len(events.Drain("s")) != 1 {
		t.Fatal("idle picker did not preserve quiet events in the inbox")
	}
}

func TestTurnRunnerRequests(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	requests := make(chan []map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body.Messages
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	ag := agent.New(config.ModelRef{ProviderName: "test", Provider: config.Provider{BaseURL: srv.URL},
		Model: config.Model{ID: "m", Input: []string{"text", "image"}}}, "", t.TempDir())
	var runner TurnRunner[string]
	image := provider.Image{File: "test.png", MIME: "image/png", Data: []byte("image bytes")}
	for i, in := range []TurnRequest{
		{Text: "text"},
		{Text: "image", Images: []provider.Image{image}},
		{Text: events.Format([]events.Event{{Text: "finished"}})},
		{Continue: true},
	} {
		ctx, err := runner.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(ctx, ag, in, func(any) {}); err != nil {
			t.Fatal(err)
		}
		runner.End()
		body := <-requests
		last := body[len(body)-1]
		if in.Continue {
			if last["role"] != "assistant" || len(ag.Messages()) != 7 {
				t.Fatal("continuation appended a user message")
			}
			continue
		}
		if last["role"] != "user" || len(ag.Messages()) != (i+1)*2 {
			t.Fatalf("request %d lost user input: %v", i, last)
		}
		if len(in.Images) > 0 {
			if !strings.Contains(fmt.Sprint(last["content"]), "data:image/png;base64,") || len(ag.Messages()[2].Images) != 1 {
				t.Fatalf("image not forwarded: %v", last)
			}
		} else if last["content"] != in.Text {
			t.Fatalf("text not forwarded: %v", last)
		}
	}
}

func TestTurnRunnerBoundaryInbox(t *testing.T) {
	for _, tt := range []struct {
		name   string
		worker bool
		quiet  bool
		calls  int32
	}{
		{"print waking event", false, false, 1},
		{"print quiet event", false, true, 1},
		{"worker waking event", true, false, 2},
		{"worker quiet event", true, true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ATTO_DIR", t.TempDir())
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					if err := events.Push("s", events.Event{Text: "finished", Quiet: tt.quiet}); err != nil {
						t.Error(err)
					}
					if err := events.RequestReload("s"); err != nil {
						t.Error(err)
					}
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()
			ag := agent.New(config.ModelRef{ProviderName: "test", Provider: config.Provider{BaseURL: srv.URL},
				Model: config.Model{ID: "m"}}, "", t.TempDir())
			var runner TurnRunner[string]
			runner.BoundaryInbox(ag, "s", tt.worker)
			ctx, err := runner.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(ctx, ag, TurnRequest{Text: "start"}, func(any) {}); err != nil {
				t.Fatal(err)
			}
			runner.End()
			if calls.Load() != tt.calls {
				t.Fatalf("requests %d, want %d", calls.Load(), tt.calls)
			}
			got := events.Drain("s")
			if tt.calls == 1 {
				if len(got) != 1 || got[0].Text != "finished" || got[0].Quiet != tt.quiet {
					t.Fatalf("events not preserved (or reload not discarded): %+v", got)
				}
			} else if len(got) != 0 {
				t.Fatalf("worker left waking event behind: %+v", got)
			}
		})
	}
}
