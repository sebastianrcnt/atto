package session

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sebastianrcnt/atto/provider"
)

func TestStreamedTreeSearchForkAndArchivedEntries(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := New(t.TempDir())
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "first question"}})
	first := w.Leaf()
	full := strings.Repeat("long unicode αβγ output\n", 1000) + "unique-end-search-token"
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "assistant", Content: full}})
	answer := w.Leaf()
	w.Append(Entry{Type: TypeLabel, TargetID: answer, Label: "saved answer"})
	w.Append(Entry{Type: TypeBlockDisplay, TargetID: answer, Block: BlockText, Ext: "test", Display: "extension override"})
	w.Branch(first)
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "assistant", Content: "new branch"}})
	leaf := w.Leaf()
	w.Close()
	for _, archived := range []bool{false, true} {
		path := w.Path
		if archived {
			var err error
			path, err = Archive(path)
			if err != nil {
				t.Fatal(err)
			}
		}
		header, err := ReadHeader(path)
		if err != nil || header.ID != w.ID {
			t.Fatalf("header: %+v, %v", header, err)
		}
		rows, err := ReadTreeRows(path)
		if err != nil || len(rows) != 6 || rows[len(rows)-1].ID != leaf {
			t.Fatalf("tree: %d rows, %v", len(rows), err)
		}
		if utf8.RuneCountInString(rows[1].Message.Content) > 200 || strings.Contains(rows[1].Message.Content, "unique-end-search-token") {
			t.Fatal("tree retained full message")
		}
		matches, err := SearchTree(path, "unique-end-search-token saved")
		if err != nil || !reflect.DeepEqual(matches, []string{answer}) {
			t.Fatalf("full-text search: %v, %v", matches, err)
		}
		e, ok, err := ReadEntry(path, answer)
		if err != nil || !ok || e.Message.Content != full {
			t.Fatal("unloaded/off-branch entry was truncated")
		}
		fork, err := ForkFile(path, t.TempDir(), rows[3].ID)
		if err != nil {
			t.Fatal(err)
		}
		fork.Close()
		_, entries, err := Load(fork.Path)
		if err != nil {
			t.Fatal(err)
		}
		copied := false
		for _, e := range entries {
			if e.Type == TypeBlockDisplay {
				copied = e.TargetID == entries[1].ID && e.Display == "extension override"
			}
		}
		if !copied {
			t.Fatal("fork lost extension amendment or ID rewrite")
		}
		var left []Entry
		if err := VisitAbandoned(path, rows[3].ID, first, func(e Entry) error { left = append(left, e); return nil }); err != nil {
			t.Fatal(err)
		}
		if len(left) != 3 || left[0].ID != answer {
			t.Fatal("abandoned path changed")
		}
	}
}
