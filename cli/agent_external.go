package cli

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
)

// externalProject is the canonical project key used for outside labels.
// No project-to-parent mapping is created: each spawn owns a fresh parent.
func externalProject(cwd string) string {
	root := agent.ProjectRoot(cwd)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
	}
	return root
}

func newExternalParent(out io.Writer) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	w := session.NewExternal(externalProject(cwd))
	w.Append(session.Entry{Type: session.TypeName, Name: "atto agent (external)"})
	w.Close()
	if err := w.Err(); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "external parent created: %s\n", w.ID)
	return w.ID, nil
}

// externalAgents inventories every external-parent tree, including legacy
// shared parents, without migrating state or trusting the old project mapping.
// Project membership comes from the root parent's cwd, not a worktree's cwd.
func externalAgents(all, direct bool) ([]agentstate.State, error) {
	project := ""
	if !all {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		project = externalProject(cwd)
	}
	type parentInfo struct {
		external bool
		project  string
	}
	parents := map[string]parentInfo{}
	var rows []agentstate.State
	for _, st := range agentstate.ListAll() {
		root := agentstate.Root(st.Session)
		if direct && st.Parent != root {
			continue
		}
		info, ok := parents[root]
		if !ok {
			if path, err := session.Find(root); err == nil {
				if h, _, err := session.Load(path); err == nil {
					info = parentInfo{external: h.External, project: externalProject(h.Cwd)}
				}
			}
			parents[root] = info
		}
		if info.external && (all || info.project == project) {
			rows = append(rows, st)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Created.Equal(rows[j].Created) {
			return rows[i].Session < rows[j].Session
		}
		return rows[i].Created.Before(rows[j].Created)
	})
	return rows, nil
}

func resolveExternalAgent(addr string) (agentstate.Target, error) {
	// An outside caller has no implicit root or parent. Absolute agent paths
	// still work by resolving their first child label in the current project.
	if addr == agentstate.RootPath || addr == ".." {
		return agentstate.Target{}, fmt.Errorf("no selected parent session: pass -session PARENT to address %q, or use an agent's @id", addr)
	}
	path := strings.TrimPrefix(addr, agentstate.RootPath+"/")
	first, _, _ := strings.Cut(path, "/")
	if err := agentstate.ValidName(first); err != nil {
		return agentstate.Target{}, err
	}
	rows, err := externalAgents(false, true)
	if err != nil {
		return agentstate.Target{}, err
	}
	var matches []agentstate.State
	for _, st := range rows {
		if st.Name == first {
			matches = append(matches, st)
		}
	}
	switch len(matches) {
	case 0:
		return agentstate.Target{}, fmt.Errorf("%w %q in this project (see atto agent list; atto agent list -all shows other projects)", agentstate.ErrNotFound, first)
	case 1:
		return agentstate.Resolve(matches[0].Parent, path)
	default:
		var candidates []string
		for _, st := range matches {
			candidates = append(candidates, fmt.Sprintf("@%s (%s, %s)", st.Session, st.Latest().Status, externalAgentAge(st.Created)))
		}
		return agentstate.Target{}, fmt.Errorf("%d agents named %s: %s; use @id", len(matches), first, strings.Join(candidates, "; "))
	}
}

func externalAgentAge(created time.Time) string {
	if created.IsZero() {
		return "unknown age"
	}
	age := max(time.Since(created), 0)
	switch {
	case age >= time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	case age >= time.Minute:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	}
}

func forgetExternalParent(parent string) error {
	if parent == "" {
		return nil
	}
	if len(agentstate.List(parent)) != 0 {
		return nil
	}
	p, err := session.Find(parent)
	if err != nil {
		return nil
	} // explicit parents need not have a session file
	h, _, err := session.Load(p)
	if err != nil {
		return err
	}
	if !h.External {
		return nil
	}
	if !isArchived(p) {
		if _, err := session.Archive(p); err != nil {
			return err
		}
	}
	// -session can select an external parent from another project.
	paths, err := filepath.Glob(filepath.Join(config.Dir(), "external_parents", "*"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if len(filepath.Base(path)) != sha256.Size*2 {
			continue
		}
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(b)) == parent {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}
