package server

import (
	"context"
	"encoding/json"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/ui"
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
	if len(snap.Instances) != 1 {
		t.Fatal(string(b))
	}
	rev = snap.Instances[0].Rev
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
