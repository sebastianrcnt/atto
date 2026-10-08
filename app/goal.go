package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// The goal is driven by core.GoalDriver, shared with atto -p: the TUI
// feeds it the agent's events (onEvent), ends its turns (afterRun) and
// starts the next goal turn whenever nothing else is waiting.

// resetGoal sets up the goal driver for the current session, without a
// goal: /goal sets one, a resume restores it.
func (a *App) resetGoal() {
	a.goal = core.GoalDriver{
		Session:  a.sess.ID,
		Steer:    func(text string) { a.agent.Steer(text) },
		Stop:     func() { a.agent.StopAtBoundary() },
		Snapshot: a.snapshotGoal,
		Changed:  a.announceGoal,
		Adopted:  a.goalStarted,
		Error:    a.errorNotice,
		Retrying: func(r core.Retry) { a.notice("%s", r.Notice()) },
	}
}

// snapshotGoal records the goal in the session (null when cleared).
func (a *App) snapshotGoal(g *goal.Goal) {
	raw := json.RawMessage("null")
	if g != nil {
		raw, _ = json.Marshal(g)
	}
	a.sess.Append(session.Entry{Type: session.TypeGoal, Goal: raw})
	a.remoteGoal()
}

// continueGoal starts the next goal turn when nothing else is waiting:
// user input (queued, pending events, an open picker) always goes first,
// and a goal held after a turn with user input waits for the user (enter on
// an empty prompt, or /goal resume).
// As in codex, a continuation shows nothing of its own: the turn just starts.
func (a *App) continueGoal() {
	if a.busy || a.modal != nil || a.queuePaused || len(a.queued) > 0 || len(a.pendingEvents) > 0 || a.goal.Held() {
		return
	}
	if r := a.goal.Pending(); r != nil {
		// A transient failure: wait out the delay without blocking the UI
		// (the user can type, /goal pause, clear or press Esc meanwhile).
		if wait := time.Until(r.At); wait > 0 {
			a.scheduleGoalRetry(wait)
			return
		}
	}
	text, ok := a.goal.Next()
	if !ok {
		return
	}
	a.runKind = "turn"
	a.recordSettings()
	a.tr().Event(transcript.Input{Text: text}) // recorded as a goal message
	a.start("Working on goal", func(ctx context.Context, emit func(any)) error {
		return a.agent.Run(ctx, text, emit)
	})
}

// scheduleGoalRetry starts the continuation again after wait, unless
// something else came first (see cancelGoalRetry); one timer at a time.
func (a *App) scheduleGoalRetry(wait time.Duration) {
	if a.retryTimer != nil {
		return
	}
	var t *time.Timer
	t = time.AfterFunc(wait, func() {
		a.ui.Do(func() {
			if a.retryTimer != t { // canceled meanwhile
				return
			}
			a.retryTimer = nil
			a.continueGoal()
		})
	})
	a.retryTimer = t
}

// cancelGoalRetry drops the timer of a waiting retry, and reports whether
// there was one.
func (a *App) cancelGoalRetry() bool {
	if a.retryTimer == nil {
		return false
	}
	a.retryTimer.Stop()
	a.retryTimer = nil
	return true
}

// interruptGoalRetry is Esc while a goal retry waits: it pauses the goal, as
// interrupting its turn would.
func (a *App) interruptGoalRetry() bool {
	if !a.cancelGoalRetry() {
		return false
	}
	if g := a.goal.Goal; g != nil && g.Status == goal.Active {
		g.Status, g.Note = goal.Paused, goal.NoteInterrupted
		a.goal.Set(g)
		a.goalInfo(g)
	}
	return true
}

// goalWaitingNotice is shown when a goal is held after a turn with user input.
const goalWaitingNotice = "Goal waiting for you — press enter on an empty prompt or /goal resume to continue."

// stoppingNotice is shown when /goal clear or /goal pause ends a running turn.
const stoppingNotice = "Stopping the turn after the current step."

// goalUsage is codex's usage line for /goal.
const goalUsage = "Usage: /goal [<objective>|clear|edit|pause|resume]"

// infoBlock is codex's info message: a title after a bullet, and a dim hint
// under it.
type infoBlock struct {
	title, hint string
	cache       tui.RenderCache[[2]string]
}

func (b *infoBlock) Render(width int) []string {
	return b.cache.Render(width, [2]string{b.title, b.hint}, func() []string { return b.render(width) })
}

func (b *infoBlock) render(width int) []string {
	out := []string{tui.Truncate("  "+tui.Dim("• ")+b.title, width, "…")}
	if b.hint != "" {
		for _, l := range tui.Wrap(b.hint, max(1, width-4)) {
			out = append(out, "    "+tui.Dim(l))
		}
	}
	return out
}

// goalInfo shows "Goal <status>" with the goal's usage summary.
func (a *App) goalInfo(g *goal.Goal) {
	a.add(&infoBlock{title: "Goal " + g.Status.Label(), hint: g.Summary()})
}

func (a *App) goalStarted(g *goal.Goal) {
	a.goalInfo(g)
	if a.agent.LongContext() {
		a.notice("%s", goal.LongContextNotice)
	}
}

var errNoGoal = errors.New("No goal is currently set.")

// cmdGoal: /goal [<objective>|clear|edit|pause|resume], as in codex. A
// change to the goal while a turn runs is told to the model (the turn would
// otherwise go on with the goal as it was). The words help and status alone
// are commands, never objectives.
func (a *App) cmdGoal(arg string) {
	arg = strings.TrimSpace(arg)
	a.goal.Poll()
	g := a.goal.Goal
	switch strings.ToLower(arg) {
	case "help", "-h", "--help":
		a.add(&infoBlock{title: goalUsage})
		return
	case "", "show", "status":
		if g == nil {
			a.add(&infoBlock{title: goalUsage, hint: "No goal is currently set."})
			return
		}
		a.add(&contextBlock{lines: goalSummaryLines(g, a.goal.Held())})
		return
	case "clear":
		if g == nil {
			a.add(&infoBlock{title: "No goal to clear", hint: "This session does not currently have a goal."})
			return
		}
		a.goal.Set(nil)
		a.cancelGoalRetry()
		told := a.goal.Tell(goal.ClearedMessage())
		a.add(&infoBlock{title: "Goal cleared"})
		if told {
			a.notice(stoppingNotice)
		}
		return
	case "edit":
		a.editGoal()
		return
	case "pause":
		if g == nil {
			a.errorNotice(errNoGoal)
			return
		}
		if g.Status == goal.Active || g.Status == goal.Blocked || g.Status == goal.UsageLimited {
			g.Status, g.Note = goal.Paused, "paused by the user"
			a.goal.Set(g)
			a.cancelGoalRetry()
			told := a.goal.Tell(g.PausedMessage())
			a.goalInfo(g)
			if told {
				a.notice(stoppingNotice)
			}
			return
		}
		a.goalInfo(g)
		return
	case "resume":
		if g == nil {
			a.errorNotice(errNoGoal)
			return
		}
		if g.Status == goal.Paused || g.Status == goal.Blocked || g.Status == goal.UsageLimited || a.goal.Held() {
			a.resumeGoal()
			return
		}
		a.goalInfo(g)
		return
	}
	ng, err := goal.New(arg)
	if err != nil {
		a.errorNotice(err)
		return
	}
	// A finished goal is replaced without asking; anything else is confirmed.
	if g != nil && g.Status != goal.Complete {
		a.confirmReplaceGoal(ng)
		return
	}
	a.startGoal(ng)
}

// startGoal makes ng the goal and starts working on it.
func (a *App) startGoal(ng *goal.Goal) {
	a.goal.Release()
	a.goal.Set(ng)
	a.goalStarted(ng)
	a.continueGoal() // starts now if idle, else after the current turn
}

// resumeGoal makes a paused, stalled or usage limited goal active again, or
// ends the hold of a goal waiting for the user; a resumed run starts a
// fresh stall audit.
func (a *App) resumeGoal() {
	g := a.goal.Goal
	a.goal.Release()
	g.Status, g.Note, g.FailStreak, g.IdleStreak = goal.Active, "", 0, 0
	a.goal.Set(g)
	a.goalStarted(g)
	a.continueGoal()
}

// goalSummaryLines is codex's summary of /goal without arguments.
func goalSummaryLines(g *goal.Goal, held bool) []string {
	lines := []string{
		tui.Bold("Goal"),
		tui.Dim("Status: ") + g.Status.Label(),
		tui.Dim("Objective: ") + g.Objective,
		tui.Dim("Time used: ") + goal.FormatElapsed(g.Seconds),
		tui.Dim("Tokens used: ") + goal.Tokens(g.TokensUsed),
	}
	hint := "Commands: /goal edit, /goal clear"
	switch g.Status {
	case goal.Active:
		hint = "Commands: /goal edit, /goal pause, /goal clear"
		if held {
			hint = "Waiting for you. Commands: /goal resume, /goal edit, /goal pause, /goal clear"
		}
	case goal.Paused, goal.Blocked, goal.UsageLimited:
		hint = "Commands: /goal edit, /goal resume, /goal clear"
	}
	return append(lines, "", tui.Dim(hint))
}

// goalPrompt is a picker with a heading above it, as codex's confirmations.
type goalPrompt struct {
	title, subtitle string
	list            *tui.SelectList
}

func (p goalPrompt) HandleInput(data string) { p.list.HandleInput(data) }

func (p goalPrompt) Render(width int) []string {
	out := []string{tui.Truncate(" "+tui.Bold(p.title), width, "…"), tui.Truncate(" "+tui.Dim(p.subtitle), width, "…"), ""}
	out = append(out, p.list.Render(width)...)
	return append(out, "", tui.Truncate(tui.Dim("  Press enter to confirm or esc to go back"), width, "…"))
}

// goalChoice shows a two-way choice; the first item is the default.
func (a *App) goalChoice(title, subtitle string, yes, no tui.SelectItem, onYes func()) {
	l := &tui.SelectList{Items: []tui.SelectItem{yes, no}}
	l.OnSelect = func(it tui.SelectItem) {
		a.closeModal()
		if it.Value == yes.Value {
			onYes()
		}
	}
	l.OnCancel = a.closeModal
	a.openModal(goalPrompt{title: title, subtitle: subtitle, list: l})
}

// confirmReplaceGoal is codex's "Replace goal?" before a new objective
// takes over an unfinished goal.
func (a *App) confirmReplaceGoal(ng *goal.Goal) {
	obj := ng.Objective
	if r := []rune(obj); len(r) > 200 {
		obj = string(r[:199]) + "…"
	}
	a.goalChoice("Replace goal?", "New objective: "+oneLine(obj),
		tui.SelectItem{Label: "Replace current goal", Detail: "Set the new objective and start it now", Value: "yes"},
		tui.SelectItem{Label: "Cancel", Detail: "Keep the current goal", Value: "no"},
		func() { a.startGoal(ng) })
}

// promptResumeGoal is codex's "Resume paused goal?", shown when a session
// with a paused, stalled or usage limited goal is opened.
func (a *App) promptResumeGoal() {
	g := a.goal.Goal
	a.goalChoice("Resume paused goal?", "Goal: "+oneLine(g.Objective),
		tui.SelectItem{Label: "Resume goal", Detail: "Mark it active and continue when idle", Value: "yes"},
		tui.SelectItem{Label: "Leave paused", Detail: "Keep it paused; use /goal resume later", Value: "no"},
		func() {
			if g := a.goal.Goal; g != nil && g.Status != goal.Active {
				a.resumeGoal()
			}
		})
}

// editGoal is codex's "Edit goal" prompt, prefilled with the objective.
func (a *App) editGoal() {
	g := a.goal.Goal
	if g == nil {
		a.errorNotice(errNoGoal)
		a.add(&infoBlock{title: goalUsage, hint: "Create a goal before editing it."})
		return
	}
	in := &labelInput{title: "Edit goal", hint: "Type a goal objective and press Enter · esc cancel", placeholder: "Type a goal objective", text: oneLine(g.Objective)}
	in.onDone = func(ok bool, text string) {
		a.closeModal()
		if ok {
			a.setObjective(text)
		}
	}
	a.openModal(labelModal{in})
}

// setObjective applies an edited objective. The goal keeps its usage and,
// as in codex, its status, except that a finished goal becomes active
// again.
func (a *App) setObjective(text string) {
	g := a.goal.Goal
	if g == nil {
		a.errorNotice(errNoGoal)
		return
	}
	text = strings.TrimSpace(text)
	if text == "" || text == g.Objective {
		return
	}
	if _, err := goal.New(text); err != nil { // the same checks
		a.errorNotice(err)
		return
	}
	wasComplete := g.Status == goal.Complete
	g.Objective = text
	a.goal.Release() // an edit is the user steering the goal: no waiting
	if g.Status == goal.Complete {
		g.Status, g.Note, g.FailStreak, g.IdleStreak = goal.Active, "", 0, 0
	}
	a.goal.Set(g)
	if wasComplete {
		a.goalStarted(g)
	} else {
		a.goalInfo(g)
	}
	if g.Status != goal.Active {
		return
	}
	if a.busy && a.runKind == "turn" {
		a.agent.Steer(g.ObjectiveUpdatedMessage()) // the running turn follows the new objective
	} else {
		a.continueGoal()
	}
}

// restoreGoal brings a resumed session's goal back from its last snapshot.
// An active goal comes back paused, so resuming never starts work by itself;
// a paused, stalled or usage limited goal asks whether to resume.
func (a *App) restoreGoal(entries []session.Entry) {
	a.resetGoal()
	a.goal.Restore(entries)
	if g := a.goal.Goal; g != nil && a.modal == nil {
		switch g.Status {
		case goal.Paused, goal.Blocked, goal.UsageLimited:
			a.promptResumeGoal()
		}
	}
}

// goalIndicator is the status indicator, drawn at the right of the status
// line in magenta as codex's footer does (see renderStatus). A goal on hold
// shows as waiting only while idle: a running turn is pursuing it as far as
// the user can tell.
func (a *App) goalIndicator() string {
	g := a.goal.Goal
	if g == nil {
		return ""
	}
	if s := g.Indicator(a.goal.Elapsed(), a.goal.Held() && !a.busy); s != "" {
		return tui.FG(5, s)
	}
	return ""
}
