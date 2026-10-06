package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/subagent"
)

func treeJob(t *testing.T, owner string, id int, kind, name string) {
	t.Helper()
	dir := filepath.Join(jobs.Root(owner), "1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(jobs.Job{ID: id, Session: owner, Type: kind, Name: name, Status: jobs.Starting, Started: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, "job.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRootLeaveStopsDescendantsOfCompletedAgent(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	a := subagent.State{Parent: "root", Name: "a", Session: "a-session"}
	b := subagent.State{Parent: a.Session, Name: "b", Session: "b-session"}
	for _, s := range []subagent.State{a, b} {
		if err := subagent.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	// A has already completed; B's job and B's own background work remain.
	treeJob(t, a.Session, 1, "agent", "b")
	treeJob(t, b.Session, 1, "", "background")
	if n := Leave("root"); n != 2 {
		t.Fatalf("stopped %d jobs, want 2", n)
	}
	for _, owner := range []string{a.Session, b.Session} {
		j, err := jobs.Get(owner, 1)
		if err != nil || j.Status != jobs.Killed {
			t.Fatalf("descendant job: %+v %v", j, err)
		}
	}
	if release, err := subagent.StartWork(b.Session); err == nil {
		release()
		t.Fatal("closed tree accepted new work")
	}
	subagent.OpenTree("root")
	release, err := subagent.StartWork(b.Session)
	if err != nil {
		t.Fatal("reopened tree:", err)
	}
	release()
}

func TestIntermediateTurnKeepsOnlyAgentJobs(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	treeJob(t, "ordinary", 1, "", "agent misleading name")
	treeJob(t, "agent-parent", 1, "agent", "not a name prefix")
	if n := LeaveKeepingAgents("ordinary"); n != 1 {
		t.Fatal("ordinary job survived based on its name")
	}
	if n := LeaveKeepingAgents("agent-parent"); n != 0 {
		t.Fatal("explicit agent job was killed")
	}
}
