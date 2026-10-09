package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/ui"
	"log"
	"strings"
	"time"
)

// uiRegistry is session-owned and all publications run on the worker lane.
func (t *thread) uiRegistry() *ui.Registry {
	if t.elements == nil {
		t.elements = ui.NewRegistry(t.publishUI, func(fn func()) { t.do(fn) })
		t.elements.Enqueue = func(ctx context.Context, fn ui.Handler, a ui.Action) {
			t.do(func() {
				if t.closing {
					return
				}
				if err := fn(ctx, a); err != nil {
					t.errorNotice(err)
					n := ui.Text(ui.TextProps{Color: ui.Error, Text: fmt.Sprintf("UI action failed: %.256s", err)})
					t.uiSeq++
					_ = t.uiRegistry().OpenDefault("atto", ui.OpenOptions{Site: ui.Toast, ID: fmt.Sprintf("atto/action-error-%d", t.uiSeq), Level: "error", ExpiresAt: time.Now().Add(4 * time.Second).UnixMilli()}, nil, &n)
				}
			})
		}
		t.elements.Log = func(owner string, site ui.Site, id string, err error) {
			log.Printf("ui %s/%s/%s: %.256s", owner, site, id, err)
		}
	}
	return t.elements
}
func (t *thread) publishUI(m ui.Mutation) {
	i := m.Instance
	p := map[string]any{"site": i.Site, "id": i.ID, "rev": i.Rev}
	switch m.Method {
	case "ui/open":
		p["options"] = i.Options
		if m.FocusClientID != "" {
			p["focusClientId"] = m.FocusClientID
		}
	case "ui/render":
		p["tree"] = i.Tree
		if ui.IsItem(i.Site) {
			p["actionsEnabled"] = t.elements.Bound(i.Site, i.ID)
		}
	case "ui/close":
		p["reason"] = m.Reason
	}
	if ui.IsItem(i.Site) && m.Method == "ui/render" {
		for j := range t.items {
			if t.items[j].ID == i.ID {
				w := &t.items[j]
				w.UIDisplay = &UIDisplay{Rev: i.Rev, Tree: i.Tree, ActionsEnabled: t.elements.Bound(i.Site, i.ID)}
				t.persistUIItem(*w)
			}
		}
	}
	if i.Site == ui.Transcript {
		t.persistUIBlock(m)
	}
	t.publish(m.Method, p)
}
func (t *thread) routeUI(client string, p threadParams) (any, error) {
	if t.readOnly != "" {
		return nil, failure(ReasonReadOnly, "%s", t.readOnly)
	}
	a := ui.Action{Site: p.Site, ID: p.ID, Key: p.Key, Type: p.EventType, Value: p.Value, Rev: p.Rev, ClientID: client, Surface: "headless"}
	t.s.mu.Lock()
	observer := false
	for _, c := range t.s.clients {
		if c.id == client && !c.interactive {
			observer = true
		}
		if c.id == client && c.ui != nil {
			a.Surface = c.ui.Surface
		}
	}
	t.s.mu.Unlock()
	if observer {
		return nil, failure(ReasonReadOnly, "this client observes UI but cannot act")
	}
	err := t.uiRegistry().Route(context.Background(), a)
	if err != nil {
		if re, ok := errors.AsType[*ui.RouteError](err); ok {
			return nil, &rpcError{Code: codeInvalidParams, Message: re.Message, Data: &ErrorData{Reason: re.Reason, CurrentRevision: int(re.CurrentRevision)}}
		}
		t.errorNotice(err)
		return map[string]any{"accepted": true, "rev": t.uiRevision(a)}, nil
	}
	return map[string]any{"accepted": true, "rev": t.uiRevision(a)}, nil
}
func (t *thread) uiRevision(a ui.Action) int64 { return t.uiRegistry().Revision(a.Site, a.ID) }
func (t *thread) persistUIBlock(m ui.Mutation) {
	if t.uiEntries == nil {
		t.uiEntries = map[string]string{}
	}
	i := m.Instance
	entry := t.uiEntries[i.ID]
	if m.Method == "ui/open" {
		if entry == "" {
			t.sess.Append(session.Entry{Type: session.TypeUIBlock, Ext: uiOwner(i.ID), Title: i.Options.Title, UISite: i.Site, UIID: i.ID, UIRev: i.Rev})
			t.uiEntries[i.ID] = t.sess.Leaf()
		}
		return
	}
	if entry == "" {
		return
	}
	e := session.Entry{Type: session.TypeUIBlockUpdate, TargetID: entry, UIRev: i.Rev, UITree: i.Tree, UIClosed: m.Method == "ui/close"}
	t.sess.Append(e)
	// Keep only the normal bounded display tail. No secondary full-tree cache.
	found := false
	for j := range t.items {
		it := &t.items[j]
		if it.Type == ItemUIBlock && it.EntryID == entry {
			it.UITree = i.Tree
			it.UIRev = i.Rev
			if e.UIClosed {
				it.UITree = nil
			}
			t.publish("item/updated", map[string]any{"item": *it})
			found = true
			break
		}
	}
	if !found && m.Method == "ui/render" {
		t.tr.Add(transcript.Item{Kind: transcript.UIBlock, Ext: uiOwner(i.ID), Title: i.Options.Title, EntryID: entry, UITree: i.Tree, UIRevision: i.Rev, UIID: i.ID})
		if len(t.attached) == 0 {
			t.tr.ForgetCompleted()
		}
	}
}

// UIBlock is the native Go host entry point, also in slim builds. It is queued
// like the legacy Host API but creates only portable data, never string UI.
func (h threadHost) UIBlock(title string, tree ui.Node) {
	h.do(func() {
		t := h.t
		t.uiSeq++
		id := fmt.Sprintf("atto/block-%d-%d", time.Now().UnixNano(), t.uiSeq)
		_ = t.uiRegistry().OpenDefault("atto", ui.OpenOptions{Site: ui.Transcript, ID: id, Title: title}, nil, &tree)
	})
}

func applyUITail(items []*transcript.Item, e session.Entry) {
	if e.Type != session.TypeUIBlockUpdate {
		return
	}
	for _, it := range items {
		if it.Kind == transcript.UIBlock && it.EntryID == e.TargetID {
			it.UITree = e.UITree
			it.UIRevision = e.UIRev
			if e.UIClosed {
				it.UITree = nil
			}
		}
	}
}
func applyUIItems(items []Item, e session.Entry) {
	if e.Type != session.TypeUIBlockUpdate && e.Type != session.TypeUIItemDisplay {
		return
	}
	for i := range items {
		it := &items[i]
		if it.EntryID != e.TargetID {
			continue
		}
		if e.Type == session.TypeUIBlockUpdate && it.Type == ItemUIBlock {
			it.UITree = e.UITree
			it.UIRev = e.UIRev
			if e.UIClosed {
				it.UITree = nil
			}
		} else if e.Type == session.TypeUIItemDisplay && (e.UICallID == "" || it.CallID == e.UICallID) && (e.Block == "" || e.Block == session.BlockReasoning && it.Type == ItemReasoning || e.Block == session.BlockText && it.Type == ItemAgent) {
			it.UIDisplay = &UIDisplay{Rev: e.UIRev, Tree: replayUITree(e.UITree, it.ID)}
		}
	}
}

func replayUITree(n *ui.Node, itemID string) *ui.Node {
	if n == nil {
		return nil
	}
	b, _ := json.Marshal(n)
	var out ui.Node
	_ = json.Unmarshal(b, &out)
	var walk func(*ui.Node)
	walk = func(n *ui.Node) {
		if n.Type == "engine" {
			n.Props["id"] = itemID
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	walk(&out)
	return &out
}

func uiOwner(id string) string {
	owner, _, _ := strings.Cut(id, "/")
	return owner
}
