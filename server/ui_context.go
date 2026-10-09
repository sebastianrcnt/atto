package server

import (
	"fmt"
	"github.com/sebastianrcnt/atto/ui"
)

func contextTokens(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// ContextTree is the passive context card used by both clients. Diagnostics and
// the system prompt remain native; no credentials or private login flows enter it.
func ContextTree(c ContextInfo) ui.Node {
	head := "Context · " + contextTokens(c.ContextTokens) + " tokens"
	if c.ContextWindow > 0 {
		head += fmt.Sprintf(" of %s (%d%%)", contextTokens(c.ContextWindow), c.ContextTokens*100/c.ContextWindow)
	}
	children := []ui.Node{ui.Text(ui.TextProps{Text: head, Bold: true})}
	add := func(s string) { children = append(children, ui.Text(ui.TextProps{Color: ui.Muted, Text: s})) }
	if c.CompactLimit > 0 {
		add("Auto-compacts at " + contextTokens(c.CompactLimit))
	}
	if c.Cap > 0 {
		if c.PriceCap {
			add(c.Loaded.Model.Name + " costs more above " + contextTokens(c.Cap) + " input tokens.")
		} else {
			add("Cap set by settings.json compaction.limits.")
		}
		add("/context long to allow more.")
	} else if c.LongContext {
		add("Long context; /context normal to restore the tier cap.")
	}
	if c.tierNote != "" {
		add(c.tierNote)
	}
	if b := c.Breakdown; c.Busy || b == nil {
		add("Breakdown is available when the turn finishes.")
	} else {
		rows := []ui.Row{}
		total := max(b.Total(), 1)
		for index, r := range []struct {
			name  string
			chars int
		}{{"system prompt", b.System}, {"tool schema", b.Tools}, {"user messages", b.User}, {"images", b.Images}, {"handoff notes", b.Notes}, {"assistant text", b.Assistant}, {"reasoning", b.Reasoning}, {"tool calls", b.ToolCalls}, {"tool results", b.ToolResults}} {
			if r.chars > 0 {
				rows = append(rows, ui.Row{Key: fmt.Sprint(index), Cells: []string{r.name, "~" + contextTokens(r.chars/4), fmt.Sprintf("%d%%", r.chars*100/total)}})
			}
		}
		children = append(children, ui.List(ui.ListProps{Mode: "table", Columns: []ui.Column{{Label: "Context"}, {Label: "Tokens"}, {Label: "Share"}}, Rows: rows}))
		add(fmt.Sprintf("%d messages · sizes estimated at 4 characters per token", b.Messages))
	}
	u := c.Usage
	if u.Last != nil {
		add(fmt.Sprintf("Last request · %s input · %s cached · %s output", contextTokens(u.Last.PromptTokens), contextTokens(u.Last.CachedTokens), contextTokens(u.Last.CompletionTokens)))
	}
	add(fmt.Sprintf("This session · %s input · %s cached · %s output · $%.4f", contextTokens(u.InputTokens), contextTokens(u.CachedInputTokens), contextTokens(u.OutputTokens), u.Cost))
	add("/context system shows the system prompt · /request saves the raw last request")
	return ui.Box(ui.BoxProps{}, children...)
}
func (t *thread) openContextPane(client string) {
	n := ContextTree(t.contextInfo(""))
	_ = t.uiRegistry().OpenDefault("atto", ui.OpenOptions{Site: ui.Pane, ID: "atto/context", Title: "Context", FocusClientID: client}, nil, &n)
}
