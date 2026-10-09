//go:build !noext

package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/ui"
)

func waitExtensionUI(t *testing.T, h *harness, id, text string) ui.Instance {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := h.call("thread/read", nil)
		b, _ := json.Marshal(out["ui"])
		var snap ui.Snapshot
		_ = json.Unmarshal(b, &snap)
		for _, i := range snap.Instances {
			if i.ID == id && i.Tree != nil && strings.Contains(ui.PlainText(*i.Tree), text) {
				return i
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing %s: %s", id, text)
	return ui.Instance{}
}
func TestExtensionCounterViaUIEventAndPersistentStore(t *testing.T) {
	h := newHarness(t)
	src, err := os.ReadFile("../examples/extensions/counter.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(config.ExtensionsDir(), "counter.tsx"), src, 0600); err != nil {
		t.Fatal(err)
	}
	h.call("input/submit", map[string]any{"input": "/reload"})
	h.call("input/submit", map[string]any{"input": "/counter"})
	i := waitExtensionUI(t, h, "counter/counter", "Count: 0")
	h.call("ui/event", map[string]any{"site": "pane", "id": i.ID, "key": "counter/more", "type": "press", "rev": i.Rev})
	_ = waitExtensionUI(t, h, i.ID, "Count: 1")
	th, _ := h.s.thread(h.id)
	var path string
	th.call(func() error { path = th.sess.Path; return nil })
	_, entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Type == session.TypeUIStore && e.Ext == "counter" && e.StoreKey == "count" {
			found = string(e.StoreValue) == "1"
		}
	}
	if !found {
		t.Fatal("store was not written through session writer")
	}
	h.call("input/submit", map[string]any{"input": "/reload"})
	h.call("input/submit", map[string]any{"input": "/counter"})
	_ = waitExtensionUI(t, h, i.ID, "Count: 1")
	if _, err = h.try(h.c, "ui/event", map[string]any{"site": "pane", "id": i.ID, "key": "counter/more", "type": "press", "rev": i.Rev}); err == nil {
		t.Fatal("pre-reload revision accepted")
	}
}
func TestGojaToolWrapperPersistsWithoutModelChanges(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "original"}, providertest.Reply{Text: "second"})
	src := `export default(atto:any)=>atto.ui.render({site:"assistantMessage"},async(e:any,next:any)=>{const{Box,Text}=atto.ui.resolve(e);return Box({children:[await next(e),Text({text:"goja-only overlay"})]})})`
	if err := os.WriteFile(filepath.Join(config.ExtensionsDir(), "wrap.ts"), []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	h.call("input/submit", map[string]any{"input": "/reload"})
	h.call("input/submit", map[string]any{"input": "q1"})
	h.completed()
	h.call("input/submit", map[string]any{"input": "q2"})
	h.completed()
	if requests := h.m.Requests(); len(requests) != 2 || strings.Contains(requests[1], "goja-only overlay") {
		t.Fatal(requests)
	}
	th, _ := h.s.thread(h.id)
	var path string
	th.call(func() error { path = th.sess.Path; return nil })
	_, entries, _ := session.Load(path)
	found := false
	for _, w := range ItemsFromEntries(h.id, entries) {
		if w.Type == ItemAgent && w.Text == "original" {
			found = w.UIDisplay != nil && w.UIDisplay.Tree != nil && strings.Contains(ui.PlainText(*w.UIDisplay.Tree), "goja-only overlay") && !w.UIDisplay.ActionsEnabled
		}
	}
	if !found {
		t.Fatal("goja overlay missing on passive replay")
	}
}
func TestRenderStoreReadDoesNotWaitForLane(t *testing.T) {
	h := newHarness(t)
	src := `export default(atto:any)=>{atto.ui.render({site:"pane",id:"store"},async(e:any)=>atto.ui.resolve(e).Text({text:String(await atto.store.get("key"))}));atto.registerCommand("readstore",{handler:async()=>{await atto.store.set("key",42);await atto.ui.open({site:"pane",id:"store"})}})}`
	_ = os.WriteFile(filepath.Join(config.ExtensionsDir(), "read.ts"), []byte(src), 0600)
	h.call("input/submit", map[string]any{"input": "/reload"})
	h.call("input/submit", map[string]any{"input": "/readstore"})
	_ = waitExtensionUI(t, h, "read/store", "42")
}
func TestReloadCancelsExtensionDialogAndRetiresWrapper(t *testing.T) {
	h := newHarness(t)
	src := `export default(atto:any)=>{atto.ui.render({site:"dialog"},async(e:any,next:any)=>{const{Box,Text}=atto.ui.resolve(e);return Box({children:[Text({text:e.props.title}),await next(e)]})});atto.registerCommand("ask",{handler:async()=>atto.ui.notify(String(await atto.ui.confirm("Wrapped question")))})}`
	_ = os.WriteFile(filepath.Join(config.ExtensionsDir(), "ask.ts"), []byte(src), 0600)
	h.call("input/submit", map[string]any{"input": "/reload"})
	h.call("input/submit", map[string]any{"input": "/ask"})
	th, _ := h.s.thread(h.id)
	var dialog ui.Instance
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		th.call(func() error {
			for _, i := range th.uiRegistry().Snapshot().Instances {
				if i.Site == ui.Dialog {
					dialog = i
				}
			}
			return nil
		})
		if dialog.ID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if dialog.ID == "" || dialog.Tree == nil {
		t.Fatal("dialog missing")
	}
	h.call("input/submit", map[string]any{"input": "/reload"})
	th.call(func() error {
		if th.prompt != nil {
			t.Fatal("reload left dialog pending")
		}
		if err := th.uiRegistry().Route(context.Background(), ui.Action{Site: ui.Dialog, ID: dialog.ID, Key: "answer", Type: ui.SelectEvent, Value: func() *string { s := "0"; return &s }(), Rev: dialog.Rev}); err == nil {
			t.Fatal("retired dialog revision accepted")
		}
		return nil
	})
}

func TestCustomExtensionDialogClosesThroughUIAndGatesWork(t *testing.T) {
	h := newHarness(t)
	src := `export default(atto:any)=>{atto.ui.render({site:"dialog",id:"custom"},e=>{const{Box,Text,Button}=atto.ui.resolve(e);return Box({children:[Text({text:e.props.kind+":"+e.props.title}),Button({key:"done",label:"Done",onPress:async()=>{await atto.store.set("closed",true);await atto.ui.close({site:"dialog",id:"custom"})}})]})});atto.registerCommand("custom",{handler:()=>atto.ui.open({site:"dialog",id:"custom",title:"Question"})})}`
	_ = os.WriteFile(filepath.Join(config.ExtensionsDir(), "custom.ts"), []byte(src), 0600)
	h.call("input/submit", map[string]any{"input": "/reload"})
	h.call("input/submit", map[string]any{"input": "/custom"})
	i := waitExtensionUI(t, h, "custom/custom", "custom:Question")
	th, _ := h.s.thread(h.id)
	th.call(func() error {
		if th.prompt == nil || th.prompt.wire.UIID != i.ID {
			t.Fatal("custom dialog bypassed broker gate")
		}
		return nil
	})
	h.call("ui/event", map[string]any{"site": "dialog", "id": i.ID, "key": "custom/done", "type": "press", "rev": i.Rev})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		closed := false
		th.call(func() error { closed = th.prompt == nil; return nil })
		if closed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("custom dialog callback held lane or failed to close")
}
