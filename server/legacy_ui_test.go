package server

import (
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/ui"
)

func TestOldEntriesConvertToPassivePortableTrees(t *testing.T) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	var entries []session.Entry
	var path string
	th.call(func() error {
		w := th.sess
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "original", ReasoningContent: "thinking"}})
		target := w.Leaf()
		w.Append(session.Entry{Type: session.TypeBlockDisplay, TargetID: target, Block: session.BlockText, Ext: "translator", Status: "translated", Display: "replacement"})
		w.Append(session.Entry{Type: session.TypeExtText, Ext: "report", Title: "Old report", Display: "old markdown", Preview: 2})
		path = w.Path
		_, entries, _ = session.Load(path)
		return nil
	})
	items := ItemsFromEntries(h.id, entries)
	if len(items) != 3 {
		t.Fatal(items)
	}
	for _, w := range items {
		switch w.Type {
		case ItemAgent:
			if w.Text != "original" || w.UIDisplay == nil || w.UIDisplay.ActionsEnabled || !strings.Contains(ui.PlainText(*w.UIDisplay.Tree), "replacement") {
				t.Fatal(w)
			}
		case ItemUIBlock:
			if w.UITree == nil || w.UITree.Type != "Collapse" || !strings.Contains(ui.PlainText(*w.UITree), "old markdown") {
				t.Fatal(w)
			}
		}
	}
	b := transcript.Builder{IDPrefix: itemPrefix(h.id)}
	page, _, err := replayFile(&b, h.id, path, "", DefaultItemLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != len(items) {
		t.Fatal(page.Items)
	}
	for i := range items {
		a, b := items[i], page.Items[i]
		if a.Type != b.Type || a.Text != b.Text {
			t.Fatal(a, b)
		}
		if a.Type == ItemAgent && b.UIDisplay == nil {
			t.Fatal("paged legacy overlay lost")
		}
	}
}
