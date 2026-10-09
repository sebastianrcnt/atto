package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
)

// An agent started with -worktree works in a git worktree of its own,
// on a new branch made from the spawning checkout's HEAD, so agents editing
// files at the same time don't clobber each other or that checkout.
//
// The worktree lives under the atto dir (<atto dir>/worktrees/<session ID>),
// not in the project: nothing shows up in the project's git status, editor
// or searches, no .gitignore entry is needed, and the path stays short on
// Windows. The branch is atto/<session ID>. The ID is chosen before any git
// work, and never reused, so closing an agent and reusing its name cannot
// collide with the branch it kept. Worktrees and branches of agents started
// before (worktrees/<parent>/<name>, atto/<parent>/<name>) keep their paths
// and names: they are recorded with the agent and always used from there.

// worktree is where a new agent's worktree goes.
type worktree struct {
	Repo   string // the spawning checkout's repository (top level)
	Path   string // the worktree
	Cwd    string // the agent's directory in it: the parent's, relatively
	Branch string
	Base   string // the commit it starts from
}

// git runs git in dir and returns its trimmed output; an error carries
// what git said.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func worktreeBranch(id string) string { return "atto/" + id }

func worktreePath(id string) string { return filepath.Join(config.Dir(), "worktrees", id) }

// planWorktree checks that a worktree for the agent with session id can be
// made from cwd's repository and says where it would go.
func planWorktree(cwd, id string) (worktree, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return worktree{}, fmt.Errorf("-worktree needs git: %v", err)
	}
	repo, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return worktree{}, fmt.Errorf("-worktree needs a git repository, and %s is not in one", cwd)
	}
	repo = filepath.FromSlash(repo)
	prefix, _ := git(cwd, "rev-parse", "--show-prefix")
	base, err := git(cwd, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil || base == "" {
		return worktree{}, fmt.Errorf("-worktree: the repository at %s has no commits yet", repo)
	}
	w := worktree{Repo: repo, Path: worktreePath(id), Branch: worktreeBranch(id), Base: base}
	w.Cwd = filepath.Join(w.Path, filepath.FromSlash(prefix))
	if _, err := git(cwd, "check-ref-format", "--branch", w.Branch); err != nil {
		return worktree{}, fmt.Errorf("-worktree: %q is not a valid branch name", w.Branch)
	}
	if _, err := git(cwd, "rev-parse", "--verify", "--quiet", "refs/heads/"+w.Branch); err == nil {
		return worktree{}, fmt.Errorf("-worktree: branch %s exists: merge or delete it (git branch -D %s)", w.Branch, w.Branch)
	}
	if _, err := os.Stat(w.Path); err == nil {
		return worktree{}, fmt.Errorf("-worktree: %s exists: remove it (git worktree remove %s)", w.Path, w.Path)
	}
	return w, nil
}

// create makes the worktree and its branch.
func (w worktree) create() error {
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o755); err != nil {
		return err
	}
	if _, err := git(w.Repo, "worktree", "add", "-b", w.Branch, w.Path, w.Base); err != nil {
		return fmt.Errorf("-worktree: %w", err)
	}
	return nil
}

// undo removes a worktree just made, branch and all, after start failed.
func (w worktree) undo() {
	_, _ = git(w.Repo, "worktree", "remove", "--force", w.Path)
	_, _ = git(w.Repo, "branch", "-D", w.Branch)
	pruneWorktreeParent(w.Path)
}

// pruneWorktreeParent removes the directory of a worktree made under a
// parent's name (the old layout) once it is empty. Never worktrees/ itself.
func pruneWorktreeParent(path string) {
	parent := filepath.Dir(path)
	if parent != filepath.Join(config.Dir(), "worktrees") {
		_ = os.Remove(parent)
	}
}

func worktreeDirt(st agentstate.State) (string, error) { return agentstate.WorktreeDirt(st) }
func removeWorktree(st agentstate.State, force bool) (string, error) {
	return agentstate.RemoveWorktree(st, force)
}
