// Package sessionfixture writes a synthetic long JSONL session for memory and
// protocol regressions. Generation itself streams directly to disk.
package sessionfixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

const Lines = 29152
const DisplayItems = 13016

func Write(t testing.TB, cwd string) (path, id string) {
	t.Helper()
	w := session.New(cwd)
	if err := os.MkdirAll(filepath.Dir(w.Path), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	encode := func(e session.Entry) {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	encode(session.Entry{Type: session.TypeSession, Version: 1, ID: w.ID, Cwd: cwd, Time: time.Now()})
	text := strings.Repeat("output line 0123456789abcdefghijklmnopqrstuv\n", 75)
	compact := func(i int) bool { return i > 0 && i%1160 == 0 }
	normal := func(i int) int {
		if i < 0 {
			return 0
		}
		return i + 1 - i/1160
	}
	displays := func(i int) int { return normal(i) * 12991 / 29126 }
	shown := func(i int) bool { return i >= 0 && i < Lines-1 && !compact(i) && displays(i) > displays(i-1) }
	tool := func(i int) bool { return shown(i) && displays(i)%4 == 0 && i > 0 && !compact(i-1) && !shown(i-1) }
	prev := ""
	for i := range Lines - 1 {
		e := session.Entry{Type: session.TypeModel, ID: fmt.Sprintf("%016x", i+1), Parent: prev, Time: time.Now(), Provider: "fake", Model: "m"}
		switch {
		case compact(i):
			e.Type = session.TypeCompaction
			notes := "Synthetic compacted notes"
			if i < 29000 {
				notes += strings.Repeat("old compacted context ", 34000)
			}
			e.Replacement = []provider.Message{{Role: "user", Content: notes}}
			e.Notes = "Synthetic compacted notes"
		case shown(i):
			e.Type = session.TypeMessage
			m := provider.Message{Role: "assistant", Content: fmt.Sprintf("entry %d\n", i) + text}
			switch displays(i) % 4 {
			case 1:
				m.Role = "user"
			case 2:
				m.ReasoningContent, m.Content = m.Content, ""
			case 0:
				if tool(i) {
					m.Role = "tool"
					m.ToolCallID = fmt.Sprintf("call-%d", i)
					e.Tool = &session.ToolMeta{Description: "Synthetic tool output", ExitCode: 0}
				}
			}
			e.Message = &m
		case tool(i + 1):
			e.Type = session.TypeMessage
			e.Message = &provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("call-%d", i+1), Function: provider.FunctionCall{Name: "bash", Arguments: `{"description":"Synthetic tool output","command":"echo synthetic"}`}}}}
		}
		encode(e)
		prev = e.ID
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return w.Path, w.ID
}
