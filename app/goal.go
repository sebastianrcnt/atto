package app

import (
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/tui"
)

// The goal runs in the runtime (core.GoalDriver, server/goal.go); the
// terminal shows it (the status indicator, /goal) and sends /goal's
// changes. Its confirmations ("Replace goal?", "Resume paused goal?") are
// the runtime's prompts, shown as goalPrompt.

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

// theGoal is the session's goal as the runtime last said (nil: none).
func (a *App) theGoal() *goal.Goal {
	if g := a.info.Goal; g != nil {
		return g.Goal
	}
	return nil
}

func (a *App) goalHeld() bool { return a.info.Goal != nil && a.info.Goal.Held }

// goalActive: a goal that is active and not waiting for the user.
func (a *App) goalActive() bool {
	g := a.theGoal()
	return g != nil && g.Status == goal.Active
}

// goalElapsed is the goal's time with the running turn's, as the runtime
// counts it.
func (a *App) goalElapsed() int64 {
	g := a.info.Goal
	if g == nil {
		return 0
	}
	n := g.Seconds
	if g.TurnStartedAt > 0 && a.busy {
		n += int64(time.Since(time.UnixMilli(g.TurnStartedAt)) / time.Second)
	}
	return n
}

// cmdGoal: /goal [<objective>|clear|edit|pause|resume], as in codex. The
// summary and the edit dialog are the terminal's; the rest goes to the
// runtime, which tells a running turn.
func (a *App) cmdGoal(arg string) {
	arg = strings.TrimSpace(arg)
	switch strings.ToLower(arg) {
	case "help", "-h", "--help":
		a.add(&infoBlock{title: goalUsage})
		return
	case "", "show", "status":
		g := a.theGoal()
		if g == nil {
			a.add(&infoBlock{title: goalUsage, hint: "No goal is currently set."})
			return
		}
		c := *g
		c.Seconds = a.goalElapsed()
		a.add(&contextBlock{lines: goalSummaryLines(&c, a.goalHeld())})
		return
	case "edit":
		a.editGoal()
		return
	}
	a.send("/goal "+arg, nil, "auto")
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

// editGoal is codex's "Edit goal" prompt, prefilled with the objective.
func (a *App) editGoal() {
	g := a.theGoal()
	if g == nil {
		a.errorNotice(errNoGoal)
		a.add(&infoBlock{title: goalUsage, hint: "Create a goal before editing it."})
		return
	}
	in := &labelInput{title: "Edit goal", hint: "Type a goal objective and press Enter · esc cancel", placeholder: "Type a goal objective", text: oneLine(g.Objective)}
	in.onDone = func(ok bool, text string) {
		a.closeModal()
		if ok && strings.TrimSpace(text) != "" && strings.TrimSpace(text) != g.Objective {
			a.rpcErr("goal/edit", map[string]any{"input": text})
		}
	}
	a.openModal(labelModal{in})
}

// goalIndicator is the status indicator, drawn at the right of the status
// line in magenta as codex's footer does (see renderStatus). A goal on hold
// shows as waiting only while idle: a running turn is pursuing it as far as
// the user can tell.
func (a *App) goalIndicator() string {
	g := a.theGoal()
	if g == nil {
		return ""
	}
	if s := g.Indicator(a.goalElapsed(), a.goalHeld() && !a.busy); s != "" {
		return tui.FG(5, s)
	}
	return ""
}

type goalError string

func (e goalError) Error() string { return string(e) }

const errNoGoal = goalError("No goal is currently set.")
