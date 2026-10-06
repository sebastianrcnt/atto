package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// A session a background run is writing is not resumed.
func TestResumeRefusesLockedSession(t *testing.T) {
	work := setup(t)
	w := session.New(work)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "u1"}})
	w.Close()
	if _, err := session.LockFor(w.Path, os.Getppid()); err != nil {
		t.Fatal(err)
	}
	s := New("test", work)
	t.Cleanup(s.Close)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "thread/resume", "params": map[string]any{"threadId": w.ID}})
	resp := s.Handle(context.Background(), b)
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "running in the background") {
		t.Fatalf("resume: %+v", resp)
	}
}

func TestStandaloneServerHoldsWriterLease(t *testing.T) {
	work := setup(t)
	s := New("test", work)
	defer s.Close()
	got, err := s.startThread(threadParams{})
	if err != nil {
		t.Fatal(err)
	}
	id := got.(ThreadInfo).ID
	s.threads[id].sess.Append(session.Entry{Type: session.TypeName, Name: "test"})
	path, err := session.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := session.LockedBy(path)
	if !ok || l.Kind != session.KindServer {
		t.Fatalf("lease: %+v %v", l, ok)
	}
	s2 := New("test", work)
	defer s2.Close()
	if _, err := s2.resumeThread(id); err == nil {
		t.Fatal("second writer opened session")
	}
	if _, err := session.LockTUI(path); err == nil {
		t.Fatal("terminal opened server session")
	}
	s.Close()
	if _, err := s2.resumeThread(id); err != nil {
		t.Fatal("lease not released:", err)
	}
}

func TestStandalonePromptDoesNotAdvertiseGoals(t *testing.T) {
	work := setup(t)
	s := New("test", work)
	defer s.Close()
	got, err := s.startThread(threadParams{})
	if err != nil {
		t.Fatal(err)
	}
	prompt := s.threads[got.(ThreadInfo).ID].agent.SystemPrompt()
	if strings.Contains(prompt, "Goals:") || strings.Contains(prompt, "atto goal set") {
		t.Fatal("standalone prompt advertises unsupported goals")
	}
}
