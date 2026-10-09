package server

import (
	"encoding/json"
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
	b, _ := json.Marshal(w)
	var props map[string]any
	_ = json.Unmarshal(b, &props)
	props["itemId"] = w.ID
	if site == ui.AssistantMessage {
		kind := "answer"
		if w.Type == ItemReasoning {
			kind = "reasoning"
		}
		props["kind"] = kind
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
