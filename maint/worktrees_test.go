package maint

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
)

func repoFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.test"}, {"config", "user.name", "Test"}} {
		if _, e := git(repo, args...); e != nil {
			t.Fatal(e)
		}
	}
	put(t, filepath.Join(repo, "base"), []byte("base"))
	if _, e := git(repo, "add", "."); e != nil {
		t.Fatal(e)
	}
	if _, e := git(repo, "commit", "-m", "Initial"); e != nil {
		t.Fatal(e)
	}
	return repo
}
func TestOrphanWorktreesCleanAndDirty(t *testing.T) {
	idleTemps(t)
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	repo := repoFixture(t)
	clean := filepath.Join(root, "worktrees/parent/clean")
	dirty := filepath.Join(root, "worktrees/parent/dirty")
	for _, p := range []string{clean, dirty} {
		os.MkdirAll(filepath.Dir(p), 0o700)
		if _, e := git(repo, "worktree", "add", "-b", "atto/parent/"+filepath.Base(p), p); e != nil {
			t.Fatal(e)
		}
	}
	put(t, filepath.Join(dirty, "untracked"), []byte("keep"))
	var out bytes.Buffer
	plan, e := Clean(CleanOptions{Root: root, Temp: t.TempDir(), Yes: true, Out: &out})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(clean); !os.IsNotExist(e) {
		t.Fatal("clean orphan kept")
	}
	if _, e = os.Stat(dirty); e != nil {
		t.Fatal("dirty orphan deleted")
	}
	if len(plan.Kept) != 1 || !strings.Contains(out.String(), "dirty worktree") {
		t.Fatalf("%+v %s", plan, out.String())
	}
	listed, e := git(repo, "worktree", "list", "--porcelain")
	if e != nil || strings.Contains(listed, clean) {
		t.Fatal("still registered", listed, e)
	}
	if _, e = git(repo, "rev-parse", "--verify", "refs/heads/atto/parent/clean"); e != nil {
		t.Fatal("branch not preserved", e)
	}
}
func TestBackupRecordsAndRestoreRecreatesWorktree(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	repo := repoFixture(t)
	w := filepath.Join(root, "worktrees/parent/worker")
	os.MkdirAll(filepath.Dir(w), 0o700)
	branch := "atto/parent/worker"
	if _, e := git(repo, "worktree", "add", "-b", branch, w); e != nil {
		t.Fatal(e)
	}
	base, _ := git(repo, "rev-parse", "HEAD")
	state := agentstate.State{Name: "worker", Parent: "parent", Session: "child", Repo: repo, Worktree: w, Cwd: w, Branch: branch, Base: base}
	b, _ := json.Marshal(state)
	put(t, filepath.Join(root, "agent-state/parent/worker.json"), b)
	file := filepath.Join(t.TempDir(), "backup.tar.zst")
	m, e := Backup(BackupOptions{Root: root, Output: file})
	if e != nil {
		t.Fatal(e)
	}
	if len(m.Worktrees) != 1 || m.Worktrees[0].Branch != branch || m.Worktrees[0].Base != base || m.Worktrees[0].Repo != repo {
		t.Fatal(m.Worktrees)
	}
	if _, e = git(repo, "worktree", "remove", w); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if _, e = Restore(file, RestoreOptions{Root: dest}); e != nil {
		t.Fatal(e)
	}
	if _, e = Restore("", RestoreOptions{Root: dest, Worktrees: true}); e != nil {
		t.Fatal(e)
	}
	newPath := filepath.Join(dest, "worktrees/parent/worker")
	if _, e = os.Stat(filepath.Join(newPath, "base")); e != nil {
		t.Fatal(e)
	}
	b, e = os.ReadFile(filepath.Join(dest, "agent-state/parent/worker.json"))
	if e != nil {
		t.Fatal(e)
	}
	json.Unmarshal(b, &state)
	if state.Worktree != newPath || state.Cwd != newPath {
		t.Fatal("state not relocated", state)
	}
	git(repo, "worktree", "remove", newPath)
}
func TestRestoreMissingWorktreeRepository(t *testing.T) {
	var out bytes.Buffer
	e := RecreateWorktrees(t.TempDir(), []Worktree{{Repo: filepath.Join(t.TempDir(), "missing"), Branch: "atto/parent/name", Parent: "parent", Name: "name"}}, &out)
	if e != nil || !strings.Contains(out.String(), "Skipped missing repository") {
		t.Fatal(e, out.String())
	}
}
