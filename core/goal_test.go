package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func step(in, cached, out int) agent.StepEnd {
	return agent.StepEnd{Usage: provider.Usage{PromptTokens: in, CachedTokens: cached, CompletionTokens: out}}
}

func TestGoalDriverAccounts(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it")
	var steers []string
	changes := 0
	snaps := 0
	d := GoalDriver{Session: "s", Goal: g,
		Steer:    func(s string) { steers = append(steers, s) },
		Changed:  func(*goal.Goal) { changes++ },
		Snapshot: func(*goal.Goal) { snaps++ },
	}
	d.BeginTurn()
	d.Event(agent.ToolStart{})
	d.Event(step(60, 20, 10)) // 50 new tokens
	d.Event(step(80, 60, 40)) // 60 more
	if len(steers) != 0 || g.TokensUsed != 110 || g.Status != goal.Active {
		t.Fatalf("tokens are information only: %v %s %d", steers, g.Status, g.TokensUsed)
	}
	if !d.EndTurn(nil) || g.Turns != 1 || snaps != 1 || changes != 0 {
		t.Fatalf("turn end: %d turns, %d snapshots, %d changes", g.Turns, snaps, changes)
	}
}

// A session saved when goals had token budgets: a goal whose budget ran out
// comes back paused, so the user can resume it.
func TestGoalDriverRestoresLegacyBudgetLimitedGoal(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	raw := `{"objective":"ship it","status":"budget_limited","budget":5000,"tokensUsed":5100,"seconds":42,"note":"token budget of 5K used","turns":3}`
	d := GoalDriver{Session: "s"}
	if d.Restore([]session.Entry{{Type: session.TypeGoal, Goal: json.RawMessage(raw)}}) {
		t.Fatal("not an active goal: nothing to wake")
	}
	g := d.Goal
	if g == nil || g.Status != goal.Paused || g.TokensUsed != 5100 || g.Seconds != 42 || g.Turns != 3 || d.Active() {
		t.Fatalf("%+v", g)
	}
	if f, err := goal.Load("s"); err != nil || f.Status != goal.Paused {
		t.Fatalf("the goal file: %+v %v", f, err)
	}
}

func TestGoalDriverStops(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it")
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
	ng, _ := goal.New("next thing")
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
	g, _ := goal.New("ship it")
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
	g, _ := goal.New("ship it")
	g.Seconds = 60
	d := GoalDriver{Session: "s", Goal: g}
	if d.Elapsed() != 60 {
		t.Fatalf("idle: %d", d.Elapsed())
	}
	d.BeginTurn()
	d.lastFold = d.lastFold.Add(-120 * time.Second) // two minutes into the turn
	if n := d.Elapsed(); n < 180 || n > 182 {
		t.Fatalf("running: %d", n)
	}
	d.EndTurn(nil)
	if n := d.Elapsed(); n < 180 || n > 182 { // the turn is now in Seconds, once
		t.Fatalf("after the turn: %d", n)
	}
}

func TestGoalDriverModelPause(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it")
	d := GoalDriver{Session: "s", Goal: g}
	d.Set(g)
	f, _ := goal.Load("s")
	f.Status, f.Note = goal.Paused, "the user asked"
	_ = goal.Save("s", f)
	if _, ok := d.Next(); ok || g.Status != goal.Paused || g.Note != "the user asked" {
		t.Fatalf("the model's pause is taken: %+v", g)
	}
}

// The user's message asking to resume is user input, but the model's resume
// releases the hold: the goal goes on after the turn.
func TestGoalDriverModelResumeIsNotHeld(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it")
	g.Status, g.Note, g.IdleStreak = goal.Paused, "paused by the user", 2
	d := GoalDriver{Session: "s", Goal: g}
	d.Set(g)
	var adopted *goal.Goal
	d.Adopted = func(g *goal.Goal) { adopted = g }
	changed := 0
	d.Changed = func(*goal.Goal) { changed++ }

	d.BeginTurn()
	d.UserInput() // "please resume the goal"
	f, _ := goal.Load("s")
	f.Status, f.Note, f.IdleStreak = goal.Active, "", 0
	_ = goal.Save("s", f)
	d.Event(step(1, 0, 1)) // the next model call notices it
	if !d.Active() || g.Note != "" || g.IdleStreak != 0 || adopted != g || changed != 0 {
		t.Fatalf("the model's resume is taken: %+v %v %d", g, adopted, changed)
	}
	if !d.EndTurn(nil) || d.Held() {
		t.Fatal("the goal is not held after the turn that resumed it")
	}
	if text, ok := d.Next(); !ok || !goal.IsMessage(text) {
		t.Fatalf("the goal continues: %q %v", text, ok)
	}

	// Without a resume the same turn holds.
	d.BeginTurn()
	d.UserInput()
	d.EndTurn(nil)
	if !d.Held() {
		t.Fatal("a turn with user input holds")
	}
}

// A turn that took user input puts the goal on hold when it ends; turns of
// continuations and events do not.
func TestGoalDriverHoldAfterUserInput(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it")
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
	g, _ := goal.New("from the model")
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

// The time shown by the indicator, /goal and the completion notice is one
// number: the turn's time up to the report is in Seconds when the goal is
// announced, and the rest of the turn does not add to a finished goal.
func TestGoalDriverTimeAgreesAtCompletion(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it")
	var announced int64 = -1
	d := GoalDriver{Session: "s", Goal: g, Changed: func(g *goal.Goal) { announced = g.Seconds }}
	d.BeginTurn()
	d.lastFold = d.lastFold.Add(-20 * time.Second)
	d.Event(agent.ToolStart{})
	done := *g
	done.Status, done.Note = goal.Complete, "done"
	done.Updated = time.Now().Add(time.Second)
	if err := goal.Save("s", &done); err != nil {
		t.Fatal(err)
	}
	d.Poll()
	if g.Status != goal.Complete || announced < 20 || announced > 21 || d.Elapsed() != g.Seconds {
		t.Fatalf("at the report: status %s, announced %d, seconds %d, elapsed %d", g.Status, announced, g.Seconds, d.Elapsed())
	}
	d.lastFold = d.lastFold.Add(-10 * time.Second) // more time passes in the turn's summary
	if d.Elapsed() != announced {
		t.Fatalf("a finished goal stops counting: %d, announced %d", d.Elapsed(), announced)
	}
	d.EndTurn(nil)
	if g.Seconds != announced {
		t.Fatalf("after the turn: %d, announced %d", g.Seconds, announced)
	}
}

// A user turn that begins with a paused goal is not part of the goal's time
// or tokens.
func TestGoalDriverUnrelatedTurnIsNotCounted(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it")
	g.Status = goal.Paused
	d := GoalDriver{Session: "s", Goal: g}
	d.BeginTurn()
	d.lastFold = d.lastFold.Add(-30 * time.Second)
	d.Event(step(100, 0, 10))
	d.EndTurn(nil)
	if g.Seconds != 0 || g.TokensUsed != 0 || d.Elapsed() != 0 {
		t.Fatalf("%+v", g)
	}
}

// The turn the goal was part of is accounted to its end, even after the
// model completed the goal: its closing summary is the goal's too.
func TestGoalDriverAccountsTheRestOfTheTurn(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it")
	d := GoalDriver{Session: "s", Goal: g}
	d.BeginTurn()
	d.Event(step(100, 0, 0))
	done := *g
	done.Status, done.Note = goal.Complete, "done"
	done.Updated = time.Now().Add(time.Second)
	_ = goal.Save("s", &done)
	d.Event(step(10, 0, 5)) // the summary, after the report
	if g.Status != goal.Complete || g.TokensUsed != 115 {
		t.Fatalf("%s %d", g.Status, g.TokensUsed)
	}
}

func TestGoalDriverTellsTheRunningTurn(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it")
	var steers []string
	stops := 0
	d := GoalDriver{Session: "s", Goal: g, Steer: func(s string) { steers = append(steers, s) }, Stop: func() { stops++ }}
	if d.Tell(goal.ClearedMessage()) { // no turn: nothing to tell
		t.Fatal("told an idle driver")
	}
	if len(steers) != 0 || stops != 0 {
		t.Fatalf("idle: %v, %d stops", steers, stops)
	}
	d.BeginTurn()
	d.Set(nil)
	if !d.Tell(goal.ClearedMessage()) {
		t.Fatal("not told")
	}
	g.Status = goal.Paused
	d.Tell(g.PausedMessage())
	if stops != 2 {
		t.Fatalf("each note asks the turn to stop: %d", stops)
	}
	if len(steers) != 2 || !strings.Contains(steers[0], "cleared the goal") || !strings.Contains(steers[1], "paused the goal") {
		t.Fatalf("%q", steers)
	}
	d.EndTurn(nil)
	d.Tell(goal.ClearedMessage())
	if len(steers) != 2 || stops != 2 {
		t.Fatalf("after the turn: %v, %d stops", steers, stops)
	}
}

func TestGoalDriverStateNote(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	d := GoalDriver{Session: "s"}
	if d.StateNote() != "" {
		t.Fatal("no goal, no note")
	}
	g, _ := goal.New("ship it")
	d.Set(g)
	if d.StateNote() != "" {
		t.Fatal("a running goal needs no note")
	}
	d.BeginTurn()
	d.UserInput()
	d.Event(agent.ToolStart{})
	d.EndTurn(nil) // held after the user's turn
	if n := d.StateNote(); !strings.Contains(n, "waiting for the user") {
		t.Fatalf("held: %q", n)
	}
	d.Release()
	d.BeginTurn()
	d.EndTurn(context.Canceled) // an interrupt pauses it
	if n := d.StateNote(); g.Status != goal.Paused || !strings.Contains(n, "because the user interrupted it") {
		t.Fatalf("interrupted: %s %q", g.Status, n)
	}
	g.Status = goal.Complete
	if d.StateNote() != "" {
		t.Fatal("a finished goal needs no note")
	}
}

// An interrupt made to send the user's message at once (Replace) leaves the
// goal active; the message's turn then holds it. A plain interrupt pauses.
func TestGoalDriverReplace(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	g, _ := goal.New("ship it")
	d := GoalDriver{Session: "s", Goal: g}
	d.Set(g)

	d.BeginTurn() // a continuation, cut off before any tool ran
	d.Replace()
	if !d.EndTurn(context.Canceled) || g.Status != goal.Active || g.Note != "" {
		t.Fatalf("a replaced turn leaves the goal active: %+v", g)
	}
	if d.Held() || g.IdleStreak != 0 || g.Turns != 1 {
		t.Fatalf("held %v, idle streak %d, turns %d", d.Held(), g.IdleStreak, g.Turns)
	}

	d.BeginTurn() // the user's message
	d.UserInput()
	d.EndTurn(nil)
	if !d.Held() || !d.Active() {
		t.Fatal("the message's turn holds the goal")
	}
	d.Release()

	// Replace is for one turn only, and only for an interrupt.
	d.BeginTurn()
	d.Replace()
	d.EndTurn(nil)
	d.BeginTurn()
	d.EndTurn(context.Canceled)
	if g.Status != goal.Paused || g.Note != goal.NoteInterrupted {
		t.Fatalf("a plain interrupt still pauses: %+v", g)
	}
}
