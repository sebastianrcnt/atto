package agentstate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/config"
)

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

func pruneWorktreeParent(path string) {
	parent := filepath.Dir(path)
	if parent != filepath.Join(config.Dir(), "worktrees") {
		_ = os.Remove(parent)
	}
}

// worktreeDirt is what keeps st's worktree from being removed: git
// status lines, at most a few, "" when clean or gone.
func WorktreeDirt(st State) (string, error) {
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
func RemoveWorktree(st State, force bool) (string, error) {
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
	pruneWorktreeParent(st.Worktree) // the parent's directory of an old-layout worktree, once empty
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
