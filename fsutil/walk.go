package fsutil

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Entry is a file or directory found by ListFiles.
type Entry struct {
	Path string // relative to the root, with "/" separators on every OS
	Dir  bool
}

// ListOptions bounds ListFiles.
type ListOptions struct {
	MaxEntries int // 0 means 50000
	MaxDepth   int // directory levels below the root; 0 means 20
	// Progress, if set, is called from time to time with the entries found
	// so far. The slice is only ever appended to: the caller may keep it
	// and read it while the walk goes on, but not change it.
	Progress func([]Entry)
}

// ListFiles lists the files and directories under root, shallowest first
// (breadth-first, by name within a directory), as the "@" file list wants
// them. Like pi's file search (fd --hidden) it includes hidden files,
// always skips .git, and respects .gitignore: the root's and nested ones,
// those of parent directories up to the repository root, and
// .git/info/exclude. Unreadable directories are skipped. Symlinks are
// listed; a symlinked directory is entered only if it leads outside root
// and has not been entered before, so links cannot loop. truncated reports
// that the entry cap cut the list short. The walk stops early, with what
// it found, when ctx is done.
func ListFiles(ctx context.Context, root string, opt ListOptions) (entries []Entry, truncated bool) {
	maxEntries, maxDepth := opt.MaxEntries, opt.MaxDepth
	if maxEntries <= 0 {
		maxEntries = 50000
	}
	if maxDepth <= 0 {
		maxDepth = 20
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	prefix, inherited := repoIgnores(root)

	type dir struct {
		abs, rel string // rel: slash path from root, "" for the root
		depth    int
		rules    []ignoreRule
	}
	queue := []dir{{abs: root, rules: inherited}}
	visited := map[string]bool{}
	reported := 0
	for len(queue) > 0 {
		if ctx.Err() != nil {
			break
		}
		d := queue[0]
		queue = queue[1:]
		repoRel := path.Join(prefix, d.rel) // the directory, from the repository root
		if repoRel == "." {
			repoRel = ""
		}
		rules := d.rules
		if own := readIgnore(filepath.Join(d.abs, ".gitignore"), repoRel); len(own) > 0 {
			rules = append(slices.Clip(rules), own...)
		}
		list, err := os.ReadDir(d.abs)
		if err != nil {
			continue
		}
		for _, de := range list {
			name := de.Name()
			if name == ".git" {
				continue
			}
			rel := name
			if d.rel != "" {
				rel = d.rel + "/" + name
			}
			isDir, link := de.IsDir(), de.Type()&os.ModeSymlink != 0
			abs := filepath.Join(d.abs, name)
			if link {
				if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
					isDir = true
				}
			}
			if ignoredBy(rules, path.Join(repoRel, name), isDir) {
				continue
			}
			if len(entries) == maxEntries {
				return entries, true
			}
			entries = append(entries, Entry{Path: rel, Dir: isDir})
			if !isDir || d.depth+1 >= maxDepth {
				continue
			}
			if link {
				real, err := filepath.EvalSymlinks(abs)
				if err != nil || visited[real] || within(real, realRoot) {
					continue
				}
				visited[real] = true
			}
			queue = append(queue, dir{abs: abs, rel: rel, depth: d.depth + 1, rules: rules})
		}
		if opt.Progress != nil && len(entries)-reported >= 500 {
			reported = len(entries)
			opt.Progress(entries)
		}
	}
	return entries, false
}

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// repoIgnores finds the git repository root contains, and returns root's
// slash path from it ("" when root is the repository root or not in one)
// and the ignore rules that apply from above root: .git/info/exclude and
// the .gitignore files of the directories between.
func repoIgnores(root string) (prefix string, rules []ignoreRule) {
	repo := root
	for {
		if _, err := os.Lstat(filepath.Join(repo, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(repo)
		if parent == repo {
			return "", nil // not in a repository: only root's own files
		}
		repo = parent
	}
	rel, err := filepath.Rel(repo, root)
	if err != nil {
		return "", nil
	}
	if rel = filepath.ToSlash(rel); rel == "." {
		rel = ""
	}
	rules = readIgnore(filepath.Join(repo, ".git", "info", "exclude"), "")
	if rel == "" {
		return "", rules
	}
	// The repository root's .gitignore and each one below it down to
	// root's parent; root's own is read by the walk.
	dir, base := repo, ""
	for seg := range strings.SplitSeq(rel, "/") {
		rules = append(rules, readIgnore(filepath.Join(dir, ".gitignore"), base)...)
		dir, base = filepath.Join(dir, seg), path.Join(base, seg)
	}
	return rel, rules
}
