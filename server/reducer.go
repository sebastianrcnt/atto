package server

import (
	"encoding/json"
	"github.com/sebastianrcnt/atto/ui"
	"slices"

	"github.com/sebastianrcnt/atto/core"
)

// ThreadView is a client's copy of a thread's state: a snapshot (thread/read,
// thread/resume or thread/attach) brought up to date by notifications.
// Notifications of other threads, and those the snapshot already has
// (their event ID is at most the snapshot's), are ignored, so a client
// that reads while events arrive applies each change exactly once.
type ThreadView struct {
	Info          ThreadInfo // Items unused: see Items
	Items         []Item
	EventID       int64 // the snapshot's cursor
	index         map[string]int
	uiRevs        map[ui.Match]int64
	NeedsSnapshot bool // replay reset or branch change requires thread/read
}

// Reset replaces the view with snapshot info.
func (v *ThreadView) Reset(info ThreadInfo) {
	v.Items = slices.Clone(info.Items)
	info.Items = nil
	v.Info, v.EventID, v.NeedsSnapshot = info, info.EventID, false
	v.uiRevs = map[ui.Match]int64{}
	if info.UI != nil {
		for _, i := range info.UI.Instances {
			v.uiRevs[ui.Match{Site: i.Site, ID: i.ID}] = i.Rev
		}
	}
	v.index = map[string]int{}
	for i, it := range v.Items {
		v.index[it.ID] = i
	}
}

// Apply brings the view up to date with n; it reports whether n changed it.
func (v *ThreadView) Apply(n Notification) bool {
	if n.Method == "events/reset" {
		v.NeedsSnapshot = true
		return true
	}
	if v.NeedsSnapshot {
		return false
	}
	if n.EventID != 0 && n.EventID <= v.EventID {
		return false
	}
	if id := n.ThreadID(); id != "" && id != v.Info.ID {
		return false
	}
	var p struct {
		Item      *Item           `json:"item"`
		ItemID    string          `json:"itemId"`
		Delta     string          `json:"delta"`
		Display   *BlockDisplay   `json:"display"`
		Thread    *ThreadInfo     `json:"thread"`
		TurnID    string          `json:"turnId"`
		Usage     *Usage          `json:"usage"`
		Context   *int            `json:"contextTokens"`
		Pending   *PendingInput   `json:"pending"`
		Goal      *GoalInfo       `json:"goal"`
		UI        *ExtensionUI    `json:"ui"`
		Prompt    *Prompt         `json:"prompt"`
		ID        string          `json:"id"`
		Activity  json.RawMessage `json:"activity"`
		StartedAt int64           `json:"startedAt"`
		RunKind   string          `json:"runKind"`
		Verb      string          `json:"verb"`
		Jobs      int             `json:"jobs"`
		Timers    int             `json:"timers"`
		Loaded    *core.Loaded    `json:"context"`
	}
	if json.Unmarshal(n.Params, &p) != nil {
		return false
	}
	switch n.Method {
	case "ui/open", "ui/render", "ui/close":
		var update struct {
			ui.Instance
			FocusClientID string `json:"focusClientId"`
		}
		if json.Unmarshal(n.Params, &update) != nil {
			return false
		}
		m := ui.Match{Site: update.Site, ID: update.ID}
		if update.Rev <= v.uiRevs[m] {
			return false
		}
		v.uiRevs[m] = update.Rev
		if v.Info.UI == nil {
			v.Info.UI = &ui.Snapshot{Version: 1, Instances: []ui.Instance{}}
		}
		instances := v.Info.UI.Instances
		found := -1
		for i, x := range instances {
			if x.Site == m.Site && x.ID == m.ID {
				found = i
				break
			}
		}
		if n.Method == "ui/close" {
			if found >= 0 {
				instances = append(instances[:found], instances[found+1:]...)
			}
		} else if found >= 0 {
			instances[found].Rev = update.Rev
			if n.Method == "ui/open" {
				instances[found].Options = update.Options
			} else {
				instances[found].Tree = update.Tree
			}
		} else if !ui.IsItem(m.Site) && m.Site != ui.Transcript {
			instances = append(instances, update.Instance)
		}
		v.Info.UI.Instances = instances
		if ui.IsItem(update.Site) && n.Method == "ui/render" {
			if i, ok := v.index[update.ID]; ok {
				v.Items[i].UIDisplay = &UIDisplay{Rev: update.Rev, Tree: update.Tree}
			}
		}

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
			it := &v.Items[i]
			it.Output += p.Delta
			// Match the transcript builder's live output bound. Completed
			// items supply the final tidy output and full-output reference.
			const tail = 64 * 1024
			if len(it.Output) > 2*tail {
				cut := len(it.Output) - tail
				it.Dropped += cut
				it.Output = it.Output[cut:]
			}
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
		t.HasMore, t.Before = v.Info.HasMore, v.Info.Before
		t.UI = v.Info.UI
		v.Info = t
	case "turn/started":
		v.Info.Busy, v.Info.TurnID, v.Info.RunKind = true, p.TurnID, p.RunKind
		v.Info.Turn = &TurnInfo{StartedAt: p.StartedAt, Verb: p.Verb}
	case "turn/completed":
		v.Info.Busy, v.Info.TurnID, v.Info.Turn, v.Info.Activity, v.Info.RunKind = false, "", nil, nil, ""
		if p.Context != nil {
			v.Info.ContextTokens = *p.Context
		}
	case "turn/activity":
		var activity *Activity
		if json.Unmarshal(p.Activity, &activity) != nil {
			return false
		}
		v.Info.Activity = activity
	case "thread/status":
		v.Info.Jobs, v.Info.Timers = p.Jobs, p.Timers
	case "thread/reloaded":
		if p.Loaded != nil {
			v.Info.Context = p.Loaded
		}
	case "thread/branchChanged", "thread/closed":
		v.NeedsSnapshot = true
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

// Prepend merges an earlier page without moving the live-event boundary or
// replacing an item already updated by live notifications.
func (v *ThreadView) Prepend(page ItemPage) []Item {
	var added []Item
	for _, it := range page.Items {
		if _, ok := v.index[it.ID]; !ok {
			added = append(added, it)
		}
	}
	v.Items = append(added, v.Items...)
	v.index = make(map[string]int, len(v.Items))
	for i, it := range v.Items {
		v.index[it.ID] = i
	}
	v.Info.HasMore, v.Info.Before = page.HasMore, page.Before
	return added
}
