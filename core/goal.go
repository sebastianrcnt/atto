package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
)

// GoalDriver keeps a session's goal going across turns, the same way in
// every front end: each model call's tokens and the turn's time are
// accounted to the goal, its complete/blocked/pause/resume reports are
// taken from the goal file, the stop conditions apply when a turn ends and
// an interrupt pauses the goal.
//
// It works event-driven, as the TUI uses it (BeginTurn, Event for every
// agent event, EndTurn, then Next when idle), or as a loop (Run), as
// atto -p does.
//
// A turn that took real user input (a message that started it, or a steer)
// puts the goal on hold when it ends: the goal stays active but no
// continuation starts until the user continues (Release), so the model's
// answer is not buried under more goal work. Goal continuations and events
// are not user input.
//
// A turn that fails for a reason a retry might fix (goal.IsTransient: all
// but what ai.IsPermanent rules out) does not stall the goal at once: the continuation
// is retried after a growing delay (retryDelays), and only when those
// retries fail too does the goal stall. The driver only says when (Pending);
// the front end waits, so it stays responsive, and Run waits itself.
//
// The goal lives in memory; the goal file is how the model reports back
// (atto goal complete|blocked|pause|resume), and only those reports are taken from it
// (goal.Adopt), so editing the file cannot rewrite the objective. The driver is not safe for concurrent use.
type GoalDriver struct {
	Session string     // session ID: names the goal file
	Goal    *goal.Goal // nil when the session has none

	// Steer delivers internal messages (what the user did to the goal) into
	// the running turn.
	Steer func(string)
	// Stop, if set, ends the running turn at its next step boundary (the
	// user cleared or paused the goal: the turn should not go on).
	Stop func()
	// Snapshot, if set, records the goal in the session file whenever it
	// is saved (nil when cleared), so a resumed session gets it back.
	Snapshot func(*goal.Goal)
	// Changed, if set, is told when the goal's status changed by itself:
	// the model reported, a stop condition tripped or an interrupt paused
	// it.
	Changed func(*goal.Goal)
	// Adopted, if set, is told about a goal the model set with atto goal
	// set (at the user's request, as codex's create_goal) or resumed with
	// atto goal resume.
	Adopted func(*goal.Goal)
	// Error, if set, receives failures to read or write the goal file.
	Error func(error)
	// Retrying, if set, is told that a turn failed for a transient reason and
	// the goal will be retried (see Pending).
	Retrying func(Retry)

	running bool // a turn is in progress (BeginTurn..EndTurn)
	tools   int  // tool calls in the current turn
	// counted: the goal is part of the running turn, so every model call of
	// it is accounted, even once the goal is complete or paused. timing: the
	// turn's time still adds to Seconds (until the goal stops being
	// active). lastFold is the time up to which it has been added.
	counted, timing bool
	lastFold        time.Time
	userInput       bool // the running turn took user input (see UserInput)
	replaced        bool // the running turn is being interrupted for a message of the user (see Replace)
	held            bool // waiting for the user after a turn with user input

	retries int    // transient failures in a row, retried so far
	retry   *Retry // the retry waiting to start (see Pending)
	// live mirrors that the goal is active, for SteerNote, which the agent
	// calls from its turn's goroutine.
	live atomic.Bool
}

// retryDelays are the waits before the retries of a goal turn that failed
// for a transient reason; when the last retry fails the goal stalls. The
// turn already retried its request (agent.streamRetries): these are the
// goal's longer safety net, for an outage of minutes, not seconds.
var retryDelays = []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

// Retry is a goal turn that failed for a transient reason, to be tried again
// at At: attempt Attempt of Of.
type Retry struct {
	Attempt, Of int
	At          time.Time
	Err         error
}

// Notice is what to tell the user: "Model error (…); retrying the goal in
// 30s (2/3)."
func (r Retry) Notice() string {
	msg := strings.Join(strings.Fields(r.Err.Error()), " ")
	if rs := []rune(msg); len(rs) > 120 {
		msg = string(rs[:119]) + "…"
	}
	wait := goal.FormatElapsed(int64((time.Until(r.At) + time.Second/2) / time.Second))
	return fmt.Sprintf("Model error (%s); retrying the goal in %s (%d/%d).", msg, wait, r.Attempt, r.Of)
}

func (d *GoalDriver) fail(err error) {
	if err != nil && d.Error != nil {
		d.Error(err)
	}
}

func (d *GoalDriver) changed() {
	if d.Changed != nil {
		d.Changed(d.Goal)
	}
}

// Set replaces the goal (nil clears it), writes the goal file and records
// a snapshot.
func (d *GoalDriver) Set(g *goal.Goal) {
	if g != d.Goal { // another goal: the running turn is not part of it yet
		d.counted, d.timing, d.lastFold = false, false, time.Now()
		d.retries, d.retry = 0, nil
		d.track()
	}
	d.Goal = g
	d.live.Store(running(g))
	if !running(g) { // paused, cleared, finished: nothing to retry
		d.retries, d.retry = 0, nil
	}
	if g == nil {
		_ = goal.Clear(d.Session)
	} else {
		d.fail(goal.Save(d.Session, g))
	}
	if d.Snapshot != nil {
		d.Snapshot(g)
	}
}

// running reports whether g keeps accruing: it is active.
func running(g *goal.Goal) bool { return g != nil && g.Status == goal.Active }

// track notes that the goal is part of the running turn.
func (d *GoalDriver) track() {
	if d.running && running(d.Goal) {
		d.counted, d.timing = true, true
	}
}

// fold adds the running turn's whole seconds to the goal's time, so that
// the indicator, /goal and the completion notice read one number. The time
// stops once the goal is no longer active (complete, paused...).
func (d *GoalDriver) fold() {
	if !d.running || !d.timing || d.Goal == nil {
		return
	}
	n := int64(time.Since(d.lastFold) / time.Second)
	d.Goal.Seconds += n
	d.lastFold = d.lastFold.Add(time.Duration(n) * time.Second)
	if !running(d.Goal) {
		d.timing = false
	}
}

// Poll takes a report the model wrote with atto goal complete|blocked|
// pause|resume, or a goal it set with atto goal set when none was
// unfinished.
func (d *GoalDriver) Poll() {
	d.fold()
	g := d.Goal
	file, err := goal.Load(d.Session)
	if err != nil {
		d.fail(err)
		return
	}
	if g == nil || g.Status == goal.Complete {
		if file != nil && file.Status == goal.Active && (g == nil || !file.Created.Equal(g.Created)) {
			d.Set(file)
			d.Release() // a new goal starts at once, even from a user's turn
			if d.Adopted != nil {
				d.Adopted(file)
			}
		}
		return
	}
	was := g.Status
	if g.Adopt(file) {
		d.fold() // the time up to the report, before the status change is announced
		d.Set(g)
		if g.Status == goal.Active && was != goal.Active {
			// The model resumed the goal at the user's request: their message
			// was the go-ahead, so there is nothing to wait for.
			d.Release()
			if d.Adopted != nil {
				d.Adopted(g)
			}
			return
		}
		d.changed()
	}
}

// Active reports whether the goal wants more turns.
func (d *GoalDriver) Active() bool { return d.Goal != nil && d.Goal.Status == goal.Active }

// Tell steers an internal message into the running turn, if there is one:
// what the user just did to the goal (cleared, paused), which the
// model cannot otherwise know, and has the turn stop at its next step
// boundary. It reports whether there was a turn to tell.
func (d *GoalDriver) Tell(msg string) bool {
	if !d.running {
		return false
	}
	if d.Steer != nil {
		d.Steer(msg)
	}
	if d.Stop != nil {
		d.Stop()
	}
	return true
}

// StateNote is the note for a user message that starts a turn while the
// goal is not running by itself (waiting for the user, paused, stalled,
// usage limited); empty when there is none to give. Goal continuations and
// events are not user turns and take none.
func (d *GoalDriver) StateNote() string {
	if d.Goal == nil {
		return ""
	}
	return d.Goal.StateMessage(d.Held())
}

// SteerNote is the note for a steer committed into a running turn (see
// agent.SteerNote): a user's message, while the goal is active, is not about
// a paused goal, whatever the model makes of its silence. Events and goal
// messages take none. Safe to call from any goroutine.
func (d *GoalDriver) SteerNote(text string) string {
	if !d.live.Load() || events.IsEvent(text) || goal.IsMessage(text) {
		return ""
	}
	return goal.SteerMessage()
}

// UserInput notes that the user's own input went into the running turn (or
// the turn about to begin): when it ends, the goal waits for the user. It
// also takes the place of a retry still waiting.
func (d *GoalDriver) UserInput() { d.userInput, d.retry = true, nil }

// Replace notes that the running turn is being interrupted to make way for
// a message the user sends at once: the interrupt does not pause the goal
// (nor does the cut-off turn count as one without progress), and the
// message's own turn puts the goal on hold, as UserInput does there.
func (d *GoalDriver) Replace() { d.replaced = true }

// Held reports whether the goal is active but waiting for the user to
// continue it, after a turn that took user input.
func (d *GoalDriver) Held() bool { return d.held && d.Active() }

// Release ends the hold (the user continued, resumed or set a new goal)
// and forgets user input noted so far in the running turn.
func (d *GoalDriver) Release() { d.held, d.userInput = false, false }

// BeginTurn starts counting a turn.
func (d *GoalDriver) BeginTurn() {
	d.lastFold, d.tools, d.running, d.retry = time.Now(), 0, true, nil
	d.counted, d.timing = false, false
	d.track()
}

// Elapsed is the goal's time in seconds including the running turn, as
// codex's indicator counts it: the turn in progress adds to a goal that is
// active, and is in Seconds (once) from then on.
func (d *GoalDriver) Elapsed() int64 {
	g := d.Goal
	if g == nil {
		return 0
	}
	n := g.Seconds
	if d.running && d.timing {
		n += int64(time.Since(d.lastFold) / time.Second)
	}
	return n
}

// Event follows the running turn: tool calls count as progress, and each
// model call is accounted.
func (d *GoalDriver) Event(ev any) {
	switch e := ev.(type) {
	case agent.ToolStart:
		d.tools++
	case agent.StepEnd:
		d.step(e.Usage.PromptTokens, e.Usage.CachedTokens, e.Usage.CompletionTokens)
	}
}

// step accounts a model call to the goal. A goal that is part of the turn
// is accounted to the turn's end, whatever happens to it meanwhile
// (complete, paused).
func (d *GoalDriver) step(input, cached, output int) {
	d.Poll()
	g := d.Goal
	if g == nil {
		return
	}
	d.track()
	if !d.counted {
		return
	}
	g.Account(input, cached, output)
	_ = goal.Save(d.Session, g) // the file follows memory; edits to it are dropped
}

// EndTurn applies the stop conditions after a turn that ended with err.
// An interrupt pauses the goal (unless it was a Replace); user input in the
// turn puts an active goal on hold. Returns true if the goal is still active (held or not).
func (d *GoalDriver) EndTurn(err error) bool {
	d.Poll() // folds the turn's time
	d.running = false
	user := d.userInput // after Poll: a goal adopted from the model starts at once
	d.userInput = false
	replaced := d.replaced && errors.Is(err, context.Canceled)
	d.replaced = false
	g := d.Goal
	if g == nil {
		return false
	}
	wasActive := g.Status == goal.Active
	var failed error
	switch {
	case replaced:
	case errors.Is(err, context.Canceled):
		if g.Status == goal.Active {
			g.Status, g.Note = goal.Paused, goal.NoteInterrupted
		}
	case err != nil:
		failed = err
	}
	switch {
	case replaced:
		g.Turns++ // cut off, so neither progress nor a lack of it
	case failed != nil && goal.IsTransient(failed) && g.Status == goal.Active:
		g.Turns++ // not yet a stall (see GoalDriver)
		switch {
		case user:
			// The user's own message failed: they see the error and say it
			// again; the goal waits for them as after any message of theirs.
		case d.retries < len(retryDelays):
			d.retry = &Retry{Attempt: d.retries + 1, Of: len(retryDelays), At: time.Now().Add(retryDelays[d.retries]), Err: failed}
			d.retries++
			if d.Retrying != nil {
				d.Retrying(*d.retry)
			}
		default:
			g.TurnEnded(failed, d.tools) // the retries failed too: stalls
			if g.Status == goal.Blocked {
				g.Note = fmt.Sprintf("model errors persisted after %d retries (last: %v)", d.retries, failed)
			}
			d.retries = 0
		}
	default:
		if err == nil {
			d.retries = 0
		}
		g.TurnEnded(failed, d.tools)
	}
	d.Set(g)
	if wasActive && g.Status != goal.Active {
		d.changed()
	}
	if user && g.Status == goal.Active {
		d.held = true
	}
	return g.Status == goal.Active
}

// Next is the input of the next goal turn, if the goal is still active
// and not waiting for the user.
func (d *GoalDriver) Next() (string, bool) {
	d.Poll()
	if !d.Active() || d.held {
		return "", false
	}
	d.retry = nil // the retry starts now
	return d.Goal.Continuation(), true
}

// Pending is the retry of a failed turn that has not started yet, or nil:
// the front end waits until At (or less, for a hurried user) before Next.
func (d *GoalDriver) Pending() *Retry {
	if d.retry == nil || !d.Active() || d.held {
		return nil
	}
	return d.retry
}

// Run runs turns until the goal stops being active, starting with input:
// turn runs one, passing its events to emit. Without a goal it runs one
// turn. between runs before each continuation. Returns the last turn's
// error.
func (d *GoalDriver) Run(ctx context.Context, input string, turn func(ctx context.Context, input string, emit func(any)) error, emit func(any), between func()) error {
	for {
		d.BeginTurn()
		err := turn(ctx, input, func(ev any) {
			emit(ev)
			d.Event(ev)
		})
		if !d.EndTurn(err) || ctx.Err() != nil {
			return err
		}
		if r := d.Pending(); r != nil { // a transient failure: wait, unless interrupted
			t := time.NewTimer(time.Until(r.At))
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return err
			}
		}
		next, ok := d.Next()
		if !ok {
			return err
		}
		if between != nil {
			between()
		}
		input = next
	}
}

// Restore brings a resumed session's goal back from its last snapshot. An
// active goal comes back paused, so resuming never starts work by itself;
// paused reports that.
func (d *GoalDriver) Restore(entries []session.Entry) (paused bool) {
	var last json.RawMessage
	for _, e := range entries {
		if e.Type == session.TypeGoal {
			last = e.Goal
		}
	}
	d.Goal = nil
	d.live.Store(false)
	d.Release()
	if len(last) == 0 || string(last) == "null" {
		_ = goal.Clear(d.Session)
		return false
	}
	var g goal.Goal
	if json.Unmarshal(last, &g) != nil {
		return false
	}
	if g.Status == goal.Active {
		g.Status, g.Note = goal.Paused, "session resumed"
		paused = true
	}
	_ = goal.Save(d.Session, &g)
	d.Goal = &g
	d.live.Store(running(&g))
	return paused
}
