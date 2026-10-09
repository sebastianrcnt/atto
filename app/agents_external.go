package app

import (
	"path/filepath"
	"runtime"
	"strings"
)

// centerShellParents presents external-parent trees as one shell group per
// project. The real IDs and ancestry in c.items and on disk stay untouched;
// only this tree-rendering copy reparents the group's direct children.
func centerShellParents(items []centerItem) []centerItem {
	groups := map[string]centerItem{}
	project := func(cwd string) string {
		key := filepath.Clean(cwd)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		return key
	}
	for _, it := range items {
		if !it.external || it.parent != "" {
			continue
		}
		key := project(it.cwd)
		previous, ok := groups[key]
		if !ok || (it.current && !previous.current) || (it.current == previous.current && it.id < previous.id) {
			groups[key] = it
		}
	}
	aliases := map[string]string{}
	for _, it := range items {
		if it.external && it.parent == "" {
			aliases[it.id] = groups[project(it.cwd)].id
		}
	}
	out := make([]centerItem, 0, len(items))
	for _, it := range items {
		if id, ok := aliases[it.id]; ok && id != it.id {
			continue
		}
		if id, ok := aliases[it.parent]; ok {
			it.parent = id
		}
		out = append(out, it)
	}
	return out
}
