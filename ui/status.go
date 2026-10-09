package ui

import (
	"fmt"
	"strings"
)

// StatusData contains the session-owned content of the native status slots.
// Layout, process RSS and the elapsed goal clock belong to each client.
type StatusData struct {
	Model, Effort                                          string
	ContextTokens, ContextWindow, CompactLimit, CompactCap int
	Long                                                   bool
	Fresh, Output, CacheWrite, LastInput, LastCached       int
	Cost, Path, Branch, Goal                               string
	Jobs, Timers                                           int
	Custom                                                 bool
	CustomLines                                            []string
}

var StatusIDs = []string{"model", "effort", "context", "contextSize", "cache", "tokens", "cacheWrite", "cost", "path", "branch", "memory", "goal", "jobs", "timers", "custom"}

// StatusTrees builds the same passive text spans for full and slim workers.
// Separate context bar/size and path/branch slots retain native drop/compression
// semantics; they are not independently packed by generic priority layout.
func StatusTrees(d StatusData) map[string]*Node {
	out := map[string]*Node{}
	put := func(id string, n Node) { out[id] = &n }
	text := func(s string, c ThemeKey) Node { return Text(TextProps{Text: CleanText(s), Color: c}) }
	if d.Custom {
		put("custom", ThemedText(strings.Join(d.CustomLines, "\n")))
	} else {
		put("model", Text(TextProps{}, text("◆ ", Accent), text(d.Model, "")))
		if d.Effort != "" {
			c, bold := Accent, false
			switch d.Effort {
			case "off", "minimal":
				c = Muted
			case "high":
				c = Warning
			case "low", "medium":
			default:
				bold = true
			}
			put("effort", Text(TextProps{Text: d.Effort, Color: c, Bold: bold}))
		}
		if d.ContextWindow > 0 {
			pct := d.ContextTokens * 100 / d.ContextWindow
			filled := min(10, (pct*10+50)/100)
			label := fmt.Sprintf(" %d%%", pct)
			if d.Long {
				label += " long"
			}
			color := Muted
			if d.CompactLimit > 0 && d.ContextTokens*100/d.CompactLimit >= 80 {
				color = Warning
			}
			put("context", Text(TextProps{Color: color}, text(strings.Repeat("━", filled), Accent), text(strings.Repeat("─", 10-filled)+label, "")))
			size := fmt.Sprintf("%s/%s", StatusTokens(d.ContextTokens), StatusTokens(d.ContextWindow))
			if d.CompactCap > 0 {
				size += " ⇥" + StatusTokens(d.CompactLimit)
			}
			put("contextSize", text(size, color))
		}
		if d.LastInput > 0 {
			put("cache", text(fmt.Sprintf("cache %d%%", d.LastCached*100/d.LastInput), Muted))
		}
		var io []string
		if d.Fresh > 0 {
			io = append(io, "↑"+StatusTokens(d.Fresh))
		}
		if d.Output > 0 {
			io = append(io, "↓"+StatusTokens(d.Output))
		}
		if len(io) > 0 {
			put("tokens", text(strings.Join(io, " "), Muted))
		}
		if d.CacheWrite > 0 {
			put("cacheWrite", text("W"+StatusTokens(d.CacheWrite), Muted))
		}
		if d.Cost != "" {
			put("cost", text(d.Cost, Muted))
		}
		put("path", text(d.Path, ""))
		put("branch", text(d.Branch, ""))
	}
	if d.Goal != "" {
		put("goal", text(d.Goal, Accent))
	}
	plural := func(n int) string {
		if n == 1 {
			return ""
		}
		return "s"
	}
	if d.Jobs > 0 {
		put("jobs", text(fmt.Sprintf("● %d job%s running (/jobs)", d.Jobs, plural(d.Jobs)), Success))
	}
	if d.Timers > 0 {
		put("timers", text(fmt.Sprintf("⏱ %d timer%s (/timers)", d.Timers, plural(d.Timers)), Accent))
	}
	return out
}

func StatusTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10000:
		return strings.Replace(fmt.Sprintf("%.1fk", float64(n)/1e3), ".0k", "k", 1)
	case n < 1000000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	case n < 10000000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	default:
		return fmt.Sprintf("%dM", (n+500000)/1000000)
	}
}
