package app

// Frozen from main's app/statusline.go, before the shared UI migration.
// Keep this renderer independent: golden updates must not use the UI compositor.
import (
	"cmp"
	"fmt"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/tui"
	"strings"
)

func (a *App) renderMainStatus(width int) []string {
	// The goal indicator goes at the right end of the first row, as codex's
	// footer shows it; the row gives up room for it. On a terminal too narrow
	// for both, it takes a row of its own.
	ind := a.goalIndicator()
	rowWidth, own := width, false
	if ind != "" {
		if rowWidth = width - tui.VisibleWidth(ind) - 2; rowWidth < minStatusWithGoal {
			rowWidth, own = width, true
		}
	}
	var out []string
	if a.statusCmd {
		for i, l := range a.statusLines {
			w := width
			if i == 0 {
				w = rowWidth
			}
			out = append(out, tui.Truncate(" "+l, w, "…"))
		}
	} else {
		out = a.buildMainStatus(a.model(), a.effort(), rowWidth, width)
	}
	if len(out) > 0 {
		out[0] = a.withToast(out[0], rowWidth)
		if ind != "" && !own {
			out[0] += strings.Repeat(" ", max(1, width-1-tui.VisibleWidth(out[0])-tui.VisibleWidth(ind))) + ind
		}
	}
	// Transient indicators go on their own line so custom output is untouched.
	flags := a.extensionStatus()
	if ind != "" && own {
		flags = append(flags, ind)
	}
	if a.jobCount > 0 {
		flags = append(flags, tui.FG(2, fmt.Sprintf("● %d job%s running (/jobs)", a.jobCount, plural(a.jobCount))))
	}
	if a.timerCount > 0 {
		flags = append(flags, tui.FG(4, fmt.Sprintf("⏱ %d timer%s (/timers)", a.timerCount, plural(a.timerCount))))
	}
	if len(flags) > 0 {
		out = append(out, tui.Truncate(" "+strings.Join(flags, tui.Dim(" · ")), width, "…"))
	}
	return out
}

func (a *App) buildMainStatus(m config.ModelRef, effort string, first, width int) []string {
	sep := tui.Dim(" · ")
	u := &a.usage

	items := []statusItem{{text: tui.FG(6, "◆ ") + a.models.DisplayName(m)}}
	if effort != "" && len(m.Model.Levels()) > 0 {
		items = append(items, statusItem{text: effortStyle(effort), drop: dropEffort})
	}
	if cw := m.Model.ContextWindow; cw > 0 {
		pct := a.ctxTokens * 100 / cw
		style := tui.Dim
		limit, cap := a.info.AutoCompactLimit, a.info.AutoCompactCap
		if limit > 0 && a.ctxTokens*100/limit >= 80 {
			style = func(s string) string { return tui.FG(3, s) } // nearing auto-compaction
		}
		label := fmt.Sprintf(" %d%%", pct)
		if a.info.LongContext {
			label += " long"
		}
		size := fmt.Sprintf("%s/%s", compactTokens(a.ctxTokens), compactTokens(cw))
		if cap > 0 { // a price tier or a setting compacts before the window fills
			size += " ⇥" + compactTokens(limit)
		}
		items = append(items,
			statusItem{text: style(contextBar(pct, 10) + label), pre: "  "},
			statusItem{text: style(size), pre: " ", drop: dropCtxSize})
	}
	if c := u.cacheLabel(); c != "" {
		items = append(items, statusItem{text: tui.Dim(c), drop: dropCacheRate})
	}
	var io, rw []string
	if n := u.fresh(); n > 0 {
		io = append(io, "↑"+compactTokens(n))
	}
	if u.output > 0 {
		io = append(io, "↓"+compactTokens(u.output))
	}
	// Cache reads are left out: a long conversation reads its whole prefix
	// back on every request, so their total says little (the hit rate does).
	if u.cacheWrite > 0 {
		rw = append(rw, "W"+compactTokens(u.cacheWrite))
	}
	if len(io) > 0 {
		items = append(items, statusItem{text: tui.Dim(strings.Join(io, " ")), drop: dropTokens})
	}
	if len(rw) > 0 {
		items = append(items, statusItem{text: tui.Dim(strings.Join(rw, " ")), drop: dropCacheTotals})
	}
	// Only a model with prices has a cost; a local model's session is free.
	// On a subscription the prices only estimate what the usage would cost
	// over the API.
	if priced(m.Model) || u.cost > 0 {
		cost := fmt.Sprintf("$%.3f", u.cost) + a.surcharge(m)
		if m.Provider.Subscription {
			cost = "≈" + cost
		}
		items = append(items, statusItem{text: tui.Dim(cost), drop: dropCost})
	}

	items = append(items, statusItem{text: tui.Dim(fmtBytes(rssBytes.Load())), drop: dropMem, right: true})
	for i := range items {
		items[i].w = tui.VisibleWidth(items[i].text)
	}

	branch := ""
	if a.gitBranch != "" {
		branch = " (" + a.gitBranch + ")"
	}
	where := core.ShortPath(a.cwd)
	whereW, branchW, sepW := tui.VisibleWidth(where+branch), tui.VisibleWidth(branch), tui.VisibleWidth(sep)

	// row lays out one row w wide: l at the left, the directory (when
	// path) and r at the right. ok is false when they do not fit; the
	// directory is shortened from the front to the room that remains.
	row := func(l, r []statusItem, w int, path bool) (s string, ok bool) {
		lw, rw := statusRun(l, sepW), statusRun(r, sepW)
		gapMin := 0
		if len(l) > 0 {
			gapMin = 2
		}
		dir := ""
		if path {
			room := w - 1 - lw - gapMin
			if len(r) > 0 {
				room -= sepW + rw
			}
			if room < min(minPath, whereW) {
				return "", false
			}
			dir = tui.Dim(compressPath(where, room-branchW) + branch)
			if len(r) > 0 {
				dir += sep
			}
			rw += tui.VisibleWidth(dir)
		}
		if len(l) == 0 && rw == 0 {
			return "", true
		}
		var b strings.Builder
		b.WriteByte(' ')
		joinStatus(&b, l, sep)
		gap := w - 1 - lw - rw
		if rw == 0 {
			return tui.Truncate(b.String(), w, "…"), gap >= 0
		}
		if gap < gapMin {
			return tui.Truncate(b.String(), w, "…"), false
		}
		b.WriteString(strings.Repeat(" ", gap))
		b.WriteString(dir)
		joinStatus(&b, r, sep)
		return b.String(), true
	}

	var left, right []statusItem
	keep := func(cut int) {
		left, right = left[:0], right[:0]
		for _, it := range items {
			switch {
			case it.drop != 0 && it.drop <= cut:
			case it.right:
				right = append(right, it)
			default:
				left = append(left, it)
			}
		}
	}

	keep(0)
	if s, ok := row(left, right, first, true); ok {
		return []string{s}
	}
	for cut := 0; ; cut++ {
		keep(cut)
		// The first row takes the left items in order while they fit (the
		// model always); the rest start the second.
		n, w := 1, left[0].w
		for ; n < len(left); n++ {
			add := cmp.Or(len(left[n].pre), sepW) + left[n].w
			if 1+w+add > first {
				break
			}
			w += add
		}
		second, ok := row(left[n:], right, width, cut < dropPath)
		if !ok && cut < dropEffort {
			continue
		}
		var b strings.Builder
		b.WriteByte(' ')
		joinStatus(&b, left[:n], sep)
		out := []string{tui.Truncate(b.String(), first, "…")}
		if second != "" {
			out = append(out, second)
		}
		return out
	}
}
