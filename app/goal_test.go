package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// goalApp is an idle app that never starts a goal turn by itself (the queue
// is paused), so the commands' effects can be read off the goal and the
// transcript.
func goalApp(t *testing.T) *App {
	a := treeApp(t)
	a.queuePaused = true
	return a
}

// goalText is everything shown so far, notices included.
func goalText(a *App) string {
	var out []string
	for _, l := range a.ui.Body.Render(80) {
		out = append(out, strings.TrimRight(tui.StripEscapes(l), " "))
	}
	return strings.Join(out, "\n")
}

func keys(a *App, ks ...string) {
	raw := map[string]string{"escape": "\x1b", "enter": "\r", "down": "\x1b[B", "up": "\x1b[A", "backspace": "\x7f"}
	for _, k := range ks {
		a.modal.HandleInput(raw[k])
	}
}

func TestGoalBareShowsUsage(t *testing.T) {
	a := goalApp(t)
	a.cmdGoal("")
	got := goalText(a)
	if !strings.Contains(got, "Usage: /goal [<objective>|clear|edit|pause|resume]") || !strings.Contains(got, "No goal is currently set.") {
		t.Fatalf("no goal:\n%s", got)
	}
}

func TestGoalSetPauseResumeClear(t *testing.T) {
	a := goalApp(t)
	a.cmdGoal("ship the thing")
	g := a.goal.Goal
	if g == nil || g.Status != goal.Active || g.Objective != "ship the thing" || g.Budget != 0 {
		t.Fatalf("set: %+v", g)
	}
	if got := goalText(a); !strings.Contains(got, "• Goal active") || !strings.Contains(got, "Objective: ship the thing") {
		t.Fatalf("set announces the goal:\n%s", got)
	}

	a.cmdGoal("pause")
	if g.Status != goal.Paused || !strings.Contains(goalText(a), "• Goal paused") {
		t.Fatalf("pause: %+v\n%s", g, goalText(a))
	}
	a.cmdGoal("resume")
	if g.Status != goal.Active || strings.Count(goalText(a), "• Goal active") != 2 {
		t.Fatalf("resume: %+v\n%s", g, goalText(a))
	}

	// A stalled goal resumes with a fresh stall audit.
	g.Status, g.Note, g.FailStreak, g.IdleStreak = goal.Blocked, "stuck", 1, 2
	a.cmdGoal("resume")
	if g.Status != goal.Active || g.Note != "" || g.FailStreak != 0 || g.IdleStreak != 0 {
		t.Fatalf("resume from stalled: %+v", g)
	}
	// So does a usage limited one.
	g.Status = goal.UsageLimited
	a.cmdGoal("RESUME")
	if g.Status != goal.Active {
		t.Fatalf("resume from usage limited: %+v", g)
	}

	// A goal that hit its budget or is complete does not resume.
	for _, st := range []goal.Status{goal.BudgetLimited, goal.Complete} {
		g.Status = st
		a.cmdGoal("resume")
		if g.Status != st {
			t.Fatalf("%s must stay: %+v", st, g)
		}
	}

	a.cmdGoal("clear")
	if a.goal.Goal != nil || !strings.Contains(goalText(a), "• Goal cleared") {
		t.Fatalf("clear: %+v", a.goal.Goal)
	}
	if f, _ := goal.Load(a.sess.ID); f != nil {
		t.Fatal("the goal file is removed")
	}
	a.cmdGoal("clear")
	if got := goalText(a); !strings.Contains(got, "No goal to clear") || !strings.Contains(got, "does not currently have a goal") {
		t.Fatalf("clear without a goal:\n%s", got)
	}
	for _, sub := range []string{"pause", "resume", "edit"} {
		a.cmdGoal(sub)
	}
	if strings.Count(goalText(a), "No goal is currently set.") != 3 {
		t.Fatalf("pause, resume and edit need a goal:\n%s", goalText(a))
	}
}

func TestGoalPauseOnlyWhatCanPause(t *testing.T) {
	a := goalApp(t)
	a.cmdGoal("x")
	g := a.goal.Goal
	g.Status = goal.BudgetLimited
	a.cmdGoal("pause")
	if g.Status != goal.BudgetLimited || !strings.Contains(goalText(a), "Goal limited by budget") {
		t.Fatalf("a budget limit takes precedence over a pause: %+v", g)
	}
	g.Status = goal.Blocked
	a.cmdGoal("pause")
	if g.Status != goal.Paused {
		t.Fatalf("%+v", g)
	}
}

func TestGoalReplaceNeedsConfirmation(t *testing.T) {
	a := goalApp(t)
	a.cmdGoal("first")
	a.cmdGoal("second")
	if a.modal == nil || a.goal.Goal.Objective != "first" {
		t.Fatal("an unfinished goal is not replaced without asking")
	}
	menu := tui.StripEscapes(strings.Join(a.modal.Render(80), "\n"))
	for _, want := range []string{"Replace goal?", "New objective: second", "Replace current goal", "Set the new objective and start it now", "Cancel", "Keep the current goal"} {
		if !strings.Contains(menu, want) {
			t.Fatalf("menu lacks %q:\n%s", want, menu)
		}
	}
	keys(a, "escape")
	if a.modal != nil || a.goal.Goal.Objective != "first" {
		t.Fatal("esc keeps the goal")
	}
	a.cmdGoal("second")
	keys(a, "down", "enter") // Cancel
	if a.modal != nil || a.goal.Goal.Objective != "first" {
		t.Fatal("cancel keeps the goal")
	}
	a.cmdGoal("second")
	keys(a, "enter") // Replace current goal
	if a.modal != nil || a.goal.Goal.Objective != "second" || a.goal.Goal.Status != goal.Active {
		t.Fatalf("replace: %+v", a.goal.Goal)
	}

	// A finished goal is replaced without asking.
	a.goal.Goal.Status = goal.Complete
	a.cmdGoal("third")
	if a.modal != nil || a.goal.Goal.Objective != "third" {
		t.Fatal("a complete goal needs no confirmation")
	}
}

func TestGoalSummaryPerStatus(t *testing.T) {
	g := &goal.Goal{Objective: "ship it", TokensUsed: 63876, Seconds: 120}
	hints := map[goal.Status]string{
		goal.Active:        "Commands: /goal edit, /goal pause, /goal clear",
		goal.Paused:        "Commands: /goal edit, /goal resume, /goal clear",
		goal.Blocked:       "Commands: /goal edit, /goal resume, /goal clear",
		goal.UsageLimited:  "Commands: /goal edit, /goal resume, /goal clear",
		goal.BudgetLimited: "Commands: /goal edit, /goal clear",
		goal.Complete:      "Commands: /goal edit, /goal clear",
	}
	labels := map[goal.Status]string{
		goal.Active: "active", goal.Paused: "paused", goal.Blocked: "stalled",
		goal.UsageLimited: "usage limited", goal.BudgetLimited: "limited by budget", goal.Complete: "complete",
	}
	for st, hint := range hints {
		g.Status = st
		got := tui.StripEscapes(strings.Join(goalSummaryLines(g, false), "\n"))
		want := "Goal\nStatus: " + labels[st] + "\nObjective: ship it\nTime used: 2m\nTokens used: 63.9K\n\n" + hint
		if got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", st, got, want)
		}
	}
	g.Budget = 50000
	if got := tui.StripEscapes(strings.Join(goalSummaryLines(g, false), "\n")); !strings.Contains(got, "Tokens used: 63.9K\nToken budget: 50K\n") {
		t.Fatalf("budget line:\n%s", got)
	}

	a := goalApp(t)
	a.cmdGoal("ship it")
	a.cmdGoal("")
	if got := goalText(a); !strings.Contains(got, "Status: active") || !strings.Contains(got, "Commands: /goal edit, /goal pause, /goal clear") {
		t.Fatalf("bare /goal shows the summary:\n%s", got)
	}
}

func TestGoalEditPrompt(t *testing.T) {
	a := goalApp(t)
	a.cmdGoal("old objective")
	g := a.goal.Goal
	g.Budget, g.TokensUsed, g.Status = 50000, 1200, goal.Paused

	a.cmdGoal("edit")
	if a.modal == nil {
		t.Fatal("edit opens a prompt")
	}
	menu := tui.StripEscapes(strings.Join(a.modal.Render(80), "\n"))
	if !strings.Contains(menu, "Edit goal") || !strings.Contains(menu, "› old objective") || !strings.Contains(menu, "Type a goal objective and press Enter") {
		t.Fatalf("prompt:\n%s", menu)
	}
	keys(a, "escape")
	if a.modal != nil || g.Objective != "old objective" {
		t.Fatal("esc leaves the goal alone")
	}

	a.cmdGoal("edit")
	keys(a, "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
	a.modal.HandleInput("new one")
	keys(a, "enter")
	if a.modal != nil || g.Objective != "old new one" {
		t.Fatalf("edited: %q", g.Objective)
	}
	if g.Status != goal.Paused || g.Budget != 50000 || g.TokensUsed != 1200 {
		t.Fatalf("a paused goal stays paused and keeps its budget and usage: %+v", g)
	}
	if got := goalText(a); !strings.Contains(got, "• Goal paused") || !strings.Contains(got, "Objective: old new one") {
		t.Fatalf("announces the edit:\n%s", got)
	}

	// Finished and budget limited goals become active again.
	for _, st := range []goal.Status{goal.Complete, goal.BudgetLimited} {
		g.Status = st
		a.cmdGoal("edit")
		a.modal.HandleInput("!")
		keys(a, "enter")
		if g.Status != goal.Active {
			t.Fatalf("%s edit -> %s", st, g.Status)
		}
	}
	g.Status = goal.Blocked
	a.cmdGoal("edit")
	a.modal.HandleInput("!")
	keys(a, "enter")
	if g.Status != goal.Blocked {
		t.Fatalf("a stalled goal stays stalled: %s", g.Status)
	}
}

func TestGoalEditSteersRunningTurn(t *testing.T) {
	a := goalApp(t)
	a.cmdGoal("old")
	a.busy, a.runKind = true, "turn"
	a.setObjective("new")
	if a.goal.Goal.Objective != "new" || a.goal.Goal.Status != goal.Active {
		t.Fatalf("%+v", a.goal.Goal)
	}
	steers := a.agent.DrainSteers()
	if len(steers) != 1 || !strings.Contains(steers[0], "edited by the user") || !strings.Contains(steers[0], "<untrusted_objective>\nnew\n</untrusted_objective>") {
		t.Fatalf("the running turn is told: %q", steers)
	}
}

func TestGoalIndicatorPlacement(t *testing.T) {
	a := statusApp(t, nil)
	row := func(width int) []string {
		var out []string
		for _, l := range a.renderStatus(width) {
			out = append(out, tui.StripEscapes(l))
		}
		return out
	}
	if r := row(120); len(r) != 1 || strings.Contains(r[0], "goal") {
		t.Fatalf("no goal, no indicator: %q", r)
	}

	cases := []struct {
		g    goal.Goal
		want string
	}{
		{goal.Goal{Status: goal.Active, Seconds: 90}, "Pursuing goal (1m)"},
		{goal.Goal{Status: goal.Active, Budget: 50000, TokensUsed: 12500}, "Pursuing goal (12.5K / 50K)"},
		{goal.Goal{Status: goal.Paused}, "Goal paused (/goal resume)"},
		{goal.Goal{Status: goal.Blocked}, "Goal stalled (/goal resume)"},
		{goal.Goal{Status: goal.UsageLimited}, "Goal hit usage limits (/goal resume)"},
		{goal.Goal{Status: goal.BudgetLimited, Budget: 50000, TokensUsed: 63876}, "Goal unmet (63.9K / 50K tokens)"},
		{goal.Goal{Status: goal.BudgetLimited}, "Goal abandoned"},
		{goal.Goal{Status: goal.Complete, Budget: 50000, TokensUsed: 40000}, "Goal achieved (40K tokens)"},
		{goal.Goal{Status: goal.Complete, Seconds: 36720}, "Goal achieved (10h 12m)"},
	}
	for _, c := range cases {
		g := c.g
		g.Objective = "x"
		a.goal.Goal = &g
		// The status takes two rows beside the indicator; it ends the first.
		r := row(110)
		if len(r) != 2 || !strings.HasSuffix(r[0], c.want) || tui.VisibleWidth(r[0]) != 109 || strings.Contains(r[1], "oal") {
			t.Errorf("%s: %q", c.want, r)
			continue
		}
		if !strings.Contains(r[0], "Orca") {
			t.Errorf("the status row stays on the same line: %q", r[0])
		}
		if styled := a.renderStatus(120)[0]; !strings.HasSuffix(styled, tui.FG(5, c.want)) {
			t.Errorf("the indicator is magenta: %q", styled)
		}
	}

	// A narrow terminal gives the indicator a row of its own.
	a.goal.Goal = &goal.Goal{Objective: "x", Status: goal.Paused}
	r := row(40)
	if len(r) != 3 || !strings.Contains(r[2], "Goal paused (/goal resume)") || strings.Contains(r[0]+r[1], "oal") {
		t.Fatalf("narrow: %q", r)
	}
	for _, l := range r {
		if tui.VisibleWidth(l) > 40 {
			t.Fatalf("too wide: %q", l)
		}
	}

	// Jobs and timers keep their own row.
	a.jobCount = 2
	if r := row(120); len(r) != 3 || !strings.Contains(r[2], "2 jobs running") || !strings.HasSuffix(r[0], "Goal paused (/goal resume)") {
		t.Fatalf("jobs: %q", r)
	}

	// A custom status line gives up the room as well.
	a.jobCount, a.statusCmd, a.statusLines = 0, true, []string{"custom status"}
	if r := row(80); len(r) != 1 || !strings.HasPrefix(r[0], " custom status") || !strings.HasSuffix(r[0], "Goal paused (/goal resume)") {
		t.Fatalf("custom: %q", r)
	}
}

func TestGoalInterruptPauses(t *testing.T) {
	a := goalApp(t)
	a.cmdGoal("ship it")
	a.busy, a.runKind = true, "turn"
	a.goal.BeginTurn()
	a.busy = false
	a.afterRun(context.Canceled)
	g := a.goal.Goal
	if g.Status != goal.Paused || g.Note != "interrupted" {
		t.Fatalf("an interrupted goal turn pauses: %+v", g)
	}
	if got := goalText(a); !strings.Contains(got, "• Goal paused") {
		t.Fatalf("announced:\n%s", got)
	}
	if a.goal.Active() {
		t.Fatal("no new goal turn starts")
	}
}

func TestGoalModelReportsAreAnnounced(t *testing.T) {
	a := goalApp(t)
	a.cmdGoal("ship it")
	f, _ := goal.Load(a.sess.ID)
	f.Status, f.Note = goal.Blocked, "needs a credential"
	_ = goal.Save(a.sess.ID, f)
	a.goal.Poll()
	got := goalText(a)
	if a.goal.Goal.Status != goal.Blocked || !strings.Contains(got, "• Goal stalled") || !strings.Contains(got, "Note: needs a credential") {
		t.Fatalf("%+v\n%s", a.goal.Goal, got)
	}
}

func TestResumePausedGoalPrompt(t *testing.T) {
	snap := func(g *goal.Goal) []session.Entry {
		raw, _ := json.Marshal(g)
		return []session.Entry{{Type: session.TypeGoal, Goal: raw}}
	}
	for _, st := range []goal.Status{goal.Paused, goal.Blocked, goal.UsageLimited, goal.Active} {
		a := goalApp(t)
		a.restoreGoal(snap(&goal.Goal{Objective: "ship it", Status: st, Budget: 100}))
		if a.modal == nil {
			t.Fatalf("%s: a goal that is not running asks to resume", st)
		}
		menu := tui.StripEscapes(strings.Join(a.modal.Render(80), "\n"))
		for _, want := range []string{"Resume paused goal?", "Goal: ship it", "Resume goal", "Mark it active and continue when idle", "Leave paused", "Keep it paused; use /goal resume later"} {
			if !strings.Contains(menu, want) {
				t.Fatalf("%s: menu lacks %q:\n%s", st, want, menu)
			}
		}
		if a.goal.Goal.Status == goal.Active {
			t.Fatalf("%s: an active goal comes back paused", st)
		}
	}

	a := goalApp(t)
	a.restoreGoal(snap(&goal.Goal{Objective: "ship it", Status: goal.Paused}))
	keys(a, "down", "enter") // Leave paused
	if a.modal != nil || a.goal.Goal.Status != goal.Paused {
		t.Fatalf("leave paused: %+v", a.goal.Goal)
	}
	a.restoreGoal(snap(&goal.Goal{Objective: "ship it", Status: goal.Paused}))
	keys(a, "escape")
	if a.modal != nil || a.goal.Goal.Status != goal.Paused {
		t.Fatalf("esc leaves it paused: %+v", a.goal.Goal)
	}
	a.restoreGoal(snap(&goal.Goal{Objective: "ship it", Status: goal.Blocked, Note: "stuck"}))
	keys(a, "enter") // Resume goal
	if a.modal != nil || a.goal.Goal.Status != goal.Active || a.goal.Goal.Note != "" {
		t.Fatalf("resume: %+v", a.goal.Goal)
	}

	// Finished and budget limited goals, and sessions without a goal, don't ask.
	for _, g := range []*goal.Goal{{Objective: "x", Status: goal.Complete}, {Objective: "x", Status: goal.BudgetLimited}} {
		a := goalApp(t)
		a.restoreGoal(snap(g))
		if a.modal != nil || a.goal.Goal == nil {
			t.Fatalf("%s: no prompt", g.Status)
		}
	}
	b := goalApp(t)
	b.restoreGoal(nil)
	if b.modal != nil || b.goal.Goal != nil {
		t.Fatal("no goal, no prompt")
	}
}

func TestOldGoalSnapshotsStillLoad(t *testing.T) {
	// A snapshot as older atto wrote it: no usage_limited, no pause note.
	raw := `{"objective":"old goal","status":"blocked","budget":1000,"tokensUsed":500,"seconds":61,"note":"stuck","turns":2,"failStreak":1,"created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z"}`
	a := goalApp(t)
	a.restoreGoal([]session.Entry{{Type: session.TypeGoal, Goal: json.RawMessage(raw)}})
	g := a.goal.Goal
	if g == nil || g.Status != goal.Blocked || g.Objective != "old goal" || g.Budget != 1000 || g.TokensUsed != 500 || g.Seconds != 61 || g.Turns != 2 {
		t.Fatalf("%+v", g)
	}
	if a.modal == nil {
		t.Fatal("an old stalled goal asks to resume too")
	}
	// The JSON of the goal keeps its keys, and the old status names.
	out, _ := json.Marshal(g)
	for _, key := range []string{`"objective"`, `"status":"blocked"`, `"budget":1000`, `"tokensUsed":500`, `"seconds":61`} {
		if !strings.Contains(string(out), key) {
			t.Errorf("format changed, lost %s: %s", key, out)
		}
	}
}

func TestUsageLimitedTurnStopsTheGoal(t *testing.T) {
	a := goalApp(t)
	a.cmdGoal("ship it")
	a.busy, a.runKind = true, "turn"
	a.goal.BeginTurn()
	a.busy = false
	a.afterRun(errors.New("429: You have hit your ChatGPT usage limit (plus plan). Try again in ~30 min."))
	g := a.goal.Goal
	if g.Status != goal.UsageLimited {
		t.Fatalf("%+v", g)
	}
	if got := goalText(a); !strings.Contains(got, "• Goal usage limited") {
		t.Fatalf("announced:\n%s", got)
	}
	a.cmdGoal("resume")
	if g.Status != goal.Active {
		t.Fatalf("resume: %+v", g)
	}
}

// endTurn simulates a goal turn that ends: steers are the texts committed
// during it, userStart that the user's message started it.
func endTurn(a *App, userStart bool, steers ...string) {
	a.busy, a.runKind = true, "turn"
	a.goal.BeginTurn()
	if userStart {
		a.goal.UserInput()
	}
	if len(steers) > 0 {
		a.onEvent(agent.SteerCommitted{Texts: steers})
	}
	a.goal.Event(agent.ToolStart{})
	a.busy = false
	a.afterRun(nil)
}

func TestGoalHeldAfterUserInput(t *testing.T) {
	for _, c := range []struct {
		name      string
		userStart bool
		steers    []string
		held      bool
	}{
		{"continuation", false, nil, false},
		{"user message started the turn", true, nil, true},
		{"user steer", false, []string{"why did you do that?"}, true},
		{"event steer", false, []string{events.Prefix + "job done"}, false},
		{"goal message steer", false, []string{goal.OpenTag + "\nbudget\n" + goal.CloseTag}, false},
		{"legacy goal message steer", false, []string{"[atto goal] budget"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := goalApp(t)
			a.cmdGoal("ship it")
			endTurn(a, c.userStart, c.steers...)
			if got := a.goal.Held(); got != c.held {
				t.Fatalf("held = %v, want %v", got, c.held)
			}
			if !a.goal.Active() {
				t.Fatalf("the goal stays active: %+v", a.goal.Goal)
			}
			notice := strings.Contains(strings.Join(strings.Fields(goalText(a)), " "), goalWaitingNotice)
			if notice != c.held {
				t.Fatalf("notice = %v, want %v:\n%s", notice, c.held, goalText(a))
			}
			if c.held && !strings.Contains(a.goalIndicator(), "Goal waiting (enter to continue)") {
				t.Fatalf("indicator %q", a.goalIndicator())
			}
			if !c.held && !strings.Contains(a.goalIndicator(), "Pursuing goal") {
				t.Fatalf("indicator %q", a.goalIndicator())
			}
		})
	}
}

func TestGoalHoldReleasedByEnterResumeAndNewGoal(t *testing.T) {
	held := func() *App {
		a := goalApp(t)
		a.cmdGoal("ship it")
		endTurn(a, true)
		if !a.goal.Held() {
			t.Fatal("not held")
		}
		return a
	}

	a := held()
	a.submit("", nil) // enter on an empty prompt
	if a.goal.Held() {
		t.Fatal("empty enter releases the hold")
	}

	a = held()
	a.cmdGoal("resume")
	if a.goal.Held() || a.goal.Goal.Status != goal.Active {
		t.Fatal("/goal resume releases the hold")
	}

	a = held()
	a.cmdGoal("second") // confirm replacing
	keys(a, "enter")
	if a.goal.Held() || a.goal.Goal.Objective != "second" {
		t.Fatalf("a new goal releases the hold: %+v", a.goal.Goal)
	}

	a = held()
	a.setObjective("edited")
	if a.goal.Held() {
		t.Fatal("editing the objective releases the hold")
	}

	// Pausing keeps nothing waiting, and resuming later starts clean.
	a = held()
	a.cmdGoal("pause")
	a.cmdGoal("resume")
	if a.goal.Held() {
		t.Fatal("resume after pause releases the hold")
	}
}

// A goal held after the user's turn does not start another turn by itself,
// and enter on an empty prompt continues it, against a scripted model.
func TestGoalHoldEndToEnd(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	count := func() int { mu.Lock(); defer mu.Unlock(); return requests }
	idle := func(a *App) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var busy bool
			a.ui.Do(func() { busy = a.busy })
			if !busy {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("turn did not finish")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	a := treeApp(t)
	a.agent.SetModel(config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: srv.URL}, Model: config.Model{ID: "m", ContextWindow: 100000}})
	g, _ := goal.New("ship it", 0)
	a.ui.Do(func() { a.goal.Set(g) })

	a.ui.Do(func() { a.startTurn("what is going on?", nil) })
	idle(a)
	time.Sleep(100 * time.Millisecond) // a continuation would have started by now
	idle(a)
	var held bool
	a.ui.Do(func() { held = a.goal.Held() })
	if n := count(); n != 1 || !held {
		t.Fatalf("after the user's turn: %d requests, held %v; want 1 and held", n, held)
	}

	a.ui.Do(func() { a.submit("", nil) })
	deadline := time.Now().Add(10 * time.Second)
	for {
		var st goal.Status
		a.ui.Do(func() { st = a.goal.Goal.Status })
		if st != goal.Active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the goal did not continue")
		}
		time.Sleep(10 * time.Millisecond)
	}
	idle(a)
	a.ui.Do(func() { held = a.goal.Held() })
	// The user's turn and two idle continuations stall the goal; the continuations do not hold.
	if n := count(); n != 3 || held {
		t.Fatalf("after enter: %d requests, held %v; want 3, not held", n, held)
	}
}

func TestGoalMessageTitle(t *testing.T) {
	g, _ := goal.New("ship it", 0)
	for text, want := range map[string]string{
		g.Continuation():                                  "◎ Continuing goal",
		"[atto goal] <objective>\nx":                      "◎ Continuing goal",
		goal.OpenTag + "\nBudget used.\n" + goal.CloseTag: "◎ Budget used.",
		"[atto goal] Budget used.":                        "◎ Budget used.",
	} {
		if got := goalMessageTitle(text); got != want {
			t.Errorf("%.40q: %q, want %q", text, got, want)
		}
	}
}
