package app

import (
	"fmt"
	"strings"

	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/tui"
)

// loadedBlock shows what the session loaded (AGENTS files, skills, hooks,
// configuration, model): one line per kind, and everything on click or
// ctrl+t. After /reload it leads with what changed. It is atto's own
// notice; the model never sees it.
type loadedBlock struct {
	expander
	clickable
	l        core.Loaded
	reloaded bool
	changes  []core.Change
	note     string // what the reload did to the system prompt
}

func (b *loadedBlock) Click(line int) bool {
	if !b.hit(line) {
		return false
	}
	b.toggle()
	return true
}

func (b *loadedBlock) Render(width int) []string {
	dim := func(s string) string { return tui.Truncate(tui.Dim(s), width, tui.Dim("…")) }
	expanded := b.expanded()
	head := "  ◇ Loaded"
	if b.reloaded {
		head = "  ◇ Reloaded · nothing changed"
		if n := len(b.changes); n > 0 {
			head = fmt.Sprintf("  ◇ Reloaded · %d change%s", n, plural(n))
		}
	}
	if !expanded {
		head += " · click or ctrl+t for details"
	}
	out := []string{dim(head)}
	for _, c := range b.changes {
		out = append(out, dim(fmt.Sprintf("    %-8s %s %s", c.Kind, c.What, c.Name)))
	}
	if b.note != "" {
		out = append(out, dim("    "+b.note))
	}
	for _, w := range b.l.Warnings {
		out = append(out, tui.Truncate(tui.FG(3, "    ! "+w), width, "…"))
	}
	if !expanded {
		for _, line := range core.FormatRows(b.l.Summary(), "    ", 12) {
			out = append(out, dim(line))
		}
		return b.clicks(true, out, false)
	}
	for _, s := range b.l.Details() {
		out = append(out, dim("    "+s.Title))
		out = append(out, wrapRows(s.Rows, "      ", 28, width)...)
	}
	return b.clicks(true, append(out, disclosure(true, 0, "")), true)
}

// wrapRows lays rows out as "label  text" with the labels in a column (at
// most maxLabel wide) and the text wrapped under itself. A label too wide
// to leave room puts its text on the lines below.
func wrapRows(rows []core.Row, indent string, maxLabel, width int) []string {
	w := 0
	for _, r := range rows {
		if n := tui.VisibleWidth(r.Label); n > w && n <= maxLabel {
			w = n
		}
	}
	var out []string
	for _, r := range rows {
		head := indent + r.Label + strings.Repeat(" ", max(0, w-tui.VisibleWidth(r.Label))) + "  "
		if width-tui.VisibleWidth(head) < 20 {
			for _, l := range tui.Wrap(r.Label, max(1, width-len(indent))) {
				out = append(out, tui.Dim(indent+l))
			}
			head = indent + "  "
		}
		pad := strings.Repeat(" ", tui.VisibleWidth(head))
		for i, l := range tui.Wrap(r.Text, max(1, width-len(pad))) {
			if i == 0 {
				out = append(out, tui.Dim(head+l))
			} else {
				out = append(out, tui.Dim(pad+l))
			}
		}
	}
	return out
}

// collect reports what the session loaded.
func (a *App) collect() core.Loaded {
	return core.Collect(a.agent, a.hookSrc, a.modelFrom, a.effortFrom)
}

// showLoaded adds the "Loaded" block for a session that just started or
// was resumed.
func (a *App) showLoaded() {
	a.loaded = a.collect()
	a.add(&loadedBlock{l: a.loaded, d: &a.details})
	a.priceTierNotice()
}

// sourceReloaded is the source of the event that tells the model what an
// `atto reload` did.
const sourceReloaded = "reloaded"

func (a *App) cmdReload(string) { a.requestReload(false) }

// requestReload reads AGENTS files, skills, hooks, settings.json and
// models.json again: now when idle, else between the running turn's steps
// (a request in flight keeps the prompt it was sent with). forModel: the
// agent asked (atto reload) and is told the result.
func (a *App) requestReload(forModel bool) {
	if !a.busy {
		a.reloadNow(forModel)
		return
	}
	if !forModel {
		a.notice("Reloading after the current step.")
	}
	id, path := a.sess.ID, a.sess.Path
	a.agent.AtBoundary(func() string { return a.reloadBetweenSteps(id, path, forModel) })
}

// reloadBetweenSteps runs on the turn's goroutine, at a step boundary (or
// after the run, if it reached none). Its result goes to the model with
// the turn's next request.
func (a *App) reloadBetweenSteps(id, path string, forModel bool) string {
	var prev core.Loaded
	a.ui.Do(func() { prev = a.loaded })
	r, err := core.Reload(a.agent, id, path, prev)
	a.ui.Do(func() { a.applyReload(r, err) })
	if !forModel {
		return ""
	}
	return events.Format([]events.Event{{Text: reloadReport(r, err)}})
}

// reloadNow reloads while no turn runs. The model's report, if it asked,
// is delivered like any event: it starts a turn.
func (a *App) reloadNow(forModel bool) {
	r, err := core.Reload(a.agent, a.sess.ID, a.sess.Path, a.loaded)
	a.applyReload(r, err)
	if forModel {
		a.pendingEvents = append(a.pendingEvents, events.Event{Source: sourceReloaded, Title: "Reload result sent to the agent", Text: reloadReport(r, err)})
	}
}

func reloadReport(r core.Reloaded, err error) string {
	if err != nil {
		return "Reload failed, nothing changed: " + err.Error()
	}
	return r.ForModel()
}

// applyReload takes over what a reload read and shows what changed.
func (a *App) applyReload(r core.Reloaded, err error) {
	if err != nil {
		a.errorNotice(fmt.Errorf("reload failed, nothing changed: %w", err))
		return
	}
	a.models, a.hooks, a.hookSrc, a.loaded = r.Models, r.Hooks, r.HookSrc, r.Loaded
	a.escAction = r.Settings.DoubleEscapeAction
	a.spinnerVerbs, a.spinnerScan = r.Settings.SpinnerVerbs, r.Settings.SpinnerScanner
	a.skipSummary = r.Settings.BranchSummary != nil && r.Settings.BranchSummary.SkipPrompt
	a.noToolGroups = r.Settings.ToolGroups != nil && !*r.Settings.ToolGroups
	if a.sugList != nil { // skills may have changed
		a.sugList.Items = commandItems(a.allCommands())
	}
	a.add(&loadedBlock{l: r.Loaded, reloaded: true, changes: r.Changes, note: r.PromptNote(), d: &a.details})
	a.priceTierNotice()
	a.statusTrigger()
	a.askMCPApprovals() // a project server the reload found
}
