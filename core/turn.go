package core

import (
	"context"
	"errors"
	"slices"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
)

var ErrTurnBusy = errors.New("a turn is already running")

// TurnRunner owns a conversation's running turn and pending input. T is the
// front end's follow-up payload (the TUI keeps attachment labels and remote
// provenance with its text). The zero value is ready to use. Like transcript
// and GoalDriver, it is serialized by its front end: the UI goroutine, or the
// server's thread lock. Run executes outside that lock; the agent's steering
// methods are safe while it runs.
//
// The state is public so display and navigation can inspect it and restore
// drafts without translating or duplicating it. Begin and End bracket all
// runs, including compaction; only Interrupt marks a user cancellation.
// Front ends decide when display/input gates (pickers, trust, goal retries)
// allow pending work to run.
type TurnRunner[T any] struct {
	Busy   bool
	Cancel context.CancelCauseFunc

	Steers                   []string
	Queued                   []T
	SendSteersAfterInterrupt bool
	SendNow                  *T
	QueuePaused              bool
	PendingEvents            []events.Event
}

// Begin claims the turn before a front end launches its background work.
// The parent preserves print/worker cancellation causes when used there.
func (r *TurnRunner[T]) Begin(parent context.Context) (context.Context, error) {
	if r.Busy {
		return nil, ErrTurnBusy
	}
	ctx, cancel := context.WithCancelCause(parent)
	r.Busy, r.Cancel = true, cancel
	return ctx, nil
}

// End releases the turn after all of its boundary work has finished.
func (r *TurnRunner[T]) End() {
	if r.Cancel != nil {
		r.Cancel(nil)
	}
	r.Busy, r.Cancel = false, nil
}

// Interrupt is a user stop. Non-turn work (compaction, branch summaries)
// still uses ordinary cancellation; quit, navigation and shutdown call
// Cancel(nil) instead and must never detach a shell command.
func (r *TurnRunner[T]) Interrupt(turn bool) {
	if r.Cancel == nil {
		return
	}
	if turn {
		r.Cancel(agent.ErrUserInterrupt)
	} else {
		r.Cancel(nil)
	}
}

// TurnRequest selects a text/image turn or a continuation without a new
// user message. Transcript input is displayed by the front end, not here.
type TurnRequest struct {
	Text     string
	Images   []provider.Image
	Continue bool
}

func (r *TurnRunner[T]) Run(ctx context.Context, ag *agent.Agent, in TurnRequest, emit func(any)) error {
	if in.Continue {
		return ag.Continue(ctx, emit)
	}
	return ag.RunWithImages(ctx, in.Text, in.Images, emit)
}

// Steer tracks only user input. Events and goal messages reach the agent
// too, but are not shown as retractable user steers. wake is true in the
// TUI, where user input also wakes atto sleep/job wait; the server has
// historically not sent that wake signal.
func (r *TurnRunner[T]) Steer(ag *agent.Agent, session, text string, wake bool) {
	ag.Steer(text)
	r.Steers = append(r.Steers, text)
	if wake {
		events.Wake(session)
	}
}

// Committed removes the first matching user steer, as transcript events do.
func (r *TurnRunner[T]) Committed(text string) bool {
	if i := slices.Index(r.Steers, text); i >= 0 {
		r.Steers = slices.Delete(r.Steers, i, i+1)
		return true
	}
	return false
}

func (r *TurnRunner[T]) Unsteer(ag *agent.Agent, text string) bool {
	if !slices.Contains(r.Steers, text) || !ag.Unsteer(text) {
		return false
	}
	r.Committed(text)
	return true
}

// EditLastSteer is Shift+Left: retract the last tracked message, not the
// first matching one (duplicate drafts can have other steers between them).
func (r *TurnRunner[T]) EditLastSteer(ag *agent.Agent) (string, bool) {
	n := len(r.Steers)
	if n == 0 {
		return "", false
	}
	text := r.Steers[n-1]
	if !ag.Unsteer(text) {
		return "", false
	}
	r.Steers = r.Steers[:n-1]
	return text, true
}

func (r *TurnRunner[T]) Enqueue(in T) { r.Queued = append(r.Queued, in) }

// NextQueued is called after steers and events, and before a goal continues.
func (r *TurnRunner[T]) NextQueued() (T, bool) {
	if r.Busy || r.QueuePaused || len(r.Queued) == 0 {
		var zero T
		return zero, false
	}
	next := r.Queued[0]
	r.Queued = r.Queued[1:]
	return next, true
}

// FollowUp is pending input from a finished turn. Send means start it now;
// otherwise restore it to the editor. Now follows Steers in typing order.
type FollowUp[T any] struct {
	Steers []string
	Now    *T
	Send   bool
}

// Settle implements the TUI's end-of-turn policy. Esc may send remaining
// steers; Ctrl+Enter sends those steers followed by its replacement draft.
// An error restores drafts and pauses queued follow-ups. Goal notes belonged
// to the finished turn and can never start the next one. The daemon does
// not settle: it historically leaves uncommitted steers for its next turn.
func (r *TurnRunner[T]) Settle(ag *agent.Agent, err error) FollowUp[T] {
	left := slices.DeleteFunc(ag.DrainSteers(), goal.IsMessage)
	canceled := errors.Is(err, context.Canceled)
	out := FollowUp[T]{Steers: left, Now: r.SendNow,
		Send: err == nil || canceled && (r.SendNow != nil || r.SendSteersAfterInterrupt)}
	r.Steers, r.SendNow, r.SendSteersAfterInterrupt = nil, nil, false
	if err != nil && len(r.Queued) > 0 {
		// A replacement turn takes precedence and gets its own chance to
		// finish before queued input is paused.
		if !out.Send || len(out.Steers) == 0 && out.Now == nil {
			r.QueuePaused = true
		}
	}
	return out
}

// RequeueQuiet preserves quiet idle messages even while input gates prevent
// delivery (for example an open TUI picker). It reports whether it did so.
func (r *TurnRunner[T]) RequeueQuiet(session string) bool {
	if len(r.PendingEvents) == 0 || r.Busy || events.Wakes(r.PendingEvents) {
		return false
	}
	events.Requeue(session, r.PendingEvents)
	r.PendingEvents = nil
	return true
}

// EventDelivery asks the front end to display and steer a running turn,
// or start a new one. If claiming that turn races with other
// input, the front end requeues Events, just as when changing sessions.
type EventDelivery struct {
	Events []events.Event
	Text   string
	Start  bool
}

// DeliverEvents centralizes quiet-event and busy/idle rules. blocked covers
// UI pickers and paused queues; acceptSteer is false during TUI compaction,
// but true in the daemon, which has always steered any running work.
func (r *TurnRunner[T]) DeliverEvents(session string, blocked, acceptSteer bool) EventDelivery {
	if len(r.PendingEvents) == 0 || blocked || r.Busy && !acceptSteer {
		return EventDelivery{}
	}
	evs := r.PendingEvents
	r.PendingEvents = nil
	if !r.Busy && !events.Wakes(evs) {
		events.Requeue(session, evs) // interrupt-detached exits wait for the next turn
		return EventDelivery{}
	}
	out := EventDelivery{Events: evs, Text: events.Format(evs), Start: !r.Busy}
	return out
}

// SteerEvents follows display/notification of the events, so a committed
// steer cannot overtake its event notification. An idle delivery instead
// starts a turn using Run; a failed claim must requeue the events.
func (r *TurnRunner[T]) SteerEvents(ag *agent.Agent, delivery EventDelivery) {
	if !delivery.Start && len(delivery.Events) > 0 {
		ag.Steer(delivery.Text)
	}
}

// BoundaryInbox polls between model steps in a synchronous print/worker
// run. A plain -p run never extends a completed turn; a worker extends it
// only for waking events. Quiet events wait even though this final boundary
// still belongs to the running turn. Print runs load settings once, so
// reload requests are discarded, as they were before TurnRunner.
func (r *TurnRunner[T]) BoundaryInbox(ag *agent.Agent, session string, continueOnWake bool) {
	var poll func() string
	poll = func() string {
		ag.AtBoundary(poll)
		_, evs := events.SplitReload(Poll(session))
		if ag.AtStop() && (!continueOnWake || !events.Wakes(evs)) {
			events.Requeue(session, evs)
			return ""
		}
		return events.Format(evs)
	}
	ag.AtBoundary(poll)
}
