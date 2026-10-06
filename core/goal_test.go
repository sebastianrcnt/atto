package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
)

func step(in, cached, out int) agent.StepEnd {
	return agent.StepEnd{Usage: provider.Usage{PromptTokens: in, CachedTokens: cached, CompletionTokens: out}}
}

func TestGoalDriverBudget(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it", 100)
	var steers []string
	var changes []goal.Status
	snaps := 0
	d := GoalDriver{Session: "s", Goal: g,
		Steer:    func(s string) { steers = append(steers, s) },
		Changed:  func(g *goal.Goal) { changes = append(changes, g.Status) },
		Snapshot: func(*goal.Goal) { snaps++ },
	}
	d.BeginTurn()
	d.Event(agent.ToolStart{})
	d.Event(step(60, 20, 10)) // 50 new tokens
	if len(steers) != 0 || g.TokensUsed != 50 {
		t.Fatalf("under budget: %v %d", steers, g.TokensUsed)
	}
	d.Event(step(80, 60, 40)) // 60 more: over
	d.Event(step(80, 60, 40)) // no longer active: not counted, no second message
	if len(steers) != 1 || !strings.Contains(steers[0], "budget") || g.Status != goal.BudgetLimited || g.TokensUsed != 110 {
		t.Fatalf("budget: %v %s %d", steers, g.Status, g.TokensUsed)
	}
	if d.EndTurn(nil) || g.Turns != 1 || snaps != 1 {
		t.Fatalf("turn end: active, %d turns, %d snapshots", g.Turns, snaps)
	}
	if len(changes) != 1 || changes[0] != goal.BudgetLimited {
		t.Fatalf("changes %v", changes)
	}
}

func TestGoalDriverStops(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it", 0)
	d := GoalDriver{Session: "s", Goal: g}
	d.BeginTurn()
	if d.EndTurn(context.Canceled) || g.Status != goal.Paused || g.Note != "interrupted" {
		t.Fatalf("an interrupt pauses: %s %q", g.Status, g.Note)
	}
	g.Status = goal.Active
	d.Set(g)
	d.BeginTurn()
	d.EndTurn(errors.New("boom"))
	if g.Status != goal.Blocked || !strings.Contains(g.Note, "boom") {
		t.Fatalf("failures block: %s %q", g.Status, g.Note)
	}

	// The model's report comes from the file; nothing else in it counts.
	g.Status, g.Note, g.FailStreak = goal.Active, "", 0
	d.Set(g)
	file, _ := goal.Load("s")
	file.Status, file.Note, file.Objective = goal.Complete, "tests pass", "something else"
	_ = goal.Save("s", file)
	if _, ok := d.Next(); ok || g.Status != goal.Complete || g.Note != "tests pass" || g.Objective != "ship it" {
		t.Fatalf("adopt: %s %q %q", g.Status, g.Note, g.Objective)
	}

	// Once it is complete, a goal the model sets (atto goal set) is taken
	// as a new goal and continued.
	var adopted *goal.Goal
	d.Adopted = func(g *goal.Goal) { adopted = g }
	ng, _ := goal.New("next thing", 0)
	ng.Created = g.Created.Add(time.Second)
	_ = goal.Save("s", ng)
	if text, ok := d.Next(); !ok || adopted == nil || d.Goal.Objective != "next thing" || !strings.Contains(text, "next thing") {
		t.Fatalf("new goal: %v %+v %+v", ok, adopted, d.Goal)
	}
}

func TestGoalDriverRun(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	var inputs []string
	turn := func(_ context.Context, input string, emit func(any)) error {
		inputs = append(inputs, input)
		emit(agent.ToolStart{})
		emit(step(10, 0, 1))
		if len(inputs) == 3 { // the model reports from its shell
			f, _ := goal.Load("s")
			f.Status, f.Note = goal.Complete, "done"
			_ = goal.Save("s", f)
		}
		return nil
	}
	seen, between := 0, 0
	emit := func(any) { seen++ }

	// Without a goal: one turn.
	d := GoalDriver{Session: "s"}
	if err := d.Run(context.Background(), "hi", turn, emit, nil); err != nil || len(inputs) != 1 {
		t.Fatalf("no goal: %v %v", err, inputs)
	}

	inputs = nil
	g, _ := goal.New("ship it", 0)
	d.Goal = g
	_ = goal.Save("s", g)
	if err := d.Run(context.Background(), "start", turn, emit, func() { between++ }); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 3 || inputs[0] != "start" || !goal.IsMessage(inputs[1]) || between != 2 {
		t.Fatalf("inputs %q, between %d", inputs, between)
	}
	if g.Status != goal.Complete || g.Turns != 3 || g.TokensUsed != 33 || seen != 8 {
		t.Fatalf("goal %+v, %d events", g, seen)
	}
}

func TestGoalDriverElapsedCountsTheRunningTurn(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it", 0)
	g.Seconds = 60
	d := GoalDriver{Session: "s", Goal: g}
	if d.Elapsed() != 60 {
		t.Fatalf("idle: %d", d.Elapsed())
	}
	d.BeginTurn()
	d.turnStart = d.turnStart.Add(-120 * time.Second) // two minutes into the turn
	if n := d.Elapsed(); n < 180 || n > 182 {
		t.Fatalf("running: %d", n)
	}
	g.Status = goal.Paused
	if d.Elapsed() != 60 {
		t.Fatalf("only an active goal counts the turn: %d", d.Elapsed())
	}
	g.Status = goal.Active
	d.EndTurn(nil)
	if n := d.Elapsed(); n < 180 || n > 182 { // the turn is now in Seconds, once
		t.Fatalf("after the turn: %d", n)
	}
}

func TestGoalDriverModelPause(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it", 0)
	d := GoalDriver{Session: "s", Goal: g}
	d.Set(g)
	f, _ := goal.Load("s")
	f.Status, f.Note = goal.Paused, "the user asked"
	_ = goal.Save("s", f)
	if _, ok := d.Next(); ok || g.Status != goal.Paused || g.Note != "the user asked" {
		t.Fatalf("the model's pause is taken: %+v", g)
	}
}

// A turn that took user input puts the goal on hold when it ends; turns of
// continuations and events do not.
func TestGoalDriverHoldAfterUserInput(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it", 0)
	d := GoalDriver{Session: "s", Goal: g}
	turn := func(user bool) {
		d.BeginTurn()
		if user {
			d.UserInput()
		}
		d.Event(agent.ToolStart{}) // progress, so the stall audit stays out of it
		d.EndTurn(nil)
	}

	// Continuations and events: no hold.
	turn(false)
	if _, ok := d.Next(); !ok || d.Held() {
		t.Fatal("a goal turn without user input continues")
	}

	// A user's message or steer: on hold, still active.
	turn(true)
	if _, ok := d.Next(); ok || !d.Held() || !d.Active() {
		t.Fatalf("held=%v active=%v: want held and active, no continuation", d.Held(), d.Active())
	}

	// An event turn while held does not release it.
	turn(false)
	if _, ok := d.Next(); ok || !d.Held() {
		t.Fatal("an event turn keeps the hold")
	}

	// Release continues; the next turn without user input holds nothing.
	d.Release()
	if _, ok := d.Next(); !ok || d.Held() {
		t.Fatal("release continues the goal")
	}
	turn(false)
	if d.Held() {
		t.Fatal("a continuation turn does not hold")
	}

	// Input noted in a turn that is then released (a new goal, an edit) does
	// not hold.
	d.BeginTurn()
	d.UserInput()
	d.Release()
	d.EndTurn(nil)
	if d.Held() {
		t.Fatal("release drops the turn's user input")
	}

	// A hold only matters while active.
	turn(true)
	g.Status = goal.Paused
	if d.Held() {
		t.Fatal("a paused goal is not waiting")
	}
	g.Status = goal.Active
	if !d.Held() {
		t.Fatal("hold survives pausing until released")
	}
	d.Release()
}

// A goal the model sets itself during the user's turn starts at once.
func TestGoalDriverAdoptedGoalIsNotHeld(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	d := GoalDriver{Session: "s"}
	d.BeginTurn()
	d.UserInput()
	g, _ := goal.New("from the model", 0)
	_ = goal.Save("s", g)
	d.EndTurn(nil)
	if d.Goal == nil || d.Held() {
		t.Fatalf("goal %v held %v", d.Goal, d.Held())
	}
}

func TestGoalDriverRestoreDropsHold(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	d := GoalDriver{Session: "s"}
	d.UserInput()
	d.Restore(nil)
	d.BeginTurn()
	d.EndTurn(nil)
	if d.Held() {
		t.Fatal("restoring a session starts clean")
	}
}
