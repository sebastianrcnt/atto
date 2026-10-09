package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/jobs"
)

// There is no depth limit: a chain of four agents under one session spawns, runs
// and is addressable down to its end, with every ancestor known.
func TestAgentsNestToAnyDepth(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	t.Chdir(t.TempDir())
	enableAgents(t, `,"maxDepth":1`) // an old setting: ignored
	parent := "p1"
	var chain []agentstate.State
	for _, name := range []string{"a", "b", "c", "d"} {
		if out, err := runAgent(t, "spawn", name, "work", "-session", parent); err != nil {
			t.Fatalf("%s under %s: %q %v", name, parent, out, err)
		}
		st, err := agentstate.LoadChild(parent, name)
		if err != nil {
			t.Fatal(err)
		}
		chain = append(chain, st)
		parent = st.Session
	}
	t.Cleanup(func() {
		jobs.KillAll("p1")
		for _, st := range chain {
			jobs.KillAll(st.Session)
		}
	})
	last := chain[3]
	if last.Depth != 4 || last.Root != "p1" || last.Path != "/root/a/b/c/d" {
		t.Fatalf("depth-4 agent: %+v", last)
	}
	if got, err := agentstate.Ancestry(last.Session); err != nil || len(got) != 5 || got[4] != "p1" {
		t.Fatalf("ancestry %v %v", got, err)
	}
	if tg, err := agentstate.Resolve("p1", "a/b/c/d"); err != nil || tg.Session != last.Session {
		t.Fatalf("resolve: %+v %v", tg, err)
	}
	for _, st := range chain {
		if out, err := runAgent(t, "wait", "@"+st.Session, "-timeout", "30s"); err != nil || !strings.Contains(out, "done") {
			t.Fatalf("%s: %q %v", st.Name, out, err)
		}
	}
	// Closing the top closes the chain below it, deepest first.
	out, err := runAgent(t, "close", "a", "-session", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(out, "/root/a/b/c/d") > strings.Index(out, "/root/a/b ") || strings.Index(out, "/root/a/b ") > strings.Index(out, "closed agent /root/a (") {
		t.Fatalf("close order: %q", out)
	}
}

// An agent made before agents were keyed by session ID keeps the worktree path
// and branch it was made with, and closing it removes that and the directory
// of its parent; new ones are named by session ID.
func TestClosingAnAgentWithAnOldWorktreeUsesItsRecordedPaths(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	repo := gitRepo(t)
	t.Chdir(repo)
	enableAgents(t, "")
	old := filepath.Join(config.Dir(), "worktrees", "pppp0001", "legacy")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "worktree", "add", "-b", "atto/pppp0001/legacy", old)
	st := agentstate.State{Name: "legacy", Parent: "pppp0001", Session: "oldwt001", Repo: repo, Worktree: old, Branch: "atto/pppp0001/legacy", Cwd: old, Task: "t"}
	if err := agentstate.Save(st); err != nil {
		t.Fatal(err)
	}
	out, err := runAgent(t, "close", "legacy", "-session", "pppp0001")
	if err != nil || !strings.Contains(out, "worktree "+old+" removed; branch atto/pppp0001/legacy kept") {
		t.Fatalf("close: %q %v", out, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("worktree kept: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(old)); !os.IsNotExist(err) {
		t.Fatalf("the old parent directory is left: %v", err)
	}
	if !hasBranch(repo, "atto/pppp0001/legacy") {
		t.Fatal("branch removed")
	}
	// The directory every new worktree goes in is never pruned.
	if _, err := os.Stat(filepath.Join(config.Dir(), "worktrees")); err != nil {
		t.Fatalf("worktrees/ itself: %v", err)
	}
}
