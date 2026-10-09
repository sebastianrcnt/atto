package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"runtime"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/ui"
)

var statusSlots = []struct {
	id       string
	priority int
	align    string
}{{"model", 90, "start"}, {"effort", 30, "start"}, {"context", 85, "start"}, {"cache", 20, "start"}, {"tokens", 15, "start"}, {"cost", 10, "start"}, {"path", 5, "end"}, {"memory", 1, "end"}, {"goal", 100, "end"}, {"activity", 100, "start"}, {"jobs", 60, "start"}, {"timers", 50, "start"}, {"custom", 95, "start"}}

func (t *thread) initUIStatus() {
	r := t.uiRegistry()
	for _, slot := range statusSlots {
		id := slot.id
		m := ui.Match{Site: ui.Status, ID: "atto/" + id}
		r.Render("atto", m, func(ui.Event, ui.Next) (*ui.Node, error) { return t.statusTree(id), nil })
		_ = r.Open("atto", ui.OpenOptions{Site: ui.Status, ID: m.ID, Priority: slot.priority, Align: slot.align})
	}
	t.refreshUIStatus()
}
func (t *thread) statusTree(id string) *ui.Node {
	info := t.info()
	text := ""
	color := ui.ColorText
	if t.customStatusConfigured && id != "goal" && id != "activity" && id != "jobs" && id != "timers" && id != "custom" {
		return nil
	}
	u := info.Usage
	switch id {
	case "model":
		text = "◆ " + info.ModelName
		if info.ModelName == "" {
			text = "◆ no model (/login)"
		}
		color = ui.Accent
	case "effort":
		text = info.Effort
		color = ui.Muted
	case "context":
		if info.ContextWindow > 0 {
			pct := info.ContextTokens * 100 / info.ContextWindow
			filled := min(10, (pct*10+50)/100)
			text = fmt.Sprintf("%s%s %d%% %s/%s", strings.Repeat("━", filled), strings.Repeat("─", 10-filled), pct, statusTokens(info.ContextTokens), statusTokens(info.ContextWindow))
			if info.LongContext {
				text += " long"
			}
			if info.AutoCompactLimit > 0 && info.ContextTokens*100/info.AutoCompactLimit >= 80 {
				color = ui.Warning
			} else {
				color = ui.Muted
			}
		}
	case "cache":
		if u != nil && u.LastInputTokens > 0 {
			text = fmt.Sprintf("cache %d%%", u.LastCachedInputTokens*100/u.LastInputTokens)
		}
		color = ui.Muted
	case "tokens":
		if u != nil && (u.InputTokens-u.CachedInputTokens > 0 || u.OutputTokens > 0) {
			text = fmt.Sprintf("↑%s ↓%s", statusTokens(max(0, u.InputTokens-u.CachedInputTokens)), statusTokens(u.OutputTokens))
		}
		color = ui.Muted
	case "cost":
		if u != nil && (info.Priced || u.Cost > 0) {
			text = fmt.Sprintf("$%.3f", u.Cost)
			if info.Subscription {
				text = "≈" + text
			}
		}
		color = ui.Muted
	case "path":
		text = core.ShortPath(t.cwd)
		if branch := session.GitBranch(t.cwd); branch != "" {
			text += " (" + branch + ")"
		}
		color = ui.Muted
	case "memory":
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		text = fmt.Sprintf("%dMB", m.HeapInuse>>20)
		color = ui.Muted
	case "goal":
		if info.Goal != nil {
			text = info.Goal.Indicator
		}
		color = ui.Accent
	case "activity":
		if info.Busy {
			text = defaultActivity(info.Activity)
		}
		color = ui.Accent
	case "jobs":
		if info.Jobs > 0 {
			text = fmt.Sprintf("● %d job%s running (/jobs)", info.Jobs, statusPlural(info.Jobs))
		}
		color = ui.Success
	case "timers":
		if info.Timers > 0 {
			text = fmt.Sprintf("⏱ %d timer%s (/timers)", info.Timers, statusPlural(info.Timers))
		}
		color = ui.Accent
	case "custom":
		if t.customStatusConfigured {
			n := ui.ThemedText(strings.Join(t.customStatusLines, " · "))
			return &n
		}
	}
	if text == "" {
		return nil
	}
	n := ui.Text(ui.TextProps{Color: color, Text: ui.CleanText(text), Wrap: "truncate", MaxLines: 1})
	return &n
}
func defaultActivity(a *Activity) string {
	if a == nil {
		return "Working…"
	}
	return a.Phase + "…"
}
func statusPlural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
func statusTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10000:
		return strings.Replace(fmt.Sprintf("%.1fk", float64(n)/1000), ".0k", "k", 1)
	case n < 1000000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
}

// refreshUIStatus executes a custom command off the lane, once per session,
// sharing its result and refresh cache across every observing client.
func (t *thread) refreshUIStatus() {
	if t.closing || t.statusUIBusy || time.Since(t.statusUICheck) < time.Second {
		return
	}
	t.statusUICheck = time.Now()
	out, err := t.statusLine()
	if err != nil {
		return
	}
	request, ok := out.(statusLineRequest)
	if !ok {
		if t.customStatusConfigured {
			t.customStatusConfigured = false
			t.uiRegistry().Invalidate(ui.Match{Site: ui.Status})
		}
		return
	}
	t.customStatusConfigured = true
	input := copyStatusInput(request.input)
	delete(input, "memory")
	encoded, _ := json.Marshal(input)
	refresh := time.Duration(request.refresh) * time.Second
	if string(encoded) == t.statusUIInput && (refresh <= 0 || time.Since(t.statusUIRan) < refresh) {
		return
	}
	t.statusUIInput = string(encoded)
	t.statusUIBusy = true
	t.statusUIRan = time.Now()
	go func() {
		out, err := runStatusLine(context.Background(), request)
		t.do(func() {
			t.statusUIBusy = false
			if t.closing {
				return
			}
			if err != nil {
				t.customStatusLines = []string{"statusLine: " + err.Error()}
			} else if m, ok := out.(map[string]any); ok {
				t.customStatusLines, _ = m["lines"].([]string)
			}
			t.uiRegistry().Invalidate(ui.Match{Site: ui.Status})
		})
	}()
}
func copyStatusInput(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}
