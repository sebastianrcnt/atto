package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// goalApp is an idle app that never starts a goal turn by itself (the queue
// is paused), so the commands' effects can be read off the goal and the
// transcript.
func goalApp(t *testing.T) *App {
	a := treeApp(t)
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
	settle(a)
	return a
}

// goalText is everything shown so far, notices included.
func goalText(a *App) string { return shown(a) }

func keys(a *App, ks ...string) {
	raw := map[string]string{"escape": "\x1b", "enter": "\r", "down": "\x1b[B", "up": "\x1b[A", "backspace": "\x7f"}
	for _, k := range ks {
		key(a, raw[k])
		settle(a)
	}
}

func TestGoalBareShowsUsage(t *testing.T) {
	a := goalApp(t)
	goalCommand(a, "")
	got := goalText(a)
	if !strings.Contains(got, "Usage: /goal [<objective>|clear|edit|pause|resume]") || !strings.Contains(got, "No goal is currently set.") {
		t.Fatalf("no goal:\n%s", got)
	}
}

func TestGoalSetPauseResumeClear(t *testing.T) {
	a := goalApp(t)
	goalCommand(a, "ship the thing")
	g := a.theGoal()
	if g == nil || g.Status != goal.Active || g.Objective != "ship the thing" {
		t.Fatalf("set: %+v", g)
	}
	if got := goalText(a); !strings.Contains(got, "• Goal active") || !strings.Contains(got, "Objective: ship the thing") {
		t.Fatalf("set announces the goal:\n%s", got)
	}

	goalCommand(a, "pause")
	if g.Status != goal.Paused || !strings.Contains(goalText(a), "• Goal paused") {
		t.Fatalf("pause: %+v\n%s", g, goalText(a))
	}
	goalCommand(a, "resume")
	if g.Status != goal.Active || strings.Count(goalText(a), "• Goal active") != 2 {
		t.Fatalf("resume: %+v\n%s", g, goalText(a))
	}

	// A stalled goal resumes with a fresh stall audit.
	g.Status, g.Note, g.FailStreak, g.IdleStreak = goal.Blocked, "stuck", 1, 2
	fixtureGoal(t, a, g)
	goalCommand(a, "resume")
	if g.Status != goal.Active || g.Note != "" || g.FailStreak != 0 || g.IdleStreak != 0 {
		t.Fatalf("resume from stalled: %+v", g)
	}
	// So does a usage limited one.
	g.Status = goal.UsageLimited
	fixtureGoal(t, a, g)
	goalCommand(a, "RESUME")
	if g.Status != goal.Active {
		t.Fatalf("resume from usage limited: %+v", g)
	}

	// A complete goal does not resume.
	g.Status = goal.Complete
	fixtureGoal(t, a, g)
	goalCommand(a, "resume")
	if g.Status != goal.Complete {
		t.Fatalf("complete must stay: %+v", g)
	}

	goalCommand(a, "clear")
	if a.theGoal() != nil || !strings.Contains(goalText(a), "• Goal cleared") {
		t.Fatalf("clear: %+v", a.theGoal())
	}
	if f, _ := goal.Load(a.threadID); f != nil {
		t.Fatal("the goal file is removed")
	}
	goalCommand(a, "clear")
	if got := goalText(a); !strings.Contains(got, "No goal to clear") || !strings.Contains(got, "does not currently have a goal") {
		t.Fatalf("clear without a goal:\n%s", got)
	}
	for _, sub := range []string{"pause", "resume", "edit"} {
		goalCommand(a, sub)
	}
	if strings.Count(goalText(a), "No goal is currently set.") != 3 {
		t.Fatalf("pause, resume and edit need a goal:\n%s", goalText(a))
	}
}

func TestGoalPauseOnlyWhatCanPause(t *testing.T) {
	a := goalApp(t)
	goalCommand(a, "x")
	g := a.theGoal()
	g.Status = goal.Complete
	fixtureGoal(t, a, g)
	goalCommand(a, "pause")
	if g.Status != goal.Complete || !strings.Contains(goalText(a), "Goal complete") {
		t.Fatalf("a complete goal does not pause: %+v", g)
	}
	g.Status = goal.Blocked
	fixtureGoal(t, a, g)
	goalCommand(a, "pause")
	if g.Status != goal.Paused {
		t.Fatalf("%+v", g)
	}
}

func TestGoalReplaceNeedsConfirmation(t *testing.T) {
	a := goalApp(t)
	goalCommand(a, "first")
	goalCommand(a, "second")
	if a.modal == nil || a.theGoal().Objective != "first" {
		t.Fatal("an unfinished goal is not replaced without asking")
	}
	menu := tui.StripEscapes(strings.Join(a.modal.Render(80), "\n"))
	for _, want := range []string{"Replace goal?", "New objective: second", "Replace current goal", "Set the new objective and start it now", "Cancel", "Keep the current goal"} {
		if !strings.Contains(menu, want) {
			t.Fatalf("menu lacks %q:\n%s", want, menu)
		}
	}
	keys(a, "escape")
	if a.modal != nil || a.theGoal().Objective != "first" {
		t.Fatal("esc keeps the goal")
	}
	goalCommand(a, "second")
	keys(a, "down", "enter") // Cancel
	if a.modal != nil || a.theGoal().Objective != "first" {
		t.Fatal("cancel keeps the goal")
	}
	goalCommand(a, "second")
	keys(a, "enter") // Replace current goal
	if a.modal != nil || a.theGoal().Objective != "second" || a.theGoal().Status != goal.Active {
		t.Fatalf("replace: %+v", a.theGoal())
	}

	// A finished goal is replaced without asking.
	g := *a.theGoal()
	g.Status = goal.Complete
	fixtureGoal(t, a, &g)
	goalCommand(a, "third")
	if a.modal != nil || a.theGoal().Objective != "third" {
		t.Fatal("a complete goal needs no confirmation")
	}
}

func TestGoalSummaryPerStatus(t *testing.T) {
	g := &goal.Goal{Objective: "ship it", TokensUsed: 63876, Seconds: 120}
	hints := map[goal.Status]string{
		goal.Active:       "Commands: /goal edit, /goal pause, /goal clear",
		goal.Paused:       "Commands: /goal edit, /goal resume, /goal clear",
		goal.Blocked:      "Commands: /goal edit, /goal resume, /goal clear",
		goal.UsageLimited: "Commands: /goal edit, /goal resume, /goal clear",
		goal.Complete:     "Commands: /goal edit, /goal clear",
	}
	labels := map[goal.Status]string{
		goal.Active: "active", goal.Paused: "paused", goal.Blocked: "stalled",
		goal.UsageLimited: "usage limited", goal.Complete: "complete",
	}
	for st, hint := range hints {
		g.Status = st
		got := tui.StripEscapes(strings.Join(goalSummaryLines(g, false), "\n"))
		want := "Goal\nStatus: " + labels[st] + "\nObjective: ship it\nTime used: 2m\nTokens used: 63.9K\n\n" + hint
		if got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", st, got, want)
		}
	}
	a := goalApp(t)
	goalCommand(a, "ship it")
	goalCommand(a, "")
	if got := goalText(a); !strings.Contains(got, "Status: active") || !strings.Contains(got, "Commands: /goal edit, /goal pause, /goal clear") {
		t.Fatalf("bare /goal shows the summary:\n%s", got)
	}
}

func TestGoalEditPrompt(t *testing.T) {
	a := goalApp(t)
	goalCommand(a, "old objective")
	g := a.theGoal()
	g.TokensUsed, g.Status = 1200, goal.Paused
	fixtureGoal(t, a, g)

	goalCommand(a, "edit")
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

	goalCommand(a, "edit")
	keys(a, "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
	a.modal.HandleInput("new one")
	keys(a, "enter")
	if a.modal != nil || g.Objective != "old new one" {
		t.Fatalf("edited: %q", g.Objective)
	}
	if g.Status != goal.Paused || g.TokensUsed != 1200 {
		t.Fatalf("a paused goal stays paused and keeps its usage: %+v", g)
	}
	if got := goalText(a); !strings.Contains(got, "• Goal paused") || !strings.Contains(got, "Objective: old new one") {
		t.Fatalf("announces the edit:\n%s", got)
	}

	// A finished goal becomes active again.
	g.Status = goal.Complete
	fixtureGoal(t, a, g)
	goalCommand(a, "edit")
	a.modal.HandleInput("!")
	keys(a, "enter")
	if g.Status != goal.Active {
		t.Fatalf("complete edit -> %s", g.Status)
	}
	g.Status = goal.Blocked
	fixtureGoal(t, a, g)
	goalCommand(a, "edit")
	a.modal.HandleInput("!")
	keys(a, "enter")
	if g.Status != goal.Blocked {
		t.Fatalf("a stalled goal stays stalled: %s", g.Status)
	}
}

func TestGoalEditSteersRunningTurn(t *testing.T) {
	gate := make(chan struct{})
	a, m := liveApp(t, providertest.Reply{Text: "first", Gate: gate}, providertest.Reply{Text: "edited"})
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
	settle(a)
	goalCommand(a, "old")
	typeLine(a, "work")
	if m.Started(5*time.Second) == 0 {
		t.Fatal("model did not start")
	}
	a.ui.Do(func() { a.rpcErr("goal/edit", map[string]any{"input": "new"}) })
	settle(a)
	close(gate)
	waitIdle(t, a)
	if g := a.theGoal(); g.Objective != "new" || g.Status != goal.Active {
		t.Fatalf("goal: %+v", g)
	}
	reqs := m.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1], "edited by the user") || !strings.Contains(reqs[1], `\nnew\n`) {
		t.Fatalf("requests: %v", reqs)
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
		{goal.Goal{Status: goal.Paused}, "Goal paused (/goal resume)"},
		{goal.Goal{Status: goal.Blocked}, "Goal stalled (/goal resume)"},
		{goal.Goal{Status: goal.UsageLimited}, "Goal hit usage limits (/goal resume)"},
		{goal.Goal{Status: goal.Complete, Seconds: 36720}, "Goal achieved (10h 12m)"},
	}
	for _, c := range cases {
		g := c.g
		g.Objective = "x"
		a.info.Goal = &server.GoalInfo{Goal: &g, Seconds: g.Seconds}
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
	a.info.Goal = &server.GoalInfo{Goal: &goal.Goal{Objective: "x", Status: goal.Paused}}
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
	if r := row(110); len(r) != 3 || !strings.Contains(r[2], "2 jobs running") || !strings.HasSuffix(r[0], "Goal paused (/goal resume)") {
		t.Fatalf("jobs: %q", r)
	}

	// A custom status line gives up the room as well.
	a.jobCount, a.statusCmd, a.statusLines = 0, true, []string{"custom status"}
	if r := row(80); len(r) != 1 || !strings.HasPrefix(r[0], " custom status") || !strings.HasSuffix(r[0], "Goal paused (/goal resume)") {
		t.Fatalf("custom: %q", r)
	}
}

func TestGoalInterruptPauses(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, m := liveApp(t, providertest.Reply{Text: "never", Gate: gate})
	typeLine(a, "/goal ship it")
	if m.Started(5*time.Second) == 0 {
		t.Fatal("goal did not run")
	}
	// Esc acts on what the screen shows: wait until it shows the turn.
	within(t, a, "the running goal turn", func() bool { return a.busy })
	key(a, "\x1b")
	within(t, a, "paused goal", func() bool { return !a.busy && a.theGoal() != nil && a.theGoal().Status == goal.Paused })
	within(t, a, "the announced pause", func() bool {
		return a.theGoal().Note == "interrupted" && strings.Contains(bodyText(a), "Goal paused") && !a.goalActive()
	})
}

func TestGoalModelReportsAreAnnounced(t *testing.T) {
	a := goalApp(t)
	goalCommand(a, "ship it")
	f, _ := goal.Load(a.threadID)
	f.Status, f.Note = goal.Blocked, "needs a credential"
	_ = goal.Save(a.threadID, f)
	a.ui.Do(func() { a.rpcErr("goal/read", nil) })
	settle(a)
	got := goalText(a)
	if a.theGoal().Status != goal.Blocked || !strings.Contains(got, "• Goal stalled") || !strings.Contains(got, "Note: needs a credential") {
		t.Fatalf("%+v\n%s", a.theGoal(), got)
	}
}

func TestResumePausedGoalPrompt(t *testing.T) {
	snap := func(g *goal.Goal) []session.Entry {
		raw, _ := json.Marshal(g)
		return []session.Entry{{Type: session.TypeGoal, Goal: raw}}
	}
	for _, st := range []goal.Status{goal.Paused, goal.Blocked, goal.UsageLimited, goal.Active} {
		a := goalApp(t)
		restoreGoal(t, a, snap(&goal.Goal{Objective: "ship it", Status: st}))
		if a.modal == nil {
			t.Fatalf("%s: a goal that is not running asks to resume", st)
		}
		menu := tui.StripEscapes(strings.Join(a.modal.Render(80), "\n"))
		for _, want := range []string{"Resume paused goal?", "Goal: ship it", "Resume goal", "Mark it active and continue when idle", "Leave paused", "Keep it paused; use /goal resume later"} {
			if !strings.Contains(menu, want) {
				t.Fatalf("%s: menu lacks %q:\n%s", st, want, menu)
			}
		}
		if a.theGoal().Status == goal.Active {
			t.Fatalf("%s: an active goal comes back paused", st)
		}
	}

	a := goalApp(t)
	restoreGoal(t, a, snap(&goal.Goal{Objective: "ship it", Status: goal.Paused}))
	keys(a, "down", "enter") // Leave paused
	if a.modal != nil || a.theGoal().Status != goal.Paused {
		t.Fatalf("leave paused: %+v", a.theGoal())
	}
	restoreGoal(t, a, snap(&goal.Goal{Objective: "ship it", Status: goal.Paused}))
	keys(a, "escape")
	if a.modal != nil || a.theGoal().Status != goal.Paused {
		t.Fatalf("esc leaves it paused: %+v", a.theGoal())
	}
	restoreGoal(t, a, snap(&goal.Goal{Objective: "ship it", Status: goal.Blocked, Note: "stuck"}))
	keys(a, "enter") // Resume goal
	if a.modal != nil || a.theGoal().Status != goal.Active || a.theGoal().Note != "" {
		t.Fatalf("resume: %+v", a.theGoal())
	}

	// Finished goals, and sessions without a goal, don't ask.
	c := goalApp(t)
	restoreGoal(t, c, snap(&goal.Goal{Objective: "x", Status: goal.Complete}))
	if c.modal != nil || c.theGoal() == nil {
		t.Fatal("complete: no prompt")
	}
	b := goalApp(t)
	restoreGoal(t, b, nil)
	if b.modal != nil || b.theGoal() != nil {
		t.Fatal("no goal, no prompt")
	}
}

func TestOldGoalSnapshotsStillLoad(t *testing.T) {
	// A snapshot as older atto wrote it: no usage_limited, no pause note.
	raw := `{"objective":"old goal","status":"blocked","budget":1000,"tokensUsed":500,"seconds":61,"note":"stuck","turns":2,"failStreak":1,"created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z"}`
	a := goalApp(t)
	restoreGoal(t, a, []session.Entry{{Type: session.TypeGoal, Goal: json.RawMessage(raw)}})
	g := a.theGoal()
	if g == nil || g.Status != goal.Blocked || g.Objective != "old goal" || g.TokensUsed != 500 || g.Seconds != 61 || g.Turns != 2 {
		t.Fatalf("%+v", g)
	}
	if a.modal == nil {
		t.Fatal("an old stalled goal asks to resume too")
	}
	// The JSON of the goal keeps its keys, and the old status names.
	out, _ := json.Marshal(g)
	for _, key := range []string{`"objective"`, `"status":"blocked"`, `"tokensUsed":500`, `"seconds":61`} {
		if !strings.Contains(string(out), key) {
			t.Errorf("format changed, lost %s: %s", key, out)
		}
	}

	// A goal whose token budget ran out, from when goals had budgets, comes
	// back paused and asks to resume.
	raw = `{"objective":"old goal","status":"budget_limited","budget":1000,"tokensUsed":1200,"seconds":61,"note":"token budget of 1K used","turns":2}`
	b := goalApp(t)
	restoreGoal(t, b, []session.Entry{{Type: session.TypeGoal, Goal: json.RawMessage(raw)}})
	if g := b.theGoal(); g == nil || g.Status != goal.Paused || g.TokensUsed != 1200 || b.modal == nil {
		t.Fatalf("budget limited: %+v, prompt %v", g, b.modal != nil)
	}
	keys(b, "enter") // Resume goal
	if g := b.theGoal(); g.Status != goal.Active {
		t.Fatalf("resumed: %+v", g)
	}
}

func TestUsageLimitedTurnStopsTheGoal(t *testing.T) {
	a, _ := liveApp(t, providertest.Reply{Status: 429, Error: "You have hit your ChatGPT usage limit (plus plan). Try again in ~30 min."})
	typeLine(a, "/goal ship it")
	within(t, a, "usage-limited goal", func() bool { return a.theGoal() != nil && a.theGoal().Status == goal.UsageLimited })
	if !strings.Contains(shown(a), "Goal usage limited") {
		t.Fatal("usage limit not announced")
	}
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
	settle(a)
	goalCommand(a, "resume")
	if a.theGoal().Status != goal.Active {
		t.Fatalf("resume: %+v", a.theGoal())
	}
}

// endTurn simulates a goal turn that ends: steers are the texts committed
// during it, userStart that the user's message started it.
func endTurn(t *testing.T, a *App, userStart bool, steers ...string) {
	t.Helper()
	m := scriptedModel(a)
	if m == nil {
		t.Fatal("missing scripted provider")
	}
	gate := make(chan struct{})
	m.SetScript(providertest.Reply{Command: "echo goal-work", Description: "work", Gate: gate}, providertest.Reply{Text: "done"})
	if userStart {
		typeLine(a, "user asks")
	} else {
		a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": false}) })
	}
	if m.Started(5*time.Second) == 0 {
		t.Fatal("goal turn did not start")
	}
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
	settle(a)
	for _, text := range steers {
		if events.IsEvent(text) || goal.IsMessage(text) {
			continue
		}
		typeLine(a, text)
	}
	settle(a)
	close(gate)
	within(t, a, "goal turn ended", func() bool { return !a.busy })
	settle(a)
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
		{"goal message steer", false, []string{goal.OpenTag + "\npaused\n" + goal.CloseTag}, false},
		{"legacy goal message steer", false, []string{"[atto goal] paused"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := goalApp(t)
			goalCommand(a, "ship it")
			endTurn(t, a, c.userStart, c.steers...)
			if got := a.goalHeld(); got != c.held {
				t.Fatalf("held = %v, want %v", got, c.held)
			}
			if !a.goalActive() {
				t.Fatalf("the goal stays active: %+v", a.theGoal())
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

// A goal on hold is waiting only while idle: a running turn shows the usual
// "Pursuing goal", whatever the turn does about the goal.
func TestGoalIndicatorWhileHeld(t *testing.T) {
	a := goalApp(t)
	goalCommand(a, "ship it")
	endTurn(t, a, true)
	if !a.goalHeld() || !strings.Contains(a.goalIndicator(), "Goal waiting (enter to continue)") {
		t.Fatal("idle hold indicator missing")
	}
	a.ui.Do(func() { a.busy = true })
	if !a.goalHeld() || !strings.Contains(a.goalIndicator(), "Pursuing goal") || strings.Contains(a.goalIndicator(), "waiting") {
		t.Fatal("running held goal indicator wrong")
	}
	a.ui.Do(func() { a.busy = false })
	if !a.goalHeld() || !strings.Contains(a.goalIndicator(), "Goal waiting") {
		t.Fatal("idle hold lost")
	}
}

func TestGoalHoldReleasedByEnterResumeAndNewGoal(t *testing.T) {
	held := func() *App {
		a := goalApp(t)
		goalCommand(a, "ship it")
		endTurn(t, a, true)
		if !a.goalHeld() {
			t.Fatal("not held")
		}
		return a
	}

	a := held()
	typeLine(a, "")
	settle(a) // enter on an empty prompt
	if a.goalHeld() {
		t.Fatal("empty enter releases the hold")
	}

	a = held()
	goalCommand(a, "resume")
	if a.goalHeld() || a.theGoal().Status != goal.Active {
		t.Fatal("/goal resume releases the hold")
	}

	a = held()
	goalCommand(a, "second") // confirm replacing
	keys(a, "enter")
	if a.goalHeld() || a.theGoal().Objective != "second" {
		t.Fatalf("a new goal releases the hold: %+v", a.theGoal())
	}

	a = held()
	a.ui.Do(func() { a.rpcErr("goal/edit", map[string]any{"input": "edited"}) })
	settle(a)
	if a.goalHeld() {
		t.Fatal("editing the objective releases the hold")
	}

	// Pausing keeps nothing waiting, and resuming later starts clean.
	a = held()
	goalCommand(a, "pause")
	goalCommand(a, "resume")
	if a.goalHeld() {
		t.Fatal("resume after pause releases the hold")
	}
}

// A goal held after the user's turn does not start another turn by itself,
// and enter on an empty prompt continues it, against a scripted model.
func TestGoalHoldEndToEnd(t *testing.T) {
	a, m := liveApp(t, providertest.Reply{Text: "ok"})
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
	settle(a)
	goalCommand(a, "ship it")
	send(t, a, "what is going on?")
	time.Sleep(100 * time.Millisecond)
	if len(m.Requests()) != 1 || !a.goalHeld() {
		t.Fatalf("user turn: %d requests, held=%v", len(m.Requests()), a.goalHeld())
	}
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": false}); a.submit("", nil) })
	within(t, a, "goal stalled after continuations", func() bool { return a.theGoal() != nil && a.theGoal().Status == goal.Blocked })
	waitIdle(t, a)
	if len(m.Requests()) != 3 || a.goalHeld() {
		t.Fatalf("continuations: %d requests, held=%v", len(m.Requests()), a.goalHeld())
	}
}

func TestGoalMessageTitle(t *testing.T) {
	g, _ := goal.New("ship it")
	for text, want := range map[string]string{
		g.Continuation():                                  "◎ Continuing goal",
		"[atto goal] <objective>\nx":                      "◎ Continuing goal",
		goal.OpenTag + "\nGoal paused.\n" + goal.CloseTag: "◎ Goal paused.",
		"[atto goal] Goal paused.":                        "◎ Goal paused.",
	} {
		if got := goalMessageTitle(text); got != want {
			t.Errorf("%.40q: %q, want %q", text, got, want)
		}
	}
}

func TestGoalReservedWordsMakeNoGoal(t *testing.T) {
	a := goalApp(t)
	for _, w := range []string{"help", "Help", "status", "show", "--help"} {
		goalCommand(a, w)
		if a.theGoal() != nil || a.modal != nil {
			t.Fatalf("/goal %s made a goal or asked: %+v", w, a.theGoal())
		}
	}
	if got := goalText(a); strings.Count(got, "Usage: /goal [<objective>") != 5 {
		t.Fatalf("each shows the usage (no goal set):\n%s", got)
	}

	goalCommand(a, "ship it")
	goalCommand(a, "status")
	if got := goalText(a); !strings.Contains(got, "Objective: ship it") {
		t.Fatalf("status shows the goal:\n%s", got)
	}
	goalCommand(a, "help")
	if a.theGoal().Objective != "ship it" || a.modal != nil {
		t.Fatalf("help leaves the goal: %+v", a.theGoal())
	}
	if strings.Count(goalText(a), "Usage: /goal [<objective>") != 6 {
		t.Fatalf("help shows the usage with a goal too:\n%s", goalText(a))
	}
}

// A change the user makes to the goal while a turn runs is told to the model.
func TestGoalChangesMidTurnSteerTheModel(t *testing.T) {
	for _, cmd := range []string{"pause", "clear"} {
		t.Run(cmd, func(t *testing.T) {
			gate := make(chan struct{})
			a, m := liveApp(t, providertest.Reply{Text: "current step", Gate: gate})
			a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
			settle(a)
			goalCommand(a, "ship it")
			typeLine(a, "work")
			if m.Started(5*time.Second) == 0 {
				t.Fatal("model did not start")
			}
			goalCommand(a, cmd)
			if !strings.Contains(shown(a), stoppingNotice) {
				t.Fatal("boundary stop not announced")
			}
			close(gate)
			waitIdle(t, a)
			if len(m.Requests()) != 1 {
				t.Fatal("goal change started another step")
			}
			if cmd == "clear" && a.theGoal() != nil || cmd == "pause" && a.theGoal().Status != goal.Paused {
				t.Fatal("goal change lost")
			}
		})
	}
	a := goalApp(t)
	goalCommand(a, "ship it")
	goalCommand(a, "pause")
	goalCommand(a, "clear")
	if strings.Contains(shown(a), stoppingNotice) {
		t.Fatal("idle changes steered a nonexistent run")
	}
}

// A goal note that raced with the end of the turn is dropped: it never
// starts a turn of its own.
func TestGoalNoteLeftAtTurnEndStartsNoTurn(t *testing.T) {
	gate := make(chan struct{})
	a, m := liveApp(t, providertest.Reply{Text: "done", Gate: gate})
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
	settle(a)
	goalCommand(a, "ship it")
	typeLine(a, "work")
	if m.Started(5*time.Second) == 0 {
		t.Fatal("model did not start")
	}
	goalCommand(a, "clear")
	close(gate)
	waitIdle(t, a)
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": false}) })
	settle(a)
	time.Sleep(100 * time.Millisecond)
	if a.busy || len(m.Requests()) != 1 {
		t.Fatal("late goal note started a turn")
	}
}

// requestLog is a scripted model that records the last user message of each
// request.
func requestLog(t *testing.T) (url string, last func() []string) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, body.Messages[len(body.Messages)-1].Content)
		mu.Unlock()
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }
}

// A user message that starts a turn while the goal is not running by itself
// carries a note on the goal's state; nothing else does.
func TestGoalStateNoteAttachesToUserTurns(t *testing.T) {
	a, m := liveApp(t, providertest.Reply{Text: "ok"})
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
	settle(a)
	goalCommand(a, "ship it")
	say := func(text string) string {
		send(t, a, text)
		reqs := m.Requests()
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.Unmarshal([]byte(reqs[len(reqs)-1]), &body); err != nil {
			t.Fatal(err)
		}
		return body.Messages[len(body.Messages)-1].Content
	}
	if got := say("hi"); got != "hi" {
		t.Fatalf("active goal adds no note: %q", got)
	}
	if msg, note := goal.SplitNote(say("thanks")); msg != "thanks" || !strings.Contains(note, "waiting for the user") {
		t.Fatalf("held: %q, %q", msg, note)
	}
	for _, tc := range []struct {
		status     goal.Status
		note, want string
	}{{goal.Paused, goal.NoteInterrupted, "paused because the user interrupted it"}, {goal.Blocked, "stuck", "stalled"}, {goal.UsageLimited, "", "usage limited"}} {
		g := a.theGoal()
		g.Status, g.Note = tc.status, tc.note
		fixtureGoal(t, a, g)
		fixtureGoal(t, a, g)
		a.ui.Do(func() { a.rpcErr("goal/read", nil) })
		settle(a)
		if _, note := goal.SplitNote(say("what now?")); !strings.Contains(note, tc.want) {
			t.Fatalf("%s: %q", tc.status, note)
		}
	}
	g := a.theGoal()
	g.Status = goal.Complete
	fixtureGoal(t, a, g)
	fixtureGoal(t, a, g)
	a.ui.Do(func() { a.rpcErr("goal/read", nil) })
	settle(a)
	if got := say("and now?"); got != "and now?" {
		t.Fatalf("complete goal added note: %q", got)
	}
}

// A transient model error does not stall the goal: the user is told when it
// retries, no "waiting for you" goes with the error, and the wait can be
// ended by /goal pause, /goal clear and Esc.
func TestGoalTransientErrorRetriesLater(t *testing.T) {
	a, _ := liveApp(t, providertest.Reply{Status: 400, Error: "Model is unavailable", Headers: map[string]string{"retry-after-ms": "1"}})
	typeLine(a, "/goal ship it")
	within(t, a, "goal retry", func() bool { return !a.goalRetryAt.IsZero() })
	if a.theGoal().Status != goal.Active || a.theGoal().FailStreak != 0 || !strings.Contains(shown(a), "retrying the goal in 10s (1/6)") || strings.Contains(shown(a), goalWaitingNotice) {
		t.Fatalf("retry: %+v, %s", a.theGoal(), shown(a))
	}
	goalCommand(a, "pause")
	if a.theGoal().Status != goal.Paused || !a.goalRetryAt.IsZero() {
		t.Fatal("pause did not cancel retry")
	}
	goalCommand(a, "resume")
	within(t, a, "second goal retry", func() bool { return !a.goalRetryAt.IsZero() })
	key(a, "\x1b")
	within(t, a, "interrupted retry", func() bool { return a.theGoal().Status == goal.Paused && a.goalRetryAt.IsZero() })
	if a.theGoal().Note != goal.NoteInterrupted {
		t.Fatal("retry interruption lost cause")
	}
	a.ui.Do(func() {
		if a.interrupt() {
			t.Error("nothing left to interrupt")
		}
	})
}

// A message the user sends while the goal's turn runs carries the note that
// the goal is still active, for the model only.
func TestGoalSteerNoteReachesTheModel(t *testing.T) {
	gate := make(chan struct{})
	a, m := liveApp(t, providertest.Reply{Text: "first", Gate: gate}, providertest.Reply{Text: "steered"})
	typeLine(a, "/goal ship it")
	if m.Started(5*time.Second) == 0 {
		t.Fatal("goal did not run")
	}
	typeLine(a, "what is the status?")
	settle(a)
	close(gate)
	within(t, a, "held goal", func() bool { return !a.busy && a.goalHeld() })
	reqs := m.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1], "what is the status?") || !strings.Contains(reqs[1], "not paused") {
		t.Fatalf("requests: %v", reqs)
	}
}

func TestGoalPrefixedUserSteerHoldsGoal(t *testing.T) {
	gate := make(chan struct{})
	a, m := liveApp(t, providertest.Reply{Text: "first", Gate: gate}, providertest.Reply{Text: "done"})
	typeLine(a, "/goal ship it")
	if m.Started(5*time.Second) == 0 {
		t.Fatal("goal did not run")
	}
	text := goal.OpenTag + "\nuser typed this\n" + goal.CloseTag
	typeLine(a, text)
	settle(a)
	close(gate)
	within(t, a, "held prefixed steer", func() bool { return !a.busy && a.goalHeld() })
	if len(a.pending.Steers) != 0 || !strings.Contains(users(a), text) {
		t.Fatal("prefixed user steer treated as internal")
	}
}

func TestGoalLongContextNotice(t *testing.T) {
	for _, long := range []bool{false, true} {
		a := goalApp(t)
		a.ui.Do(func() {
			a.rpcErr("thread/setContextMode", map[string]any{"contextMode": map[bool]string{true: "long", false: "normal"}[long]})
		})
		settle(a)
		goalCommand(a, "ship it")
		want := 0
		if long {
			want = 1
		}
		if n := strings.Count(goalText(a), "price-tier cap is off"); n != want {
			t.Fatalf("long=%v: %s", long, goalText(a))
		}
		goalCommand(a, "pause")
		goalCommand(a, "resume")
		if n := strings.Count(goalText(a), "price-tier cap is off"); n != 2*want {
			t.Fatalf("resume long=%v: %s", long, goalText(a))
		}
		if a.info.LongContext != long {
			t.Fatal("goal changed the context mode")
		}
	}
}

// goalCommand sends the command and waits off the UI goroutine.
func goalCommand(a *App, arg string) {
	a.ui.Do(func() { a.cmdGoal(arg) })
	settle(a)
}

const goalWaitingNotice = "Goal waiting for you — press enter on an empty prompt or /goal resume to continue."
const stoppingNotice = "Stopping the turn after the current step."

func restoreGoal(t *testing.T, a *App, entries []session.Entry) {
	t.Helper()
	w := session.New(a.cwd)
	for _, e := range entries {
		w.Append(e)
	}
	if len(entries) == 0 {
		w.Append(session.Entry{Type: session.TypeName, Name: "no goal"})
	}
	w.Close()
	a.ui.Do(func() { a.resumeID(w.ID) })
	settle(a)
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
	settle(a)
}

// Arbitrary stopped goal states are saved-session fixtures. Opening one is
// the public way to restore them; goal files intentionally accept only a
// restricted set of reports from a running model.
func fixtureGoal(t *testing.T, a *App, g *goal.Goal) {
	t.Helper()
	copy := *g
	raw, _ := json.Marshal(copy)
	restoreGoal(t, a, []session.Entry{{Type: session.TypeGoal, Goal: raw}})
	if a.prompt != nil {
		a.ui.Do(func() { a.rpcErr("prompt/answer", map[string]any{"id": a.prompt.id, "cancel": true}) })
		settle(a)
	}
}
