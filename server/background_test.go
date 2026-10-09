package server

import (
	"testing"

	"github.com/sebastianrcnt/atto/agentstate"
)

func TestAgentTreeIncludesOnlyRelatedDescendants(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for _, state := range []agentstate.State{
		{Name: "tests", Parent: "root-session", Session: "test-session"},
		{Name: "lint", Parent: "test-session", Session: "lint-session"},
		{Name: "other", Parent: "unrelated", Session: "other-session"},
	} {
		if err := agentstate.Save(state); err != nil {
			t.Fatal(err)
		}
	}
	value, err := background("agent/tree", "test-session", threadParams{})
	if err != nil {
		t.Fatal(err)
	}
	tree := value.(map[string]any)
	agents := tree["agents"].([]Agent)
	if tree["rootThreadId"] != "root-session" || len(agents) != 2 {
		t.Fatalf("tree: %#v", tree)
	}
	if agents[0].Path != "/root/tests" || agents[1].Path != "/root/tests/lint" || agents[1].ParentThreadID != "test-session" {
		t.Fatalf("agents: %+v", agents)
	}
	value, err = background("agent/read", "root-session", threadParams{Name: "/root/tests/lint"})
	if err != nil || value.(map[string]any)["agent"].(Agent).ThreadID != "lint-session" {
		t.Fatalf("nested agent read: %#v %v", value, err)
	}
	if _, err := background("agent/read", "root-session", threadParams{Name: "/root"}); err == nil {
		t.Fatal("root is not an agent")
	}
}
