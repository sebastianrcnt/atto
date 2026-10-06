package session

import (
	"runtime"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
)

func TestListCachesUnchangedSummaries(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	var writers []*Writer
	for range 8 {
		w := New("/work")
		w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "task"}})
		w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "assistant", Content: strings.Repeat("x", 256*1024)}})
		w.Close()
		writers = append(writers, w)
	}
	if list, err := List("", false); err != nil || len(list) != 8 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 3 {
		if _, err := List("", false); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 512*1024 {
		t.Fatalf("unchanged scans allocated %d bytes", allocated)
	}
	w := writers[0]
	h, _, err := Load(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	changed := Resume(w.Path, h)
	changed.Append(Entry{Type: TypeName, Name: "changed"})
	changed.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "assistant", Content: "new answer"}})
	changed.Close()
	list, err := List("", false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range list {
		if s.ID == w.ID {
			found = s.Name == "changed" && s.LastMessage == "new answer"
		}
	}
	if !found {
		t.Fatal("changed file did not invalidate its summary")
	}
}

func TestListSkipsSubagentHistory(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := NewSubagent("/work", "root")
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: strings.Repeat("x", 2*1024*1024)}})
	w.Close()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	list, err := List("", false)
	runtime.ReadMemStats(&after)
	if err != nil || len(list) != 0 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 512*1024 {
		t.Fatalf("subagent history loaded: %d bytes", allocated)
	}
}

func BenchmarkCachedSessionList(b *testing.B) {
	b.Setenv("ATTO_DIR", b.TempDir())
	for range 100 {
		w := New("/work")
		w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "task"}})
		w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "assistant", Content: strings.Repeat("x", 1024*1024)}})
		w.Close()
	}
	if _, err := List("", false); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := List("", false); err != nil {
			b.Fatal(err)
		}
	}
}
