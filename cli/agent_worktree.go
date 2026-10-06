package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/subagent"
)

// A subagent started with -worktree works in a git worktree of its own,
// on a new branch made from the parent's HEAD, so subagents editing files
// at the same time don't clobber each other or the parent's checkout.
//
// The worktree lives under the atto dir (<atto dir>/worktrees/<parent>/
// <name>), not in the project: nothing shows up in the parent's git
// status, editor or searches, no .gitignore entry is needed, and the path
// stays short on Windows. The branch is atto/<parent>/<name>: session IDs
// are short, and the parent's part keeps names reused by other sessions
// (or after rm, which keeps the branch) from colliding.

// worktree is where a new subagent's worktree goes.
type worktree struct {
	Repo   string // the parent's repository (top level)
	Path   string // the worktree
	Cwd    string // the subagent's directory in it: the parent's, relatively
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

func worktreeBranch(parent, name string) string { return "atto/" + parent + "/" + name }

func worktreePath(parent, name string) string {
	return filepath.Join(config.Dir(), "worktrees", parent, name)
}

// planWorktree checks that a worktree for subagent name can be made from
// cwd's repository and says where it would go.
func planWorktree(cwd, parent, name string) (worktree, error) {
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
	w := worktree{Repo: repo, Path: worktreePath(parent, name), Branch: worktreeBranch(parent, name), Base: base}
	w.Cwd = filepath.Join(w.Path, filepath.FromSlash(prefix))
	if _, err := git(cwd, "check-ref-format", "--branch", w.Branch); err != nil {
		return worktree{}, fmt.Errorf("-worktree: %q is not a valid branch name", w.Branch)
	}
	if _, err := git(cwd, "rev-parse", "--verify", "--quiet", "refs/heads/"+w.Branch); err == nil {
		return worktree{}, fmt.Errorf("-worktree: branch %s exists (from an earlier subagent?): merge or delete it (git branch -D %s), or pick another name", w.Branch, w.Branch)
	}
	if _, err := os.Stat(w.Path); err == nil {
		return worktree{}, fmt.Errorf("-worktree: %s exists: remove it (git worktree remove %s), or pick another name", w.Path, w.Path)
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
	_ = os.Remove(filepath.Dir(w.Path))
}

// worktreeDirt is what keeps st's worktree from being removed: git
// status lines, at most a few, "" when clean or gone.
func worktreeDirt(st subagent.State) (string, error) {
	if st.Worktree == "" {
		return "", nil
	}
	if _, err := os.Stat(st.Worktree); os.IsNotExist(err) {
		return "", nil
	}
	out, err := git(st.Worktree, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", nil
	}
	lines := strings.Split(out, "\n")
	if len(lines) > 10 {
		lines = append(lines[:10], fmt.Sprintf("... and %d more", len(lines)-10))
	}
	return strings.Join(lines, "\n"), nil
}

// removeWorktree removes st's worktree (forcing past uncommitted changes
// when force) and keeps its branch. It returns a line about the branch.
func removeWorktree(st subagent.State, force bool) (string, error) {
	if _, err := os.Stat(st.Worktree); err == nil {
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		if _, err := git(st.Repo, append(args, st.Worktree)...); err != nil {
			return "", err
		}
	} else {
		_, _ = git(st.Repo, "worktree", "prune")
	}
	_ = os.Remove(filepath.Dir(st.Worktree)) // the parent's directory, once empty
	if _, err := git(st.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+st.Branch); err != nil {
		return fmt.Sprintf("worktree %s removed; its branch %s is gone", st.Worktree, st.Branch), nil
	}
	commits := ""
	if st.Base != "" {
		if n, err := git(st.Repo, "rev-list", "--count", st.Base+".."+st.Branch); err == nil {
			commits = fmt.Sprintf(" (%s new commits)", n)
			if n == "1" {
				commits = " (1 new commit)"
			}
		}
	}
	return fmt.Sprintf("worktree %s removed; branch %s kept%s: merge it with git merge %s", st.Worktree, st.Branch, commits, st.Branch), nil
}
