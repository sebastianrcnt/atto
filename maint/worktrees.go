package maint

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/agentstate"
)

type Worktree struct {
	Repo      string `json:"repo"`
	Path      string `json:"path"`
	Branch    string `json:"branch"`
	Base      string `json:"base"`
	Parent    string `json:"parent,omitempty"`
	Name      string `json:"name,omitempty"`
	Session   string `json:"session,omitempty"`
	StateFile string `json:"state_file,omitempty"`
	Closed    bool   `json:"closed,omitempty"`
}

func git(dir string, args ...string) (string, error) {
	b, e := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("git %s: %s (%w)", strings.Join(args, " "), strings.TrimSpace(string(b)), e)
	}
	return strings.TrimSpace(string(b)), nil
}
func states(root string) ([]Worktree, error) {
	var out []Worktree
	for _, layout := range []string{"agent-state", "subagents"} {
		dir := filepath.Join(root, layout)
		st, e := os.Lstat(dir)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			continue
		}
		e = filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() || !strings.HasSuffix(p, ".json") || strings.HasSuffix(p, ".turn.json") || strings.Contains(filepath.ToSlash(p), "/.coord/") {
				return nil
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			var s agentstate.State
			if json.Unmarshal(b, &s) != nil || s.Worktree == "" {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			out = append(out, Worktree{Repo: s.Repo, Path: s.Worktree, Branch: s.Branch, Base: s.Base, Parent: s.Parent, Name: s.Name, Session: s.Session, StateFile: filepath.ToSlash(rel),
				Closed: s.Lifecycle == agentstate.Closed || strings.Contains(filepath.ToSlash(rel), "/_closed/")})
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}

// Worktrees includes records and registered checkouts, even when state vanished.
func Worktrees(root string) ([]Worktree, error) {
	records, e := states(root)
	if e != nil {
		return nil, e
	}
	byPath := map[string]Worktree{}
	repos := map[string]bool{}
	for _, w := range records {
		if w.Repo != "" {
			repos[w.Repo] = true
		}
		byPath[canonical(w.Path)] = w
	}
	dir := filepath.Join(root, "worktrees")
	e = filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if d.Name() != ".git" {
			return nil
		}
		common, e := git(filepath.Dir(p), "rev-parse", "--path-format=absolute", "--git-common-dir")
		if e != nil {
			return nil
		}
		repo := filepath.Dir(filepath.FromSlash(common))
		if filepath.Base(common) == ".git" {
			repos[repo] = true
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	for repo := range repos {
		text, e := git(repo, "worktree", "list", "--porcelain")
		if e != nil {
			continue
		}
		for block := range strings.SplitSeq(text, "\n\n") {
			w := Worktree{Repo: repo}
			for line := range strings.SplitSeq(block, "\n") {
				key, value, _ := strings.Cut(line, " ")
				switch key {
				case "worktree":
					w.Path = filepath.FromSlash(value)
				case "branch":
					w.Branch = strings.TrimPrefix(value, "refs/heads/")
				case "HEAD":
					w.Base = value
				}
			}
			if !within(dir, w.Path) || w.Path == dir {
				continue
			}
			if old, ok := byPath[canonical(w.Path)]; ok {
				if old.Repo == "" {
					old.Repo = w.Repo
				}
				if old.Branch == "" {
					old.Branch = w.Branch
				}
				if old.Base == "" {
					old.Base = w.Base
				}
				byPath[canonical(w.Path)] = old
			} else {
				rel, _ := filepath.Rel(canonical(dir), canonical(w.Path))
				parts := strings.Split(filepath.ToSlash(rel), "/")
				switch len(parts) {
				case 1: // worktrees/<session ID>
					w.Session = parts[0]
				case 2: // worktrees/<parent>/<name>, made before agents were keyed by session
					w.Parent, w.Name = parts[0], parts[1]
				}
				byPath[canonical(w.Path)] = w
			}
		}
	}
	var out []Worktree
	for _, w := range byPath {
		out = append(out, w)
	}
	sortWorktrees(out)
	return out, nil
}

// isLegacyWorktree reports a worktree made under a parent's name
// (worktrees/<parent>/<name>) rather than its session ID.
func isLegacyWorktree(w Worktree) bool {
	return filepath.Base(filepath.Dir(filepath.FromSlash(w.Path))) != "worktrees"
}

func RecreateWorktrees(root string, records []Worktree, out io.Writer) error {
	for _, w := range records {
		if w.Closed {
			fmt.Fprintln(out, "Skipped closed worktree:", w.Branch)
			continue
		}
		legacy := isLegacyWorktree(w)
		valid := safeName(w.Session) && !strings.Contains(w.Session, "/")
		if legacy {
			valid = agentstate.ValidName(w.Name) == nil && safeName(w.Parent) && !strings.Contains(w.Parent, "/")
		}
		if !valid || !strings.HasPrefix(w.Branch, "atto/") {
			fmt.Fprintln(out, "Skipped invalid worktree record:", w.Path)
			continue
		}
		if _, e := os.Stat(w.Repo); e != nil {
			fmt.Fprintln(out, "Skipped missing repository:", w.Repo)
			continue
		}
		if _, e := git(w.Repo, "rev-parse", "--verify", "refs/heads/"+w.Branch); e != nil {
			fmt.Fprintln(out, "Skipped missing branch:", w.Branch)
			continue
		}
		dest := filepath.Join(root, "worktrees", w.Session)
		if legacy {
			dest = filepath.Join(root, "worktrees", w.Parent, w.Name)
		}
		if !noSymlinkParents(root, dest) {
			fmt.Fprintln(out, "Skipped worktree with symlink ancestor:", dest)
			continue
		}
		if e := os.MkdirAll(filepath.Dir(dest), 0o700); e != nil {
			return e
		}
		if _, e := git(w.Repo, "worktree", "add", dest, w.Branch); e != nil {
			fmt.Fprintln(out, "Could not recreate:", e)
			continue
		}
		if w.StateFile != "" && safeName(w.StateFile) {
			p := filepath.Join(root, filepath.FromSlash(w.StateFile))
			if !noSymlinkParents(root, p) {
				return fmt.Errorf("state file has symlink ancestor: %s", p)
			}
			if st, e := os.Lstat(p); e == nil && st.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("state file is a symlink: %s", p)
			}
			b, e := os.ReadFile(p)
			var s agentstate.State
			if e == nil && json.Unmarshal(b, &s) == nil {
				rel, e := filepath.Rel(w.Path, s.Cwd)
				if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					s.Cwd = filepath.Join(dest, rel)
				} else {
					s.Cwd = dest
				}
				s.Worktree = dest
				s.Repo = w.Repo
				b, _ = json.MarshalIndent(s, "", "  ")
				if e = os.WriteFile(p, b, 0o600); e != nil {
					return e
				}
			}
		}
		fmt.Fprintf(out, "Recreated %s on %s\n", dest, w.Branch)
	}
	return nil
}
