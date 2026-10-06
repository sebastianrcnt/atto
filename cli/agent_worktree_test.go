package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/prompts"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/subagent"
)

// gitRepo makes a repository with one commit, holding sub/file.txt, and
// returns its path.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "t")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t@example.com")
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "file.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "-q")
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "commit", "-q", "-m", "init")
	return repo
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func hasBranch(dir, branch string) bool {
	_, err := git(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func TestAgentWorktree(t *testing.T) {
	bodies := agentServer(t, func(n int, _ string) string {
		if n%2 == 1 {
			return toolAnswer("echo hi > made.txt")
		}
		return textAnswer("made it")
	})
	repo := gitRepo(t)
	t.Chdir(filepath.Join(repo, "sub"))
	enableSubagents(t, "")
	const parent = "p1"
	// The model inside atto may use it: it is isolation, not an override.
	t.Setenv(config.EnvAgent, "1")

	out, err := runAgent(t, "start", "w1", "general", "make", "a", "file", "-worktree", "-session", parent)
	wt := filepath.Join(config.Dir(), "worktrees", parent, "w1")
	if err != nil || !strings.Contains(out, "worktree "+wt+" on branch atto/p1/w1") {
		t.Fatalf("start: %q %v", out, err)
	}
	if out, err := runAgent(t, "wait", "w1", "-timeout", "30s", "-session", parent); err != nil || !strings.Contains(out, "made it") {
		t.Fatalf("wait: %q %v", out, err)
	}
	st, err := subagent.Load(parent, "w1")
	if err != nil || st.Worktree != wt || st.Branch != "atto/p1/w1" || st.Cwd != filepath.Join(wt, "sub") {
		t.Fatalf("state: %+v %v", st, err)
	}
	// Its session and commands work in the worktree, at the parent's place in it.
	p, err := session.Find(st.Session)
	if err != nil {
		t.Fatal(err)
	}
	if h, _, err := session.Load(p); err != nil || h.Cwd != st.Cwd {
		t.Fatalf("session cwd: %q %v", h.Cwd, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "sub", "made.txt")); err != nil {
		t.Fatalf("not made in the worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "sub", "made.txt")); !os.IsNotExist(err) {
		t.Fatalf("made in the parent's checkout: %v", err)
	}
	if got := mustGit(t, wt, "branch", "--show-current"); got != "atto/p1/w1" {
		t.Fatalf("worktree branch %q", got)
	}
	// It is told where it works and to commit there.
	if b := bodies(); !strings.Contains(b[0], "own git worktree") || !strings.Contains(b[0], "on branch atto/p1/w1") || !strings.Contains(b[0], "commit your work there") {
		t.Fatalf("subagent prompt: %s", b[0])
	}

	// report, -json and list show the worktree and branch.
	if out, _ := runAgent(t, "report", "w1", "-session", parent); !strings.Contains(out, "worktree "+wt+" · branch atto/p1/w1") {
		t.Fatalf("report: %q", out)
	}
	out, _ = runAgent(t, "report", "w1", "-json", "-session", parent)
	var r struct{ Worktree, Branch string }
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Worktree != wt || r.Branch != "atto/p1/w1" {
		t.Fatalf("report -json: %q %v", out, err)
	}
	if out, _ := runAgent(t, "list", "-session", parent); !strings.Contains(out, "w1: worktree "+wt+" · branch atto/p1/w1") {
		t.Fatalf("list: %q", out)
	}

	// Names and branches are not reused.
	if _, err := runAgent(t, "start", "w1", "general", "again", "-worktree", "-session", parent); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("second start: %v", err)
	}
	mustGit(t, repo, "branch", "atto/p1/w2")
	if _, err := runAgent(t, "start", "w2", "general", "x", "-worktree", "-session", parent); err == nil || !strings.Contains(err.Error(), "branch atto/p1/w2 exists") {
		t.Fatalf("branch collision: %v", err)
	}
	if _, err := subagent.Load(parent, "w2"); err == nil {
		t.Fatal("a refused subagent was saved")
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), "worktrees", parent, "w2")); !os.IsNotExist(err) {
		t.Fatalf("a refused worktree was made: %v", err)
	}

	// rm refuses while the worktree has uncommitted changes...
	if _, err := runAgent(t, "rm", "w1", "-session", parent); err == nil || !strings.Contains(err.Error(), "uncommitted changes") ||
		!strings.Contains(err.Error(), "made.txt") || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("rm dirty: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("dirty worktree removed: %v", err)
	}
	// ...and once they are committed removes it and keeps the branch.
	mustGit(t, wt, "add", ".")
	mustGit(t, wt, "commit", "-q", "-m", "made")
	out, err = runAgent(t, "rm", "w1", "-session", parent)
	if err != nil || !strings.Contains(out, "branch atto/p1/w1 kept (1 new commit): merge it with git merge atto/p1/w1") || !strings.Contains(out, "removed subagent w1") {
		t.Fatalf("rm clean: %q %v", out, err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree kept: %v", err)
	}
	if !hasBranch(repo, "atto/p1/w1") {
		t.Fatal("branch removed")
	}

	// rm -done skips a dirty worktree; -force removes it.
	if _, err := runAgent(t, "start", "w3", "general", "make", "a", "file", "-worktree", "-session", parent); err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "wait", "w3", "-timeout", "30s", "-session", parent); err != nil {
		t.Fatal(err)
	}
	wt3 := filepath.Join(config.Dir(), "worktrees", parent, "w3")
	if _, err := runAgent(t, "rm", "-done", "-session", parent); err == nil || !strings.Contains(err.Error(), "subagent w3") || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("rm -done dirty: %v", err)
	}
	if _, err := subagent.Load(parent, "w3"); err != nil {
		t.Fatalf("dirty subagent removed: %v", err)
	}
	out, err = runAgent(t, "rm", "-done", "-force", "-session", parent)
	if err != nil || !strings.Contains(out, "branch atto/p1/w3 kept (0 new commits)") || !strings.Contains(out, "removed subagent w3") {
		t.Fatalf("rm -done -force: %q %v", out, err)
	}
	if _, err := os.Stat(wt3); !os.IsNotExist(err) {
		t.Fatalf("forced worktree kept: %v", err)
	}
	if !hasBranch(repo, "atto/p1/w3") {
		t.Fatal("branch removed")
	}
	if out := mustGit(t, repo, "worktree", "list"); strings.Contains(out, "w3") || strings.Contains(out, "w1") {
		t.Fatalf("worktrees still registered: %s", out)
	}
}

func TestAgentWorktreeNeedsGit(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("ok") })
	t.Chdir(t.TempDir())
	enableSubagents(t, "")
	if _, err := runAgent(t, "start", "a", "general", "x", "-worktree", "-session", "p1"); err == nil || !strings.Contains(err.Error(), "needs a git repository") {
		t.Fatalf("outside git: %v", err)
	}
	if _, err := subagent.Load("p1", "a"); err == nil {
		t.Fatal("subagent saved")
	}
}

func TestWorktreeMentioned(t *testing.T) {
	if !strings.Contains(agentUsage, "-worktree") {
		t.Error("usage does not mention -worktree")
	}
	if got := prompts.Render("subagent_parent", map[string]any{"Presets": ""}); !strings.Contains(got, "add -worktree to give it its own git worktree and branch") {
		t.Errorf("parent prompt: %q", got)
	}
	if got := prompts.Render("subagent", prompts.Subagent{Name: "w", Preset: "p"}); strings.Contains(got, "worktree") {
		t.Errorf("worktree clause without one: %q", got)
	}
}
