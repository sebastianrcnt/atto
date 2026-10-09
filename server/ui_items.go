package server

import (
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/ui"
	"strings"
)

func itemUISite(w Item) ui.Site {
	switch w.Type {
	case ItemUser:
		return ui.UserMessage
	case ItemAgent, ItemReasoning:
		return ui.AssistantMessage
	case ItemCommand:
		return ui.ToolCall
	case ItemNotice:
		return ui.Notice
	}
	return ""
}
func (t *thread) drawUIItem(w Item) Item {
	site := itemUISite(w)
	if site == "" || t.elements == nil || !t.elements.HasRenderer(ui.Match{Site: site, ID: w.ID}) {
		return w
	}
	props := map[string]any{"itemId": w.ID, "status": w.Status}
	if w.EntryID != "" {
		props["entryId"] = w.EntryID
	}
	switch site {
	case ui.UserMessage:
		props["text"] = w.Text
		props["images"] = w.Images
		if w.ClientID != "" {
			props["clientId"] = w.ClientID
		}
		if w.InputID != "" {
			props["inputId"] = w.InputID
		}
	case ui.AssistantMessage:
		props["text"] = w.Text
		kind := "answer"
		if w.Type == ItemReasoning {
			kind = "reasoning"
		}
		props["kind"] = kind
		if w.BlockID != "" {
			props["blockId"] = w.BlockID
		}
	case ui.ToolCall:
		props["command"], props["description"], props["output"] = w.Command, w.Description, w.Output
		props["durationMs"], props["background"], props["shell"], props["images"] = w.DurationMs, w.Background, w.Shell, w.Images
		if w.CallID != "" {
			props["callId"] = w.CallID
		}
		if w.ExitCode != nil {
			props["exitCode"] = *w.ExitCode
		}
		if w.Job > 0 {
			props["job"] = w.Job
		}
	case ui.Notice:
		props["text"], props["level"] = w.Text, w.Level
		if w.Title != "" {
			props["title"] = w.Title
		}
	}
	if (site == ui.UserMessage || site == ui.ToolCall) && len(w.Images) == 0 {
		props["images"] = []any{}
	}
	tree, rev, err := t.elements.DrawItem(site, w.ID, props)
	if err != nil {
		return w
	}
	w.UIDisplay = &UIDisplay{Rev: rev, Tree: tree, ActionsEnabled: t.elements.Bound(site, w.ID)}
	return w
}
func (t *thread) persistUIItem(w Item) {
	if w.UIDisplay == nil || w.EntryID == "" || strings.HasPrefix(w.EntryID, "n") || w.Status == "inProgress" {
		return
	}
	kind := ""
	if w.Type == ItemAgent {
		kind = session.BlockText
	}
	if w.Type == ItemReasoning {
		kind = session.BlockReasoning
	}
	t.sess.Append(session.Entry{Type: session.TypeUIItemDisplay, TargetID: w.EntryID, Block: kind, UICallID: w.CallID, UISite: itemUISite(w), UIID: w.ID, UIRev: w.UIDisplay.Rev, UITree: w.UIDisplay.Tree})
}
