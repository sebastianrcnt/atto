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
// model is told to wrap up once (mid-turn) when the budget runs out, its
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

	// Steer delivers the budget message into the running turn.
	Steer func(string)
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

	turnStart  time.Time
	running    bool // a turn is in progress (BeginTurn..EndTurn)
	tools      int  // tool calls in the current turn
	budgetSent bool // budget message already steered into this turn
	userInput  bool // the running turn took user input (see UserInput)
	held       bool // waiting for the user after a turn with user input
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

// Poll takes a report the model wrote with atto goal complete|blocked|
// pause|resume, or a goal it set with atto goal set when none was
// unfinished.
func (d *GoalDriver) Poll() {
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
	d.turnStart, d.tools, d.budgetSent, d.running = time.Now(), 0, false, true
}

// Elapsed is the goal's time in seconds including the running turn, as
// codex's indicator counts it: the turn in progress adds to an active goal.
func (d *GoalDriver) Elapsed() int64 {
	g := d.Goal
	if g == nil {
		return 0
	}
	n := g.Seconds
	if d.running && g.Status == goal.Active {
		n += int64(time.Since(d.turnStart).Seconds())
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

// step accounts a model call against an active goal and, when the budget
// runs out, tells the model to wrap up (once, mid-turn as in codex).
func (d *GoalDriver) step(input, cached, output int) {
	d.Poll()
	g := d.Goal
	if g == nil || g.Status != goal.Active {
		return
	}
	exhausted := g.Account(input, cached, output)
	_ = goal.Save(d.Session, g) // the file follows memory; edits to it are dropped
	if exhausted && !d.budgetSent {
		d.budgetSent = true
		if d.Steer != nil {
			d.Steer(g.BudgetMessage())
		}
		d.changed()
	}
}

// EndTurn applies the stop conditions after a turn that ended with err.
// An interrupt pauses the goal; user input in the turn puts an active goal
// on hold. Returns true if the goal is still active (held or not).
func (d *GoalDriver) EndTurn(err error) bool {
	d.running = false
	d.Poll()
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
			g.Status, g.Note = goal.Paused, "interrupted"
		}
	case err != nil:
		failed = err
	}
	g.TurnEnded(time.Since(d.turnStart), failed, d.tools)
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
