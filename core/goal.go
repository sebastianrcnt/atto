package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
)

// GoalDriver keeps a session's goal going across turns, the same way in
// every front end: each model call is accounted against the budget, the
// model is told to wrap up (mid-turn) when the budget runs out, reminded as
// it keeps going, and its turn stopped at twice the budget; its
// complete/blocked/pause/resume reports are taken from the goal file, the stop
// conditions apply when a turn ends and an interrupt pauses the goal.
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
// The goal lives in memory; the goal file is how the model reports back
// (atto goal complete|blocked|pause|resume), and only those reports are taken from it
// (goal.Adopt), so editing the file cannot rewrite the objective or the
// budget. The driver is not safe for concurrent use.
type GoalDriver struct {
	Session string     // session ID: names the goal file
	Goal    *goal.Goal // nil when the session has none

	// Steer delivers internal messages (the budget messages, what the user
	// did to the goal) into the running turn.
	Steer func(string)
	// Stop, if set, ends the running turn at its next step boundary, as a
	// normal stop (the turn is over budget); Notice, if set, tells the user.
	Stop   func()
	Notice func(string)
	// Snapshot, if set, records the goal in the session file whenever it
	// is saved (nil when cleared), so a resumed session gets it back.
	Snapshot func(*goal.Goal)
	// Changed, if set, is told when the goal's status changed by itself:
	// the model reported, the budget ran out, a stop condition tripped or
	// an interrupt paused it.
	Changed func(*goal.Goal)
	// Adopted, if set, is told about a goal the model set with atto goal
	// set (at the user's request, as codex's create_goal) or resumed with
	// atto goal resume.
	Adopted func(*goal.Goal)
	// Error, if set, receives failures to read or write the goal file.
	Error func(error)

	running    bool // a turn is in progress (BeginTurn..EndTurn)
	tools      int  // tool calls in the current turn
	budgetSent bool // budget message already steered into this turn
	stopped    bool // the turn was stopped for exceeding the budget
	mark       int  // tokens used when the budget message or reminder last went out
	// counted: the goal is part of the running turn, so every model call of
	// it is accounted, even once the goal is complete or paused. timing: the
	// turn's time still adds to Seconds (until the goal stops being active
	// or limited). lastFold is the time up to which it has been added.
	counted, timing bool
	lastFold        time.Time
	userInput       bool // the running turn took user input (see UserInput)
	held            bool // waiting for the user after a turn with user input
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
		d.track()
	}
	d.Goal = g
	if g == nil {
		_ = goal.Clear(d.Session)
	} else {
		d.fail(goal.Save(d.Session, g))
	}
	if d.Snapshot != nil {
		d.Snapshot(g)
	}
}

// running reports whether g keeps accruing: it is active or limited by budget.
func running(g *goal.Goal) bool {
	return g != nil && (g.Status == goal.Active || g.Status == goal.BudgetLimited)
}

// track notes that the goal is part of the running turn.
func (d *GoalDriver) track() {
	if d.running && running(d.Goal) {
		d.counted, d.timing = true, true
	}
}

// fold adds the running turn's whole seconds to the goal's time, so that
// the indicator, /goal and the completion notice read one number. The time
// stops once the goal is no longer active or limited (complete, paused...).
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
// what the user just did to the goal (cleared, paused, budget), which the
// model cannot otherwise know.
func (d *GoalDriver) Tell(msg string) {
	if d.running && d.Steer != nil {
		d.Steer(msg)
	}
}

// StateNote is the note for a user message that starts a turn while the
// goal is not running by itself (waiting for the user, paused, stalled,
// limited); empty when there is none to give. Goal continuations and
// events are not user turns and take none.
func (d *GoalDriver) StateNote() string {
	if d.Goal == nil {
		return ""
	}
	return d.Goal.StateMessage(d.Held())
}

// UserInput notes that the user's own input went into the running turn (or
// the turn about to begin): when it ends, the goal waits for the user.
func (d *GoalDriver) UserInput() { d.userInput = true }

// Held reports whether the goal is active but waiting for the user to
// continue it, after a turn that took user input.
func (d *GoalDriver) Held() bool { return d.held && d.Active() }

// Release ends the hold (the user continued, resumed or set a new goal)
// and forgets user input noted so far in the running turn.
func (d *GoalDriver) Release() { d.held, d.userInput = false, false }

// BeginTurn starts counting a turn.
func (d *GoalDriver) BeginTurn() {
	d.lastFold, d.tools, d.running = time.Now(), 0, true
	d.budgetSent, d.stopped, d.counted, d.timing = false, false, false, false
	d.track()
}

// Elapsed is the goal's time in seconds including the running turn, as
// codex's indicator counts it: the turn in progress adds to a goal that is
// active or limited, and is in Seconds (once) from then on.
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

// budgetStep is how much usage past the budget message (or the last
// reminder) brings the next reminder: a quarter of the budget. At twice the
// budget the turn is stopped.
func budgetStep(budget int) int { return max(1, budget/4) }

// step accounts a model call against the goal and, when the budget runs
// out, tells the model to wrap up (mid-turn as in codex). A model that goes
// on past that is reminded every quarter budget and stopped at twice the
// budget, so it cannot work its way to completion on tokens it was refused.
// A goal that is part of the turn is accounted to the turn's end, whatever
// happens to it meanwhile (complete, paused, limited).
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
	exhausted := g.Account(input, cached, output)
	_ = goal.Save(d.Session, g) // the file follows memory; edits to it are dropped
	if exhausted && !d.budgetSent {
		d.budgetSent, d.mark = true, g.TokensUsed
		if d.Steer != nil {
			d.Steer(g.BudgetMessage())
		}
		d.changed()
	}
	if !d.budgetSent || g.Status != goal.BudgetLimited || g.Budget <= 0 || d.stopped {
		return
	}
	switch {
	case g.TokensUsed >= 2*g.Budget:
		d.stopped = true
		if d.Stop != nil {
			d.Stop()
		}
		if d.Notice != nil {
			d.Notice("Goal budget exceeded: stopped the turn.")
		}
	case g.TokensUsed-d.mark >= budgetStep(g.Budget):
		d.mark = g.TokensUsed
		if d.Steer != nil {
			d.Steer(g.BudgetReminderMessage())
		}
	}
}

// EndTurn applies the stop conditions after a turn that ended with err.
// An interrupt pauses the goal; user input in the turn puts an active goal
// on hold. Returns true if the goal is still active (held or not).
func (d *GoalDriver) EndTurn(err error) bool {
	d.Poll() // folds the turn's time
	d.running = false
	user := d.userInput // after Poll: a goal adopted from the model starts at once
	d.userInput = false
	g := d.Goal
	if g == nil {
		return false
	}
	wasActive := g.Status == goal.Active
	var failed error
	switch {
	case errors.Is(err, context.Canceled):
		if g.Status == goal.Active {
			g.Status, g.Note = goal.Paused, goal.NoteInterrupted
		}
	case err != nil:
		failed = err
	}
	g.TurnEnded(failed, d.tools)
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
	return d.Goal.Continuation(), true
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
	return paused
}
