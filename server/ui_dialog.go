package server

import (
	"context"
	"github.com/sebastianrcnt/atto/ui"
	"strconv"
)

func promptTree(p Prompt) ui.Node {
	title := p.Title
	if p.Subtitle != "" {
		title += "  " + p.Subtitle
	}
	children := []ui.Node{ui.Text(ui.TextProps{Text: title, Bold: true})}
	if p.Kind == PromptInput {
		children = append(children, ui.Input(ui.InputProps{Key: "answer", AutoFocus: true, Value: p.Text}))
	} else {
		var options []ui.Option
		for i, o := range p.Options {
			options = append(options, ui.Option{Value: strconv.Itoa(i), Label: o.Label, Description: o.Description})
		}
		selected := strconv.Itoa(p.Selected)
		children = append(children, ui.Select(ui.SelectProps{Key: "answer", AutoFocus: true, Options: options, Value: &selected}))
	}
	return ui.Box(ui.BoxProps{Gap: 1}, children...)
}
func portablePrompt(p *openPrompt) bool {
	return p != nil && p.wire.Kind != PromptCustom && p.origin != "mcp" && p.origin != "client" && p.wire.Kind != PromptMultiSelect && (p.wire.Kind != PromptSelect || len(p.wire.Options) > 0)
}
func (t *thread) openUIDialog(p *openPrompt) {
	if !portablePrompt(p) {
		return
	}
	m := ui.Match{Site: ui.Dialog, ID: "atto/" + p.wire.ID}
	r := t.uiRegistry()
	kind := ui.SelectEvent
	if p.wire.Kind == PromptInput {
		kind = ui.Submit
	}
	answer := func(ctx context.Context, a ui.Action) error {
		ans := PromptAnswer{}
		if kind == ui.Submit {
			ans.Text = a.Value
		} else {
			index, err := strconv.Atoi(*a.Value)
			if err != nil {
				return err
			}
			ans.Index = &index
		}
		return t.answerPrompt(a.ClientID, p.wire.ID, ans)
	}
	r.Bind("atto", m, "answer", kind, answer)
	r.Bind("atto", m, "$site", ui.CloseEvent, func(ctx context.Context, a ui.Action) error {
		return t.answerPrompt(a.ClientID, p.wire.ID, PromptAnswer{Cancel: true})
	})
	tree := promptTree(p.wire)
	kindName := "select"
	if p.wire.Confirm {
		kindName = "confirm"
	}
	if p.wire.Kind == PromptInput {
		kindName = "input"
	}
	options := make([]map[string]any, 0, len(p.wire.Options))
	for index, opt := range p.wire.Options {
		options = append(options, map[string]any{"value": strconv.Itoa(index), "label": opt.Label})
	}
	props := map[string]any{"kind": kindName, "title": p.wire.Title, "options": options, "initialValue": p.wire.Text}
	_ = r.OpenDefault("atto", ui.OpenOptions{Site: ui.Dialog, ID: m.ID, Title: p.wire.Title, CloseOnEscape: true}, props, &tree)
}
func (t *thread) closeUIDialog(p *openPrompt, how string) {
	if p != nil && p.wire.UIID != "" && t.elements != nil {
		_ = t.elements.CloseReason(p.ext, ui.Dialog, p.wire.UIID, "provider")
		return
	}
	if !portablePrompt(p) || t.elements == nil {
		return
	}
	reason := "answered"
	if how != "answered" {
		reason = "provider"
	}
	_ = t.elements.CloseReason("atto", ui.Dialog, "atto/"+p.wire.ID, reason)
}

// DialogTree is the portable default drawing for a broker helper.
func DialogTree(p Prompt) ui.Node { return promptTree(p) }
