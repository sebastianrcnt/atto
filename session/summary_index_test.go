package session

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
)

// referenceSummary is the summary computed from a full Load, as List did
// before summaries were read incrementally.
func referenceSummary(t *testing.T, path string) Summary {
	t.Helper()
	h, entries, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := Summary{Path: path, ID: h.ID, Cwd: h.Cwd, Created: h.Time, Updated: h.Time, Branch: h.GitBranch, AgentOf: h.AgentOf, External: h.External}
	if st, err := os.Stat(path); err == nil {
		s.Size = st.Size()
	}
	for _, e := range entries {
		s.Updated = e.Time
		if e.Type == TypeName {
			s.Name = e.Name
		}
		if e.Type == TypeModel {
			s.Model = e.Provider + "/" + e.Model
		}
		if e.Type == TypeMessage && e.Message != nil && e.Message.Role == "user" && s.Preview == "" {
			s.Preview = e.Message.Content
		}
	}
	first := true
	for _, e := range Active(entries) {
		if e.Type != TypeMessage || e.Message == nil {
			continue
		}
		switch e.Message.Role {
		case "user":
			s.Messages++
			if first {
				s.Preview, first = e.Message.Content, false
			}
		case "assistant":
			s.Messages++
			if text := strings.TrimSpace(e.Message.Content); text != "" {
				s.LastMessage = text
			}
		}
	}
	s.Preview = clipRunes(s.Preview, previewMax)
	s.LastMessage = clipRunes(s.LastMessage, previewMax)
	return s
}

// resetSummaryCache forgets everything, as a new process starts.
func resetSummaryCache() {
	summaryCache.Lock()
	summaryCache.paths = map[string]*summaryState{}
	summaryCache.disk = nil
	summaryCache.diskLoad = sync.Once{}
	summaryCache.dirty = false
	summaryCache.Unlock()
}

func smsg(role, text string) Entry {
	return Entry{Type: TypeMessage, Message: &provider.Message{Role: role, Content: text}}
}

// Summaries match a full Load across branches, names, models, tool-only
// answers and appends read incrementally.
func TestSummaryMatchesFullLoad(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	resetSummaryCache()
	w := New("/work")
	w.Append(Entry{Type: TypeModel, Provider: "p", Model: "m1"})
	w.Append(msg("user", "first question"))
	w.Append(msg("assistant", "  first answer\n"))
	w.Append(msg("assistant", ""))
	w.Append(msg("tool", "output"))
	check := func(when string) {
		t.Helper()
		got, err := listSummaryMode(w.Path, true)
		if err != nil {
			t.Fatal(err)
		}
		got.Running = 0
		if want := referenceSummary(t, w.Path); got != want {
			t.Fatalf("%s:\n got %+v\nwant %+v", when, got, want)
		}
	}
	check("start")
	reads := summaryReads.Load()
	check("unchanged")
	if summaryReads.Load() != reads {
		t.Fatal("an unchanged file was read again")
	}
	w.Append(Entry{Type: TypeName, Name: "named"})
	w.Append(msg("user", "second"))
	w.Append(msg("assistant", "second answer"))
	check("appended")
	// Go back to the first answer and continue from there: a new branch.
	_, entries, err := Load(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	w.Branch(entries[2].ID)
	w.Append(msg("user", "branch question"))
	w.Append(Entry{Type: TypeModel, Provider: "p", Model: "m2"})
	check("branched")
	// Back to before the first message: no user message on the branch.
	w.Branch("")
	check("rewound")
	w.Close()
}

// A rewritten (shorter) file is read again from the start, and the
// summary survives a new process through the disk cache without a read.
func TestSummaryRewriteAndDiskCache(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	resetSummaryCache()
	w := New("/work")
	w.Append(msg("user", "task"))
	w.Append(msg("assistant", strings.Repeat("long answer ", 1000)))
	w.Close()
	if _, err := listSummaryMode(w.Path, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n") // header, user message, answer
	if err := os.WriteFile(w.Path, []byte(lines[0]+lines[1]), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := listSummaryMode(w.Path, false)
	if err != nil || got.Messages != 1 || got.LastMessage != "" {
		t.Fatalf("after rewrite: %+v %v", got, err)
	}
	saveDiskSummaries(true)
	resetSummaryCache()
	reads := summaryReads.Load()
	again, err := listSummaryMode(w.Path, false)
	if err != nil || again.Messages != 1 || again.Preview != "task" {
		t.Fatalf("from disk: %+v %v", again, err)
	}
	if summaryReads.Load() != reads {
		t.Fatal("the disk cache did not spare the read")
	}
}

// A huge message is summarized without holding copies of it: the scan
// skips message text, and the preview is cut before it is converted.
func TestSummaryOfHugeMessageStaysSmall(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	resetSummaryCache()
	w := New("/work")
	w.Append(msg("user", strings.Repeat("x", 16<<20)))
	w.Append(msg("assistant", "ok"))
	w.Close()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	s, err := listSummaryMode(w.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if len(s.Preview) != previewMax || s.LastMessage != "ok" || s.Messages != 2 {
		t.Fatalf("summary %d %q %d", len(s.Preview), s.LastMessage, s.Messages)
	}
	// One read of the line to scan it and one to preview it, each the
	// line's size; Load decoded it into several more copies.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 40<<20 {
		t.Fatalf("summarizing a 16 MB message allocated %d MB", alloc>>20)
	}
}
