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
	"github.com/sebastianrcnt/atto/server"
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

// cmdContext is /context: the report is the terminal's, its data the
// runtime's (thread/context). long and normal change the context mode.
func (a *App) cmdContext(arg string) bool {
	if arg != "" && arg != "system" && arg != "long" && arg != "normal" {
		a.notice("Usage: /context [system|long|normal]")
		return true
	}
	if arg == "long" || arg == "normal" {
		if a.refuseReadOnly("/context " + arg) {
			return true
		}
		a.info.LongContext = arg == "long"
		a.rpcErr("thread/setContextMode", map[string]any{"contextMode": arg})
	}
	a.rpc("thread/context", map[string]any{"view": arg}, func(raw json.RawMessage, err error) {
		if err != nil {
			a.errorNotice(err)
			return
		}
		var c server.ContextInfo
		if json.Unmarshal(raw, &c) == nil {
			a.showContext(c, arg == "system")
		}
	})
	return true
}

// showContext adds the /context report.
func (a *App) showContext(c server.ContextInfo, system bool) {
	if system {
		a.add(&noticeBlock{text: "System prompt:\n\n" + c.System, style: tui.Dim})
		return
	}
	m := a.model()
	ctx := c.ContextTokens
	var lines []string
	head := fmt.Sprintf("%s · %s tokens", tui.Bold("Context"), tui.FormatTokens(ctx))
	if cw := c.ContextWindow; cw > 0 {
		head += fmt.Sprintf(" of %s (%d%%)", tui.FormatTokens(cw), pct(ctx, cw))
	}
	lines = append(lines, head)
	if limit, cap := c.CompactLimit, c.Cap; limit > 0 {
		lines = append(lines, tui.Dim("Auto-compacts at "+tui.FormatTokens(limit)))
		if cap > 0 {
			if c.PriceCap {
				lines = append(lines, tui.Dim(fmt.Sprintf("%s costs more above %s input tokens.", m.Model.DisplayName(), tui.FormatTokens(cap))))
			} else {
				lines = append(lines, tui.Dim("Cap set by settings.json compaction.limits."))
			}
			lines = append(lines, tui.Dim("/context long to allow more."))
		} else if c.LongContext {
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
	if b := c.Breakdown; c.Busy || b == nil {
		lines = append(lines, tui.Dim("Breakdown is available when the turn finishes."))
	} else {
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
	a.rpc("thread/debugRequest", nil, func(raw json.RawMessage, err error) {
		if err != nil {
			a.errorNotice(err)
			return
		}
		var r struct {
			Request string `json:"request"`
		}
		_ = json.Unmarshal(raw, &r)
		if r.Request == "" {
			a.notice("No request has been sent yet.")
			return
		}
		a.saveRequest([]byte(r.Request))
	})
}

func (a *App) saveRequest(body []byte) {
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
		len(req.Messages), len(req.Tools), fmtBytes(int64(len(body))), core.ShortPath(path))
}
