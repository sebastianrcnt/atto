package server

import (
	"testing"

	"github.com/sebastianrcnt/atto/agentstate"
)

// An agent started from a shell is the root of its tree: agent/tree includes
// it, with no parent, and agent/read finds agents by session ID too.
func TestAgentTreeOfAnOutsideRoot(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	by := &agentstate.SpawnedBy{Origin: "outside", Cwd: "/src/p"}
	for _, state := range []agentstate.State{
		{Name: "tests", Session: "aaaa1111", Project: "/src/p", SpawnedBy: by, Job: 3, JobOwner: "aaaa1111"},
		{Name: "lint", Parent: "aaaa1111", Session: "bbbb1111"},
	} {
		if err := agentstate.Save(state); err != nil {
			t.Fatal(err)
		}
	}
	for _, sid := range []string{"aaaa1111", "bbbb1111"} {
		value, err := background("agent/tree", sid, threadParams{})
		if err != nil {
			t.Fatal(err)
		}
		tree := value.(map[string]any)
		agents := tree["agents"].([]Agent)
		if tree["rootThreadId"] != "aaaa1111" || len(agents) != 2 {
			t.Fatalf("%s: tree: %#v", sid, tree)
		}
		root, child := agents[0], agents[1]
		if root.ThreadID != "aaaa1111" || root.ParentThreadID != "" || root.RootThreadID != "aaaa1111" || root.Depth != 0 || root.Origin != "external" ||
			root.Project != "/src/p" || root.Lifecycle != "open" || root.JobOwner != "aaaa1111" || root.Job != 3 || root.SpawnedBy == nil || root.SpawnedBy.Origin != "outside" {
			t.Fatalf("root: %+v", root)
		}
		if child.ParentThreadID != "aaaa1111" || child.RootThreadID != "aaaa1111" || child.Depth != 1 || child.Path != "/root/lint" || child.JobOwner != "aaaa1111" {
			t.Fatalf("child: %+v", child)
		}
	}
	// agent/list shows direct children only: a root has the one.
	value, _ := background("agent/list", "aaaa1111", threadParams{})
	if agents := value.(map[string]any)["agents"].([]Agent); len(agents) != 1 || agents[0].ThreadID != "bbbb1111" {
		t.Fatalf("list: %+v", agents)
	}
	// agent/read takes the agent's ID as agentId, and still names.
	for _, p := range []threadParams{{AgentID: "bbbb1111"}, {AgentID: "bbbb11"}, {Name: "lint"}, {Name: "@bbbb11"}} {
		value, err := background("agent/read", "aaaa1111", p)
		if err != nil || value.(map[string]any)["agent"].(Agent).ThreadID != "bbbb1111" {
			t.Fatalf("%+v: %#v %v", p, value, err)
		}
	}
	if _, err := background("agent/read", "aaaa1111", threadParams{AgentID: "cccc1111"}); err == nil {
		t.Fatal("read an agent that does not exist")
	}
}
