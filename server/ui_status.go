package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/ui"
)

func (t *thread) initUIStatus() {
	r := t.uiRegistry()
	t.refreshUIStatus()
	for _, id := range ui.StatusIDs {
		m := ui.Match{Site: ui.Status, ID: "atto/" + id}
		r.Render("atto", m, func(ui.Event, ui.Next) (*ui.Node, error) { return t.statusTree(id), nil })
		_ = r.Open("atto", ui.OpenOptions{Site: ui.Status, ID: m.ID})
	}
}
func (t *thread) statusTree(id string) *ui.Node {
	info := t.info()
	d := ui.StatusData{Model: info.ModelName, ContextTokens: info.ContextTokens, ContextWindow: info.ContextWindow,
		CompactLimit: info.AutoCompactLimit, CompactCap: info.AutoCompactCap, Long: info.LongContext,
		Path: core.ShortPath(t.cwd), Branch: session.GitBranch(t.cwd), Jobs: info.Jobs, Timers: info.Timers,
		Custom: t.customStatusConfigured, CustomLines: t.customStatusLines}
	if len(info.Efforts) > 0 {
		d.Effort = info.Effort
	}
	if info.Goal != nil {
		d.Goal = info.Goal.Indicator
	}
	if u := info.Usage; u != nil {
		d.Fresh = max(0, u.InputTokens-u.CachedInputTokens-u.CacheWriteTokens)
		d.Output, d.CacheWrite = u.OutputTokens, u.CacheWriteTokens
		d.LastInput, d.LastCached = u.LastInputTokens, u.LastCachedInputTokens
		if u.Last != nil {
			d.LastInput, d.LastCached = u.Last.PromptTokens, u.Last.CachedTokens
		}
		if info.Priced || u.Cost > 0 {
			d.Cost = fmt.Sprintf("$%.3f", u.Cost)
			cost := u.LastCost
			if cost == nil {
				cost = t.model().Model.Cost
			}
			if multiplier := cost.InputMultiplier(d.LastInput); multiplier > 1 {
				d.Cost += fmt.Sprintf(" ×%.0f", multiplier)
			}
			if info.Subscription {
				d.Cost = "≈" + d.Cost
			}
		}
	}
	return ui.StatusTrees(d)[id]
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
