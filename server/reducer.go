package server

import (
	"encoding/json"
	"slices"
)

// ThreadView is a client's copy of a thread's state: a snapshot (thread/read,
// thread/resume or thread/attach) brought up to date by notifications.
// Notifications of other threads, and those the snapshot already has
// (their event ID is at most the snapshot's), are ignored, so a client
// that reads while events arrive applies each change exactly once.
type ThreadView struct {
	Info    ThreadInfo // Items unused: see Items
	Items   []Item
	EventID int64 // the snapshot's cursor
	index   map[string]int
}

// Reset replaces the view with snapshot info.
func (v *ThreadView) Reset(info ThreadInfo) {
	v.Items = slices.Clone(info.Items)
	info.Items = nil
	v.Info, v.EventID = info, info.EventID
	v.index = map[string]int{}
	for i, it := range v.Items {
		v.index[it.ID] = i
	}
}

// Apply brings the view up to date with n; it reports whether n changed it.
func (v *ThreadView) Apply(n Notification) bool {
	if n.EventID != 0 && n.EventID <= v.EventID {
		return false
	}
	if id := n.ThreadID(); id != "" && id != v.Info.ID {
		return false
	}
	var p struct {
		Item    *Item         `json:"item"`
		ItemID  string        `json:"itemId"`
		Delta   string        `json:"delta"`
		Display *BlockDisplay `json:"display"`
		Thread  *ThreadInfo   `json:"thread"`
		TurnID  string        `json:"turnId"`
		Usage   *Usage        `json:"usage"`
		Context *int          `json:"contextTokens"`
		Pending *PendingInput `json:"pending"`
		Goal    *GoalInfo     `json:"goal"`
		UI      *ExtensionUI  `json:"ui"`
		Prompt  *Prompt       `json:"prompt"`
		ID      string        `json:"id"`
	}
	if json.Unmarshal(n.Params, &p) != nil {
		return false
	}
	switch n.Method {
	case "item/started", "item/updated", "item/completed":
		if p.Item == nil {
			return false
		}
		if i, ok := v.index[p.Item.ID]; ok {
			v.Items[i] = *p.Item
		} else {
			if v.index == nil {
				v.index = map[string]int{}
			}
			v.index[p.Item.ID] = len(v.Items)
			v.Items = append(v.Items, *p.Item)
		}
	case "item/delta":
		i, ok := v.index[p.ItemID]
		if !ok {
			return false
		}
		if v.Items[i].Type == ItemCommand {
			v.Items[i].Output += p.Delta
		} else {
			v.Items[i].Text += p.Delta
		}
	case "item/display":
		i, ok := v.index[p.ItemID]
		if !ok {
			return false
		}
		v.Items[i].Display = p.Display
	case "thread/updated":
		if p.Thread == nil {
			return false
		}
		t := *p.Thread
		t.Items = nil
		v.Info = t
	case "turn/started":
		v.Info.Busy, v.Info.TurnID = true, p.TurnID
	case "turn/completed":
		v.Info.Busy, v.Info.TurnID, v.Info.Turn = false, "", nil
		if p.Context != nil {
			v.Info.ContextTokens = *p.Context
		}
	case "thread/usage":
		v.Info.Usage = p.Usage
		if p.Context != nil {
			v.Info.ContextTokens = *p.Context
		}
	case "turn/pending":
		v.Info.Pending = p.Pending
	case "goal/updated":
		v.Info.Goal = p.Goal
	case "extension/ui":
		v.Info.ExtensionUI = p.UI
	case "prompt/open":
		v.Info.Prompt = p.Prompt
	case "prompt/closed":
		if v.Info.Prompt != nil && v.Info.Prompt.ID == p.ID {
			v.Info.Prompt = nil
		}
	default:
		return false
	}
	if n.EventID != 0 {
		v.EventID = n.EventID
	}
	return true
}
