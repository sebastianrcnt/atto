package app

import (
	"slices"
	"strings"

	"github.com/sebastianrcnt/atto/tui"
	"github.com/sebastianrcnt/atto/ui"
)

// renderUIStatus preserves main's native status layout while sourcing each
// drawing from the shared registry. Process RSS and an elapsed clock are local
// display data, not worker heap or per-frame shared mutations.
func (a *App) renderUIStatus(width int) []string {
	trees := map[string]*ui.Node{}
	for _, instance := range a.liveUI(ui.Status) {
		if id, ok := strings.CutPrefix(instance.ID, "atto/"); ok && isBuiltinStatus(instance.ID) {
			if e := a.elements[ui.Match{Site: ui.Status, ID: instance.ID}]; e != nil && e.Rev == instance.Rev {
				trees[id] = e.Tree
			}
		}
	}
	if trees["memory"] == nil {
		n := ui.Text(ui.TextProps{Color: ui.Muted, Text: fmtBytes(rssBytes.Load())})
		trees["memory"] = &n
	}
	if g := a.info.Goal; g != nil && trees["goal"] != nil && ui.PlainText(*trees["goal"]) == g.Indicator {
		n := ui.Text(ui.TextProps{Color: ui.Accent, Text: tui.StripEscapes(a.goalIndicator())})
		trees["goal"] = &n
	}
	text := func(id string) string {
		n := trees[id]
		if n == nil {
			return ""
		}
		theme := func(c ui.ThemeKey, s string) string {
			switch c {
			case ui.Muted, ui.Border:
				return tui.Dim(s)
			case ui.Success, ui.DiffAdd:
				return tui.FG(2, s)
			case ui.Warning:
				return tui.FG(3, s)
			case ui.Error, ui.DiffRemove:
				return tui.FG(1, s)
			case ui.Accent, ui.DiffHunk:
				color := 6
				switch id {
				case "goal":
					color = 5
				case "timers":
					color = 4
				case "effort":
					switch ui.PlainText(*n) {
					case "low":
						color = 4
					case "medium":
					default:
						color = 5
					}
				}
				return tui.FG(color, s)
			}
			return s
		}
		if n.Type == "Text" {
			return tui.StatusSpans(*n, theme)
		}
		// Extensions may wrap or replace a built-in; retain the catalog renderer.
		e := &tui.Elements{Tree: n, Theme: theme}
		return strings.Join(e.Render(512), " ")
	}
	ind := text("goal")
	rowWidth, own := width, false
	if ind != "" {
		if rowWidth = width - tui.VisibleWidth(ind) - 2; rowWidth < minStatusWithGoal {
			rowWidth, own = width, true
		}
	}
	var out []string
	if trees["custom"] != nil {
		custom := text("custom")
		if custom != "" {
			for i, l := range strings.Split(custom, "\n") {
				w := width
				if i == 0 {
					w = rowWidth
				}
				out = append(out, tui.Truncate(" "+l, w, "…"))
			}
		}
	} else {
		var items []statusItem
		for _, slot := range []struct {
			id, pre string
			drop    int
			right   bool
		}{
			{"model", "", 0, false}, {"effort", "", dropEffort, false}, {"context", "  ", 0, false},
			{"contextSize", " ", dropCtxSize, false}, {"cache", "", dropCacheRate, false},
			{"tokens", "", dropTokens, false}, {"cacheWrite", "", dropCacheTotals, false},
			{"cost", "", dropCost, false}, {"memory", "", dropMem, true},
		} {
			if s := text(slot.id); s != "" {
				items = append(items, statusItem{text: s, pre: slot.pre, drop: slot.drop, right: slot.right})
			}
		}
		if len(items) > 0 {
			out = layoutStatus(items, text("path"), text("branch"), rowWidth, width)
		}
	}
	if len(out) > 0 {
		out[0] = a.withToast(out[0], rowWidth)
		if ind != "" && !own {
			out[0] += strings.Repeat(" ", max(1, width-1-tui.VisibleWidth(out[0])-tui.VisibleWidth(ind))) + ind
		}
	}
	flags := a.extensionStatus()
	if ind != "" && own {
		flags = append(flags, ind)
	}
	if s := text("jobs"); s != "" {
		flags = append(flags, s)
	}
	if s := text("timers"); s != "" {
		flags = append(flags, s)
	}
	if len(flags) > 0 {
		out = append(out, tui.Truncate(" "+strings.Join(flags, tui.Dim(" · ")), width, "…"))
	}
	return append(out, a.renderAdditionalUIStatus(width)...)
}

// Built-in placement rules apply only to the catalogued native slots, not to
// every Go-owned status item. Additional Go/provider slots must remain visible.
func isBuiltinStatus(id string) bool {
	local, ok := strings.CutPrefix(id, "atto/")
	if !ok {
		return false
	}
	return slices.Contains(ui.StatusIDs, local)
}
