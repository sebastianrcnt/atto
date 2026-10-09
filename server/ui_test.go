package server

import (
	"context"
	"encoding/json"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/ui"
	"strings"
	"testing"
)

func TestUIProtocolSnapshotAndStaleAction(t *testing.T) {
	h := newHarness(t)
	calls := 0
	var rev int64
	th, _ := h.s.thread(h.id)
	err := th.call(func() error {
		r := th.uiRegistry()
		m := ui.Match{Site: ui.Pane, ID: "atto/test"}
		r.Bind("atto", m, "press", ui.Press, func(context.Context, ui.Action) error { calls++; return nil })
		n := ui.Button(ui.ButtonProps{Key: "press", Label: "Press"})
		return r.OpenDefault("atto", ui.OpenOptions{Site: ui.Pane, ID: m.ID}, nil, &n)
	})
	if err != nil {
		t.Fatal(err)
	}
	out := h.call("thread/read", nil)
	b, _ := json.Marshal(out["ui"])
	var snap ui.Snapshot
	if err = json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Instances) < 1 {
		t.Fatal(string(b))
	}
	for _, instance := range snap.Instances {
		if instance.Site == ui.Pane {
			rev = instance.Rev
		}
	}
	p := map[string]any{"site": "pane", "id": "atto/test", "key": "press", "type": "press", "rev": rev}
	h.call("ui/event", p)
	if err = th.call(func() error {
		if calls != 1 {
			t.Fatalf("calls %d", calls)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = h.s.call(context.Background(), "ui/event", mustJSON(map[string]any{"threadId": h.id, "site": "pane", "id": "atto/test", "key": "press", "type": "press", "rev": rev}))
	if err == nil {
		t.Fatal("duplicate accepted")
	}
}
func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func TestUIBlockReplayIsDisplayOnly(t *testing.T) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	n := ui.Collapse(ui.CollapseProps{Key: "diff", Title: "git diff", PreviewLines: 2}, ui.Diff(ui.DiffProps{Source: "+hello\n-world"}))
	threadHost{th}.UIBlock("git diff", n)
	if err := th.call(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var path string
	if err := th.call(func() error { path = th.sess.Path; return nil }); err != nil {
		t.Fatal(err)
	}
	_, entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	items := ItemsFromEntries(h.id, entries)
	if len(items) != 1 || items[0].Type != ItemUIBlock || items[0].UITree == nil {
		t.Fatalf("%#v", items)
	}
	for _, e := range entries {
		if e.Type == session.TypeUIBlock || e.Type == session.TypeUIBlockUpdate {
			if e.Message != nil {
				t.Fatal("UI in model history")
			}
		}
	}
}
func TestUIReducerTombstones(t *testing.T) {
	v := ThreadView{}
	v.Reset(ThreadInfo{ID: "t", EventID: 10, UI: &ui.Snapshot{Version: 1, Instances: []ui.Instance{}}})
	event := func(method string, id int64, rev int64) Notification {
		return Notification{Method: method, EventID: id, Params: mustJSON(map[string]any{"threadId": "t", "site": "pane", "id": "atto/p", "rev": rev, "tree": ui.Text(ui.TextProps{Text: "test"})})}
	}
	if !v.Apply(event("ui/open", 11, 1)) || !v.Apply(event("ui/render", 12, 2)) || !v.Apply(event("ui/close", 13, 3)) {
		t.Fatal("didn't apply")
	}
	if v.Apply(event("ui/render", 14, 2)) || len(v.Info.UI.Instances) != 0 {
		t.Fatal("late tree reopened")
	}
	if v.Apply(event("ui/open", 10, 5)) {
		t.Fatal("covered cursor")
	}
}

func TestUIDialogFirstAnswerWins(t *testing.T) {
	h := newHarness(t)
	other := h.connect()
	th, _ := h.s.thread(h.id)
	calls := 0
	if err := th.call(func() error {
		th.ask(&openPrompt{origin: "extension", wire: Prompt{Kind: PromptSelect, Title: "Pick", Options: []PromptOption{{Label: "one"}, {Label: "two"}}}, choose: func(int) { calls++ }, cancel: func() {}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out := h.call("thread/read", nil)
	b, _ := json.Marshal(out["ui"])
	var snap ui.Snapshot
	_ = json.Unmarshal(b, &snap)
	var dialog ui.Instance
	for _, i := range snap.Instances {
		if i.Site == ui.Dialog {
			dialog = i
		}
	}
	if dialog.ID == "" {
		t.Fatal("missing dialog")
	}
	params := map[string]any{"site": "dialog", "id": dialog.ID, "key": "answer", "type": "select", "value": "1", "rev": dialog.Rev}
	h.call("ui/event", params)
	if _, err := h.try(other, "ui/event", params); err == nil {
		t.Fatal("late answer accepted")
	}
	if err := th.call(func() error {
		if calls != 1 || th.prompt != nil {
			t.Fatal("broker not settled exactly once")
		}
		for _, i := range th.uiRegistry().Snapshot().Instances {
			if i.Site == ui.Dialog {
				t.Fatal("answered dialog remains")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUIItemOverlayPersistsWithoutChangingTruth(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "original answer"})
	th, _ := h.s.thread(h.id)
	if err := th.call(func() error {
		th.uiRegistry().Render("review", ui.Match{Site: ui.AssistantMessage}, func(e ui.Event, next ui.Next) (*ui.Node, error) {
			n, err := next(e)
			if err != nil {
				return nil, err
			}
			tree := ui.Box(ui.BoxProps{}, *n, ui.Text(ui.TextProps{Text: "display-only review"}))
			return &tree, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.call("input/submit", map[string]any{"input": "hello"})
	h.completed()
	var path string
	th.call(func() error { path = th.sess.Path; return nil })
	_, entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	items := ItemsFromEntries(h.id, entries)
	found := false
	for _, w := range items {
		if w.Type == ItemAgent {
			if w.Text != "original answer" || w.UIDisplay == nil || w.UIDisplay.Tree == nil || w.UIDisplay.ActionsEnabled {
				t.Fatalf("truth/overlay: %#v", w)
			}
			if err := ui.ValidateDisplay(ui.AssistantMessage, w.ID, *w.UIDisplay.Tree); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing persisted overlay")
	}
	for _, e := range entries {
		if e.Type == session.TypeMessage && e.Message != nil && strings.Contains(e.Message.Content, "display-only review") {
			t.Fatal("changed model context")
		}
	}
}

func TestUIBlockPreservesProviderOwner(t *testing.T) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	var path string
	if err := th.call(func() error {
		n := ui.Text(ui.TextProps{Text: "provider drawing"})
		path = th.sess.Path
		return th.uiRegistry().OpenDefault("review", ui.OpenOptions{Site: ui.Transcript, ID: "review/report", Title: "Review"}, nil, &n)
	}); err != nil {
		t.Fatal(err)
	}
	_, entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	items := ItemsFromEntries(h.id, entries)
	if len(items) != 1 || items[0].Ext != "review" || items[0].Title != "Review" {
		t.Fatalf("provider provenance lost: %#v", items)
	}
}

func TestUIChangesDoNotAlterModelRequestBytes(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "original answer"})
	h.call("turn/start", map[string]any{"input": "hello"})
	h.completed()
	h.call("thread/rollback", nil)
	th, _ := h.s.thread(h.id)
	if err := th.call(func() error {
		r := th.uiRegistry()
		r.Render("review", ui.Match{Site: ui.AssistantMessage}, func(e ui.Event, next ui.Next) (*ui.Node, error) {
			original, err := next(e)
			if err != nil {
				return nil, err
			}
			n := ui.Box(ui.BoxProps{}, *original, ui.Text(ui.TextProps{Text: "display-only secret"}))
			return &n, nil
		})
		n := ui.Text(ui.TextProps{Text: "display-only block secret"})
		return r.OpenDefault("review", ui.OpenOptions{Site: ui.Transcript, ID: "review/block", Title: "Review"}, nil, &n)
	}); err != nil {
		t.Fatal(err)
	}
	h.call("turn/start", map[string]any{"input": "hello"})
	h.completed()
	requests := h.m.Requests()
	if len(requests) != 2 || requests[0] != requests[1] {
		t.Fatalf("display mutations altered provider request bytes: %v", requests)
	}
}
