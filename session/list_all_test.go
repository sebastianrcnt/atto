package session

import (
	"github.com/sebastianrcnt/atto/provider"
	"testing"
)

func TestListAllIncludesAgentSummaries(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	parent := NewExternal("/work")
	parent.Append(Entry{Type: TypeName, Name: "external"})
	parent.Close()
	agent := NewSubagent("/work", parent.ID)
	agent.Append(Entry{Type: TypeName, Name: "tests"})
	agent.Close()
	// Prime the header-only default cache, then request the full summaries.
	normal, err := List("", false)
	if err != nil || len(normal) != 1 || normal[0].ID != parent.ID {
		t.Fatalf("default %v %v", normal, err)
	}
	all, err := ListAll("", false)
	if err != nil || len(all) != 2 {
		t.Fatalf("all %v %v", all, err)
	}
	for _, s := range all {
		if s.ID == agent.ID && s.Name != "tests" {
			t.Fatalf("agent header-only: %+v", s)
		}
	}
	agent = Resume(agent.Path, Entry{ID: agent.ID})
	agent.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "Run tests"}})
	agent.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "assistant", Content: "Tests pass"}})
	agent.Close()
	all, err = ListAll("/work", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.ID == agent.ID && (s.Preview != "Run tests" || s.LastMessage != "Tests pass") {
			t.Fatalf("stale summary %+v", s)
		}
	}
	normal, _ = List("", false)
	if len(normal) != 1 {
		t.Fatal("inclusive cache changed default listing")
	}
	if other, _ := ListAll("/other", false); len(other) != 0 {
		t.Fatal("cwd filter lost")
	}
	if _, err := Archive(agent.Path); err != nil {
		t.Fatal(err)
	}
	archived, err := ListAll("", true)
	if err != nil || len(archived) != 1 || !archived[0].Archived || archived[0].AgentOf != parent.ID {
		t.Fatalf("archived %v %v", archived, err)
	}
}
