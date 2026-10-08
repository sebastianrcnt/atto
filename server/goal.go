package server

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
)

// The goal is driven by core.GoalDriver, shared with atto -p: the runtime
// feeds it the agent's events, ends its turns and starts the next goal
// turn whenever nothing else is waiting (user input, events, a prompt or
// an open picker). A goal held after a turn with user input waits for the
// user: an empty Enter, /goal resume or goal/resume.

// resetGoal sets up the goal driver for the session, without a goal.
func (t *thread) resetGoal() {
	if t.retryTimer != nil {
		t.retryTimer.Stop()
		t.retryTimer = nil
	}
	t.goal = core.GoalDriver{
		Session:  t.id,
		Steer:    func(text string) { t.agent.Steer(text) },
		Stop:     func() { t.agent.StopAtBoundary() },
		Snapshot: t.snapshotGoal,
		Changed:  t.announceGoal,
		Adopted:  t.goalNotice,
		Error:    t.errorNotice,
		Retrying: func(r core.Retry) { t.notice("", "%s", r.Notice()) },
	}
}

// snapshotGoal records the goal in the session (null when cleared).
func (t *thread) snapshotGoal(g *goal.Goal) {
	raw := json.RawMessage("null")
	if g != nil {
		raw, _ = json.Marshal(g)
	}
	t.sess.Append(session.Entry{Type: session.TypeGoal, Goal: raw})
	t.goalChanged()
}

// announceGoal shows a goal status change in the transcript.
func (t *thread) announceGoal(g *goal.Goal) {
	if g == nil || g.Status == goal.Active {
		return
	}
	c := *g
	t.tr.Add(transcript.Item{Kind: transcript.GoalStatus, GoalState: &c})
	if g.Status == goal.Blocked {
		msg := "The goal is blocked"
		if g.Note != "" {
			msg += ": " + g.Note
		}
		t.notify("goal_blocked", msg)
	}
}

// goalNotice shows "Goal <status>" with the goal's usage summary.
func (t *thread) goalNotice(g *goal.Goal) {
	t.infoNotice("Goal "+g.Status.Label(), g.Summary())
}

// continueGoal starts the next goal turn when nothing else is waiting.
// As in codex, a continuation shows nothing of its own: the turn starts.
func (t *thread) continueGoal() {
	if t.turns.Busy || t.gated() || t.turns.QueuePaused || len(t.turns.Queued) > 0 || len(t.turns.PendingEvents) > 0 || t.goal.Held() || t.closing || t.readOnly != "" {
		return
	}
	if r := t.goal.Pending(); r != nil {
		// A transient failure: wait out the delay (clients can type, pause,
		// clear or interrupt meanwhile).
		if wait := time.Until(r.At); wait > 0 {
			t.scheduleGoalRetry(wait)
			return
		}
	}
	text, ok := t.goal.Next()
	if !ok {
		return
	}
	t.recordSettings()
	t.feed(transcript.Input{Text: text}) // recorded as a goal message
	t.start("turn", "Working on goal", func(ctx context.Context, emit func(any)) error {
		return t.turns.Run(ctx, t.agent, core.TurnRequest{Text: text}, emit)
	})
}

// scheduleGoalRetry starts the continuation again after wait, unless
// something else came first; one timer at a time.
func (t *thread) scheduleGoalRetry(wait time.Duration) {
	if t.retryTimer != nil {
		return
	}
	var tm *time.Timer
	tm = time.AfterFunc(wait, func() {
		t.do(func() {
			if t.retryTimer != tm { // canceled meanwhile
				return
			}
			t.retryTimer = nil
			t.continueGoal()
		})
	})
	t.retryTimer = tm
	t.publish("goal/retry", map[string]any{"at": time.Now().Add(wait).UnixMilli()})
}

// cancelGoalRetry drops the timer of a waiting retry, and reports whether
// there was one.
func (t *thread) cancelGoalRetry() bool {
	if t.retryTimer == nil {
		return false
	}
	t.retryTimer.Stop()
	t.retryTimer = nil
	return true
}

// interruptGoalRetry is Esc while a goal retry waits: it pauses the goal,
// as interrupting its turn would.
func (t *thread) interruptGoalRetry() bool {
	if !t.cancelGoalRetry() {
		return false
	}
	if g := t.goal.Goal; g != nil && g.Status == goal.Active {
		g.Status, g.Note = goal.Paused, goal.NoteInterrupted
		t.goal.Set(g)
		t.goalNotice(g)
	}
	return true
}

// goalWaitingNotice is shown when a goal is held after a turn with user input.
const goalWaitingNotice = "Goal waiting for you — press enter on an empty prompt or /goal resume to continue."

// stoppingNotice is shown when clearing or pausing the goal ends a running turn.
const stoppingNotice = "Stopping the turn after the current step."

// goalUsage is codex's usage line for /goal.
const goalUsage = "Usage: /goal [<objective>|clear|edit|pause|resume]"

var errNoGoal = errors.New("No goal is currently set.")

// cmdGoal: /goal [<objective>|clear|edit|pause|resume], as in codex. A
// change to the goal while a turn runs is told to the model.
func (t *thread) cmdGoal(client, arg string) {
	arg = strings.TrimSpace(arg)
	t.goal.Poll()
	g := t.goal.Goal
	switch strings.ToLower(arg) {
	case "help", "-h", "--help":
		t.infoNotice(goalUsage, "")
		return
	case "", "show", "status":
		if g == nil {
			t.infoNotice(goalUsage, "No goal is currently set.")
			return
		}
		t.infoNotice("Goal "+g.Status.Label(), g.Objective+"\n"+g.Summary())
		return
	case "clear":
		t.clearGoal()
		return
	case "edit":
		t.notice("", "/goal edit is a dialog of the terminal; goal/edit sets the objective.")
		return
	case "pause":
		t.pauseGoal()
		return
	case "resume":
		t.resumeGoalCmd()
		return
	}
	t.setGoal(client, arg)
}

// setGoal makes objective the goal: a finished goal is replaced without
// asking, anything else after a confirmation.
func (t *thread) setGoal(client, objective string) {
	ng, err := goal.New(objective)
	if err != nil {
		t.errorNotice(err)
		return
	}
	if g := t.goal.Goal; g != nil && g.Status != goal.Complete {
		t.confirmReplaceGoal(ng)
		return
	}
	t.startGoal(ng)
}

func (t *thread) clearGoal() {
	if t.goal.Goal == nil {
		t.infoNotice("No goal to clear", "This session does not currently have a goal.")
		return
	}
	t.goal.Set(nil)
	t.cancelGoalRetry()
	told := t.goal.Tell(goal.ClearedMessage())
	t.infoNotice("Goal cleared", "")
	if told {
		t.notice("", stoppingNotice)
	}
	t.goalChanged()
}

func (t *thread) pauseGoal() {
	g := t.goal.Goal
	if g == nil {
		t.errorNotice(errNoGoal)
		return
	}
	if g.Status == goal.Active || g.Status == goal.Blocked || g.Status == goal.UsageLimited {
		g.Status, g.Note = goal.Paused, "paused by the user"
		t.goal.Set(g)
		t.cancelGoalRetry()
		told := t.goal.Tell(g.PausedMessage())
		t.goalNotice(g)
		if told {
			t.notice("", stoppingNotice)
		}
		return
	}
	t.goalNotice(g)
}

func (t *thread) resumeGoalCmd() {
	g := t.goal.Goal
	if g == nil {
		t.errorNotice(errNoGoal)
		return
	}
	if g.Status == goal.Paused || g.Status == goal.Blocked || g.Status == goal.UsageLimited || t.goal.Held() {
		t.resumeGoal()
		return
	}
	t.goalNotice(g)
}

// startGoal makes ng the goal and starts working on it.
func (t *thread) startGoal(ng *goal.Goal) {
	t.goal.Release()
	t.goal.Set(ng)
	t.goalNotice(ng)
	t.continueGoal() // starts now if idle, else after the current turn
}

// resumeGoal makes a paused, stalled or usage limited goal active again,
// or ends the hold of a goal waiting for the user; a resumed run starts a
// fresh stall audit.
func (t *thread) resumeGoal() {
	g := t.goal.Goal
	t.goal.Release()
	g.Status, g.Note, g.FailStreak, g.IdleStreak = goal.Active, "", 0, 0
	t.goal.Set(g)
	t.goalNotice(g)
	t.continueGoal()
}

// confirmReplaceGoal is codex's "Replace goal?" before a new objective
// takes over an unfinished goal.
func (t *thread) confirmReplaceGoal(ng *goal.Goal) {
	obj := ng.Objective
	if r := []rune(obj); len(r) > 200 {
		obj = string(r[:199]) + "…"
	}
	t.choice("Replace goal?", "New objective: "+oneLine(obj), "goal",
		PromptOption{Label: "Replace current goal", Description: "Set the new objective and start it now"},
		PromptOption{Label: "Cancel", Description: "Keep the current goal"},
		func() { t.startGoal(ng) })
}

// promptResumeGoal is codex's "Resume paused goal?", asked when a session
// with a paused, stalled or usage limited goal is opened.
func (t *thread) promptResumeGoal() {
	g := t.goal.Goal
	t.choice("Resume paused goal?", "Goal: "+oneLine(g.Objective), "goal",
		PromptOption{Label: "Resume goal", Description: "Mark it active and continue when idle"},
		PromptOption{Label: "Leave paused", Description: "Keep it paused; use /goal resume later"},
		func() {
			if g := t.goal.Goal; g != nil && g.Status != goal.Active {
				t.resumeGoal()
			}
		})
}

// setObjective applies an edited objective. The goal keeps its usage and
// its status, except that a finished goal becomes active again.
func (t *thread) setObjective(text string) error {
	g := t.goal.Goal
	if g == nil {
		return errNoGoal
	}
	text = strings.TrimSpace(text)
	if text == "" || text == g.Objective {
		return nil
	}
	if _, err := goal.New(text); err != nil { // the same checks
		return err
	}
	g.Objective = text
	t.goal.Release() // an edit is the user steering the goal: no waiting
	if g.Status == goal.Complete {
		g.Status, g.Note, g.FailStreak, g.IdleStreak = goal.Active, "", 0, 0
	}
	t.goal.Set(g)
	t.goalNotice(g)
	if g.Status != goal.Active {
		return nil
	}
	if t.turns.Busy && t.runKind == "turn" {
		t.agent.Steer(g.ObjectiveUpdatedMessage()) // the running turn follows the new objective
	} else {
		t.continueGoal()
	}
	return nil
}

// restoreGoal brings a resumed session's goal back from its last
// snapshot. An active goal comes back paused, so resuming never starts
// work by itself; a paused, stalled or usage limited goal asks.
func (t *thread) restoreGoal(entries []session.Entry) {
	t.resetGoal()
	t.goal.Restore(entries)
	if g := t.goal.Goal; g != nil && t.prompt == nil {
		switch g.Status {
		case goal.Paused, goal.Blocked, goal.UsageLimited:
			t.promptResumeGoal()
		}
	}
	t.goalChanged()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
