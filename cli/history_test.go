package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func TestHistoryGrepShow(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := session.New("/w")
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "build it"}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
		{ID: "c1", Function: provider.FunctionCall{Name: "bash", Arguments: `{"description":"Build","command":"go build ./..."}`}},
	}}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "tool", ToolCallID: "c1", Content: "ok\nmain.go:3: undefined: Frobnicate\n[exit code 1]"}})
	w.Append(session.Entry{Type: session.TypeCompaction, Notes: "build fails; see References: Frobnicate"})
	w.Close()
	t.Setenv("ATTO_SESSION_ID", w.ID)

	var out bytes.Buffer
	if err := RunHistory([]string{"grep", "-i", "frobnicate"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"#3 tool output: Build: main.go:3: undefined: Frobnicate", "#4 compaction notes:", "2 matching lines in 2 entries"} {
		if !strings.Contains(got, want) {
			t.Errorf("grep output missing %q:\n%s", want, got)
		}
	}

	out.Reset()
	if err := RunHistory([]string{"show", "-C", "1", "3"}, &out); err != nil {
		t.Fatal(err)
	}
	got = out.String()
	for _, want := range []string{"── #2 assistant ──\n[Build] $ go build ./...", "── #3 tool output: Build ──", "── #4 compaction notes ──"} {
		if !strings.Contains(got, want) {
			t.Errorf("show output missing %q:\n%s", want, got)
		}
	}
	if err := RunHistory([]string{"show", "99"}, &out); err == nil {
		t.Error("expected error for missing entry")
	}
}

func TestHistoryGrepMax(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := session.New("/w")
	for i := range 5 {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: fmt.Sprintf("match %d", i)}})
	}
	w.Close()
	t.Setenv("ATTO_SESSION_ID", w.ID)

	var out bytes.Buffer
	if err := RunHistory([]string{"grep", "-max", "2", "match"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, "user: match") != 2 || !strings.Contains(got, "[stopped after 2 matching lines; narrow the pattern or raise -max]") {
		t.Errorf("-max 2:\n%s", got)
	}

	out.Reset()
	if err := RunHistory([]string{"grep", "-max", "0", "match"}, &out); err != nil {
		t.Fatal(err)
	}
	got = out.String()
	if strings.Count(got, "user: match") != 5 || !strings.Contains(got, "5 matching lines in 5 entries") {
		t.Errorf("-max 0 should print every match:\n%s", got)
	}

	out.Reset()
	if err := RunHistory([]string{"grep", "-max", "-1", "match"}, &out); err == nil || !strings.Contains(err.Error(), "-max must not be negative") {
		t.Errorf("-max -1 should be a usage error: %v", err)
	}
}

func TestHistoryMarksOtherBranches(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := session.New("/w")
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "try plan A"}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "plan A failed"}})
	w.Branch("") // back to before the first message
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "try plan B"}})
	w.Close()
	t.Setenv("ATTO_SESSION_ID", w.ID)

	var out bytes.Buffer
	if err := RunHistory([]string{"grep", "plan"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#1 user (other branch): try plan A", "#2 assistant (other branch): plan A failed", "#4 user: try plan B"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("grep missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := RunHistory([]string{"grep", "-active", "plan"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "plan A") || !strings.Contains(out.String(), "#4 user: try plan B") {
		t.Errorf("-active:\n%s", out.String())
	}
	out.Reset()
	if err := RunHistory([]string{"show", "2"}, &out); err != nil || !strings.Contains(out.String(), "── #2 assistant (other branch) ──") {
		t.Errorf("show: %v\n%s", err, out.String())
	}
}

func TestReadStdinIdlePipe(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	start := time.Now()
	got, err := readStdin(r, 100*time.Millisecond)
	if err != nil || got != "" || time.Since(start) > time.Second {
		t.Fatalf("idle pipe should give up quickly: %q %v", got, err)
	}
	r2, w2 := io.Pipe()
	go func() { w2.Write([]byte("hello ")); w2.Write([]byte("world")); w2.Close() }()
	if got, _ := readStdin(r2, time.Second); got != "hello world" {
		t.Fatalf("got %q", got)
	}
}
