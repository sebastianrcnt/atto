package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func call(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp := s.Handle(context.Background(), b)
	if resp.Error != nil {
		t.Fatalf("%s: %s", method, resp.Error.Message)
	}
	raw, _ := json.Marshal(resp.Result)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return out
}

func itemTexts(r map[string]any) string {
	items, _ := r["items"].([]any)
	s := ""
	for _, it := range items {
		s += fmt.Sprint(it.(map[string]any)["text"]) + ";"
	}
	return s
}

func TestRollbackFollowsBranch(t *testing.T) {
	work := setup(t)
	w := session.New(work)
	for _, m := range []provider.Message{{Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"}, {Role: "user", Content: "u2"}, {Role: "assistant", Content: "a2"}} {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &m})
	}
	w.Close()

	s := New("test", work)
	r := call(t, s, "thread/resume", map[string]any{"threadId": w.ID})
	if got := itemTexts(r); got != "u1;a1;u2;a2;" {
		t.Fatalf("resume items %q", got)
	}
	r = call(t, s, "thread/rollback", map[string]any{"threadId": w.ID})
	if got := itemTexts(r); got != "u1;a1;" || r["input"] != "u2" {
		t.Fatalf("rollback %q input %v", got, r["input"])
	}
	s.Close()

	// A fresh server resumes on the new branch; the old one stays on disk.
	s2 := New("test", work)
	t.Cleanup(s2.Close)
	if got := itemTexts(call(t, s2, "thread/resume", map[string]any{"threadId": w.ID})); got != "u1;a1;" {
		t.Fatalf("after rollback, resume shows %q", got)
	}
	_, entries, _ := session.Load(w.Path)
	if len(entries) != 5 || entries[4].Type != session.TypeBranch {
		t.Fatalf("entries %+v", entries)
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "thread/rollback", "params": map[string]any{"threadId": w.ID, "numTurns": 5}})
	if resp := s2.Handle(context.Background(), b); resp.Error == nil {
		t.Fatal("rolling back more than there is should fail")
	}
}

// rollbackOver resumes a thread of two user turns, the second followed by
// one message atto sent in the user's role, and rolls back one turn. That
// message is not a turn, so the rollback must land on the user's own "u2".
func rollbackOver(t *testing.T, content string) {
	t.Helper()
	work := setup(t)
	w := session.New(work)
	for _, m := range []provider.Message{
		{Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"}, {Role: "assistant", Content: "a2"},
		{Role: "user", Content: content}, {Role: "assistant", Content: "a3"},
	} {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &m})
	}
	w.Close()

	s := New("test", work)
	t.Cleanup(s.Close)
	call(t, s, "thread/resume", map[string]any{"threadId": w.ID})
	r := call(t, s, "thread/rollback", map[string]any{"threadId": w.ID})
	if got := itemTexts(r); got != "u1;a1;" || r["input"] != "u2" {
		t.Fatalf("rollback %q input %v, want the user's own u2", got, r["input"])
	}
}

func TestRollbackSkipsGoalAndStopHookMessages(t *testing.T) {
	t.Run("goal", func(t *testing.T) { rollbackOver(t, goal.OpenTag+"\nkeep going\n"+goal.CloseTag) })
	t.Run("legacy goal", func(t *testing.T) { rollbackOver(t, "[atto goal] keep going") })
	t.Run("stop hook", func(t *testing.T) { rollbackOver(t, agent.StopHookPrefix+"run the tests first") })
}
