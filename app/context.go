package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// usageStats tracks token usage for the status line and /context.
type usageStats struct {
	lastCost              *ai.ModelCost
	last                  provider.Usage
	input, cached, output int // session totals; input includes cached and written
	cacheWrite            int // session total of tokens written to the cache
	cost                  float64
}

func (u *usageStats) add(x provider.Usage) {
	u.last = x
	u.input += x.PromptTokens
	u.cached += x.CachedTokens
	u.output += x.CompletionTokens
	u.cacheWrite += x.CacheWriteTokens
	u.cost += x.Cost
}

// fresh is the session's input that was neither read from nor written to
// the cache: pi's "↑".
func (u *usageStats) fresh() int { return max(0, u.input-u.cached-u.cacheWrite) }

// fromEntries rebuilds totals from a resumed session.
func (u *usageStats) fromEntries(entries []session.Entry, models config.ModelsFile) {
	*u = usageStats{}
	var model config.ModelRef
	for _, e := range entries {
		if e.Type == session.TypeModel {
			model, _ = models.Find(e.Provider, e.Model)
		}
		if e.Usage != nil {
			u.add(*e.Usage)
			u.lastCost = model.Model.Cost
		}
	}
}

// fromSaved uses the totals accumulated without decoding old messages.
func (u *usageStats) fromSaved(saved core.Saved, models config.ModelsFile) {
	*u = usageStats{}
	u.add(saved.Usage)
	u.last = saved.LastUsage
	model, _ := models.Find("", saved.UsageModel)
	u.lastCost = model.Model.Cost
}

func pct(part, whole int) int {
	if whole <= 0 {
		return 0
	}
	return part * 100 / whole
}

// cacheLabel is "cache 93%" for the last request, or "" before any.
func (u *usageStats) cacheLabel() string {
	if u.last.PromptTokens == 0 {
		return ""
	}
	return fmt.Sprintf("cache %d%%", pct(u.last.CachedTokens, u.last.PromptTokens))
}

// contextBlock is the /context report. Its lines are set when it is made.
type contextBlock struct {
	lines []string
	cache tui.RenderCache[struct{}]
}

func (c *contextBlock) Render(width int) []string {
	return c.cache.Render(width, struct{}{}, func() []string {
		out := make([]string, len(c.lines))
		for i, l := range c.lines {
			out[i] = tui.Truncate("  "+l, width, "…")
		}
		return out
	})
}

func (a *App) cmdContext(arg string) {
	if arg == "long" || arg == "normal" {
		if a.refuseReadOnly("/context " + arg) {
			return
		}
		a.agent.SetLongContext(arg == "long")
		a.sess.Append(session.Entry{Type: session.TypeContext, LongContext: arg == "long"})
		a.statusTrigger()
	} else if arg != "" && arg != "system" {
		a.notice("Usage: /context [system|long|normal]")
		return
	}
	if arg == "system" {
		a.add(&noticeBlock{text: "System prompt:\n\n" + a.agent.SystemPrompt(), style: tui.Dim})
		return
	}
	m := a.model()
	var lines []string
	head := fmt.Sprintf("%s · %s tokens", tui.Bold("Context"), tui.FormatTokens(a.ctxTokens))
	if cw := m.Model.ContextWindow; cw > 0 {
		head += fmt.Sprintf(" of %s (%d%%)", tui.FormatTokens(cw), pct(a.ctxTokens, cw))
	}
	lines = append(lines, head)
	if limit, cap := a.agent.CompactionLimit(); limit > 0 {
		lines = append(lines, tui.Dim("Auto-compacts at "+tui.FormatTokens(limit)))
		if cap > 0 {
			if cap == m.Model.Cost.ContextPriceBoundary() {
				lines = append(lines, tui.Dim(fmt.Sprintf("%s costs more above %s input tokens.", m.Model.DisplayName(), tui.FormatTokens(cap))))
			} else {
				lines = append(lines, tui.Dim("Cap set by settings.json compaction.limits."))
			}
			lines = append(lines, tui.Dim("/context long to allow more."))
		} else if a.agent.LongContext() {
			lines = append(lines, tui.Dim("Long context; /context normal to restore the tier cap."))
		}
	}

	if m.Model.Cost.ContextPriceBoundary() == 0 {
		reason := "no tier data known for this model"
		if m.Model.Cost != nil && len(m.Model.Cost.Tiers) > 0 {
			reason = "known tiers do not increase the input price"
		}
		lines = append(lines, tui.Dim("No tier cap: "+reason+"."))
	}

	if a.turns.Busy {
		lines = append(lines, tui.Dim("Breakdown is available when the turn finishes."))
	} else {
		b := a.agent.Breakdown()
		total := max(b.Total(), 1)
		rows := []struct {
			name  string
			chars int
		}{
			{"system prompt", b.System}, {"tool schema", b.Tools}, {"user messages", b.User}, {"images", b.Images},
			{"handoff notes", b.Notes}, {"assistant text", b.Assistant}, {"reasoning", b.Reasoning},
			{"tool calls", b.ToolCalls}, {"tool results", b.ToolResults},
		}
		for _, r := range rows {
			if r.chars == 0 {
				continue
			}
			p := pct(r.chars, total)
			lines = append(lines, fmt.Sprintf("  %-15s %s %6s %3d%%", r.name, contextBar(p, 20), "~"+tui.FormatTokens(r.chars/4), p))
		}
		lines = append(lines, tui.Dim(fmt.Sprintf("  %d messages · sizes estimated at 4 characters per token", b.Messages)))
	}

	u := a.usage
	if u.last.PromptTokens > 0 {
		lines = append(lines, "", fmt.Sprintf("Last request   %s input · %s cached (%d%%) · %s output",
			tui.FormatTokens(u.last.PromptTokens), tui.FormatTokens(u.last.CachedTokens), pct(u.last.CachedTokens, u.last.PromptTokens), tui.FormatTokens(u.last.CompletionTokens)))
		lines = append(lines, fmt.Sprintf("This session   %s input · %s cached (%d%%) · %s output",
			tui.FormatTokens(u.input), tui.FormatTokens(u.cached), pct(u.cached, u.input), tui.FormatTokens(u.output)))
	}
	lines = append(lines, "", tui.Dim("/context system shows the system prompt · /request saves the raw last request"))
	a.add(&contextBlock{lines: lines})
}

// cmdRequest saves the last request body sent to the model, pretty-printed.
func (a *App) cmdRequest(string) {
	body := a.agent.LastRequest()
	if body == nil {
		a.notice("No request has been sent yet.")
		return
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") != nil {
		pretty.Write(body)
	}
	path := filepath.Join(config.Dir(), "cache", "last-request.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		a.errorNotice(err)
		return
	}
	if err := os.WriteFile(path, pretty.Bytes(), 0o600); err != nil {
		a.errorNotice(err)
		return
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(body, &req)
	a.notice("Saved the last request (%d messages, %d tools, %s) to %s",
		len(req.Messages), len(req.Tools), fmtBytes(int64(len(body))), shortPath(path))
}

// priceTierNotice is shown on model selection, not on every turn or status update.
func (a *App) priceTierNotice() {
	if notice := config.PriceTierNotice(a.model()); notice != "" {
		a.notice("%s", notice)
	}
}
