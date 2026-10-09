package app

import (
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// centerShellParents shows the agents started from a shell under one
// heading per project. The heading is a virtual display group: it has no
// session, no inbox and no effect on ancestry, and the agents under it are
// roots of trees of their own (they have no parent). Only this
// tree-rendering copy of the items gets the heading; c.items is untouched.
func centerShellParents(items []centerItem) []centerItem {
	project := func(it centerItem) string {
		key := it.projectRoot
		if key == "" {
			key = it.cwd
		}
		key = filepath.Clean(key)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		return key
	}
	type group struct {
		id      string
		updated time.Time
	}
	groups := map[string]*group{}
	for _, it := range items {
		if !it.shell || it.parent != "" {
			continue
		}
		key := project(it)
		g := groups[key]
		if g == nil {
			g = &group{id: "shell:" + key}
			groups[key] = g
		}
		if it.updated.After(g.updated) {
			g.updated = it.updated
		}
	}
	if len(groups) == 0 {
		return items
	}
	out := make([]centerItem, 0, len(items)+len(groups))
	placed := map[string]bool{}
	for _, it := range items {
		if it.shell && it.parent == "" {
			key := project(it)
			if !placed[key] {
				placed[key] = true
				g := groups[key]
				out = append(out, centerItem{id: g.id, title: "agents started from a shell", cwd: key, external: true, virtual: true, tab: tabInactive, updated: g.updated})
			}
			it.parent = groups[key].id
		}
		out = append(out, it)
	}
	return out
}
