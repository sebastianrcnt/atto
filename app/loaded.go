package app

import (
	"fmt"
	"strings"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
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

// reloaded follows a reload of the runtime (/reload, atto reload): the
// terminal reads its own settings again, and the catalog.
func (a *App) reloaded() {
	if s, err := config.LoadSettings(); err == nil {
		a.applySettings(s)
	}
	if models, err := config.LoadModels(); err == nil {
		a.models = models
	}
	a.loadCatalog()
	a.statusTrigger()
}
