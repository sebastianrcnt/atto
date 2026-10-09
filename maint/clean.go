package maint

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sebastianrcnt/atto/session"
)

var runtimeProcesses = otherAttoProcesses

type CleanOptions struct {
	Root, Temp  string
	Older       time.Duration
	Now         time.Time
	Out         io.Writer
	Confirm     func(string, bool) bool
	Yes, DryRun bool
}
type Item struct {
	Category, Path, Reason string
	Count                  int
	Bytes                  int64
	Worktree               *Worktree
	Session                string
}
type Plan struct {
	Root, Temp  string
	Items, Kept []Item
}

func size(p string) (int, int64) {
	count := 0
	var bytes int64
	_ = filepath.WalkDir(p, func(p string, d fs.DirEntry, e error) error {
		if e == nil && !d.IsDir() {
			st, e := d.Info()
			if e == nil {
				count++
				bytes += st.Size()
			}
		}
		return nil
	})
	return count, bytes
}
func cleanDefaults(o CleanOptions) CleanOptions {
	if o.Root == "" {
		o.Root = configRoot()
	}
	if o.Temp == "" {
		o.Temp = os.TempDir()
	}
	if o.Older == 0 {
		o.Older = 30 * 24 * time.Hour
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	return o
}
func (p *Plan) add(cat, path, reason string, w *Worktree, sid string) {
	if reason == "" && !noSymlinkParents(p.boundary(path), path) {
		reason = "symlink ancestor (kept)"
	}
	count, bytes := size(path)
	if count == 0 {
		count = 1
	}
	i := Item{cat, path, reason, count, bytes, w, sid}
	if reason != "" {
		p.Kept = append(p.Kept, i)
	} else {
		p.Items = append(p.Items, i)
	}
}
func older(p string, now time.Time, d time.Duration) bool {
	st, e := os.Lstat(p)
	return e == nil && now.Sub(st.ModTime()) > d
}
func sessionBusy(root, id, p string) bool {
	if p == "" && id != "" {
		busy := false
		for _, dir := range []string{"sessions", "archived_sessions"} {
			_ = filepath.WalkDir(filepath.Join(root, dir), func(file string, d fs.DirEntry, e error) error {
				if e == nil && !d.IsDir() && d.Name() == id+".lock" && busyLock(file) {
					busy = true
				}
				return nil
			})
		}
		if busy {
			return true
		}
	}
	if p != "" {
		if _, busy := session.LockedBy(p); busy {
			return true
		}
	}
	busy := false
	_ = filepath.WalkDir(filepath.Join(root, "jobs", id), func(p string, d fs.DirEntry, e error) error {
		if e == nil && d.Name() == "job.json" {
			j, e := readJob(p)
			if e != nil || jobActive(j) {
				busy = true
			}
		}
		return nil
	})
	return busy
}

// PlanClean inventories only known leftovers. It does not create files or mutate state.
func PlanClean(o CleanOptions) (p Plan, err error) {
	o = cleanDefaults(o)
	p.Root, p.Temp = o.Root, o.Temp
	if o.Older < 0 {
		return p, errors.New("-older must be positive")
	}
	if e := safeRoot(o.Root); e != nil {
		return p, e
	}
	probe := fileProbe()
	sessions, e := sessionPaths(o.Root)
	if e != nil {
		return p, e
	}
	for _, cat := range []string{"outputs", "debug"} {
		dir := filepath.Join(o.Root, cat)
		e = filepath.WalkDir(dir, func(file string, d fs.DirEntry, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if d.IsDir() || d.Type()&os.ModeSymlink != 0 || !older(file, o.Now, o.Older) {
				return nil
			}
			sid := ""
			if cat == "outputs" {
				rel, _ := filepath.Rel(dir, file)
				parts := strings.Split(filepath.ToSlash(rel), "/")
				if len(parts) < 2 {
					return nil
				}
				sid = parts[0]
				if sessions[sid] != "" {
					return nil
				}
			}
			reason := ""
			if cat == "debug" && runtimeProcesses() {
				reason = "active atto process"
			}
			if sessionBusy(o.Root, sid, sessions[sid]) || probe(file) {
				reason = "in use (or unable to prove idle)"
			}
			p.add(cat, file, reason, nil, sid)
			return nil
		})
		if e != nil {
			return p, e
		}
	}
	dirs, e := os.ReadDir(filepath.Join(o.Root, "jobs"))
	if e != nil && !os.IsNotExist(e) {
		return p, e
	}
	for _, d := range dirs {
		if !d.IsDir() || sessions[d.Name()] != "" {
			continue
		}
		file := filepath.Join(o.Root, "jobs", d.Name())
		reason := ""
		if sessionBusy(o.Root, d.Name(), "") || probe(file) {
			reason = "running job or open files"
		}
		p.add("jobs", file, reason, nil, d.Name())
	}
	records, e := Worktrees(o.Root)
	if e != nil {
		return p, e
	}
	for _, w := range records {
		if _, e := os.Stat(w.Path); os.IsNotExist(e) {
			continue
		}
		if !within(filepath.Join(o.Root, "worktrees"), w.Path) {
			continue
		}
		if w.StateFile != "" && !w.Closed {
			continue
		}
		if w.Closed {
			if _, e := git(w.Repo, "rev-parse", "--verify", "refs/heads/"+w.Branch); e == nil {
				if _, e = git(w.Repo, "merge-base", "--is-ancestor", w.Branch, "HEAD"); e != nil {
					p.add("worktrees", w.Path, "closed branch is unmerged", &w, w.Session)
					continue
				}
			}
		}
		reason := ""
		if sessionBusy(o.Root, w.Session, sessions[w.Session]) || sessionBusy(o.Root, w.Parent, sessions[w.Parent]) || probe(w.Path) {
			reason = "in use"
		}
		status, e := git(w.Path, "status", "--porcelain")
		if e != nil {
			reason = "cannot inspect git status"
		} else if status != "" {
			reason = "dirty worktree (kept; merge/save changes first)"
		}
		p.add("worktrees", w.Path, reason, &w, w.Session)
	}
	refs, e := states(o.Root)
	if e != nil {
		return p, e
	}
	parents := map[string]bool{}
	for _, w := range refs {
		if !w.Closed {
			parents[w.Parent] = true
		}
	}
	// Worktree-less agents also keep their external parent alive.
	for _, layout := range []string{"agent-state", "subagents"} {
		_ = filepath.WalkDir(filepath.Join(o.Root, layout), func(file string, d fs.DirEntry, e error) error {
			if e == nil && !d.IsDir() && strings.HasSuffix(file, ".json") && !strings.HasSuffix(file, ".turn.json") && !strings.Contains(filepath.ToSlash(file), "/_closed/") && !strings.Contains(filepath.ToSlash(file), "/_up/") && !strings.Contains(filepath.ToSlash(file), "/.coord/") {
				b, _ := os.ReadFile(file)
				var s struct {
					Parent  string `json:"parent"`
					Session string `json:"session"`
				}
				if json.Unmarshal(b, &s) == nil && s.Session != "" {
					parents[s.Parent] = true
				}
			}
			return nil
		})
	}
	externalMappings := map[string][]string{}
	maps, _ := os.ReadDir(filepath.Join(o.Root, "external_parents"))
	for _, d := range maps {
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			continue
		}
		file := filepath.Join(o.Root, "external_parents", d.Name())
		b, e := os.ReadFile(file)
		if e == nil {
			id := strings.TrimSpace(string(b))
			externalMappings[id] = append(externalMappings[id], file)
		}
	}
	for id, file := range sessions {
		if parents[id] {
			continue
		}
		header, msgs, e := session.Load(file)
		isEmptyMapped := false
		if st, statErr := os.Lstat(file); statErr == nil && st.Size() == 0 && len(externalMappings[id]) > 0 {
			isEmptyMapped = true
		}
		if !isEmptyMapped && (e != nil || !header.External) {
			continue
		}
		messages := 0
		for _, m := range msgs {
			if m.Type == session.TypeMessage {
				messages++
			}
		}
		if messages != 0 {
			continue
		}
		reason := ""
		if sessionBusy(o.Root, id, file) || probe(file) {
			reason = "session in use"
		}
		p.add("empty external sessions", file, reason, nil, id)
		for _, mapping := range externalMappings[id] {
			p.add("external parent mappings", mapping, reason, nil, id)
		}
	}
	e = filepath.WalkDir(filepath.Join(o.Root, "run"), func(file string, d fs.DirEntry, e error) error {
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if d.IsDir() || !strings.HasSuffix(file, ".sock") {
			return nil
		}
		reason := ""
		if socketLive(file) {
			reason = "socket has listener or status unknown"
		}
		p.add("stale sockets", file, reason, nil, "")
		return nil
	})
	if e != nil {
		return p, e
	}
	temps, e := os.ReadDir(o.Temp)
	if e != nil {
		return p, e
	}
	active := runtimeProcesses()
	for _, d := range temps {
		name := d.Name()
		file := filepath.Join(o.Temp, name)
		if !owned(file) || d.Type()&os.ModeSymlink != 0 {
			continue
		}
		cat := ""
		test := strings.HasPrefix(name, "atto-home") || strings.HasPrefix(name, "atto-session-test")
		switch {
		case test:
			if !older(file, o.Now, 24*time.Hour) {
				continue
			}
			cat = "test temp directories"
		case strings.HasPrefix(name, "atto-bash-") && strings.HasSuffix(name, ".log"), strings.HasPrefix(name, "atto-transcript-"), strings.HasPrefix(name, "atto-view-"):
			cat = "runtime temp files"
		case strings.HasPrefix(name, "atto-mcp-") && strings.HasSuffix(name, ".sock"):
			reason := ""
			if active || socketLive(file) {
				reason = "active process/listener"
			}
			p.add("temp sockets", file, reason, nil, "")
			continue
		case name == privateSocketDirName() && d.IsDir():
			entries, _ := os.ReadDir(file)
			for _, s := range entries {
				if strings.HasSuffix(s.Name(), ".sock") {
					sock := filepath.Join(file, s.Name())
					reason := ""
					if active || socketLive(sock) {
						reason = "active process/listener"
					}
					p.add("temp sockets", sock, reason, nil, "")
				}
			}
			continue
		default:
			continue
		}
		reason := ""
		if active || probe(file) || strings.HasSuffix(name, ".sock") && socketLive(file) {
			reason = "active atto process or open files"
		}
		p.add(cat, file, reason, nil, "")
	}
	sort.Slice(p.Items, func(i, j int) bool {
		if p.Items[i].Category == p.Items[j].Category {
			return p.Items[i].Path < p.Items[j].Path
		}
		return p.Items[i].Category < p.Items[j].Category
	})
	return p, nil
}
func (p Plan) Print(out io.Writer) {
	out = writer(out)
	type total struct {
		count int
		bytes int64
	}
	totals := map[string]total{}
	for _, i := range p.Items {
		v := totals[i.Category]
		v.count += i.Count
		v.bytes += i.Bytes
		totals[i.Category] = v
	}
	keys := []string{}
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "CATEGORY\tCOUNT\tBYTES")
	for _, k := range keys {
		v := totals[k]
		fmt.Fprintf(t, "%s\t%d\t%d\n", k, v.count, v.bytes)
	}
	t.Flush()
	for _, i := range p.Kept {
		fmt.Fprintf(out, "Kept %s: %s\n", i.Path, i.Reason)
	}
}
func Clean(o CleanOptions) (Plan, error) {
	o = cleanDefaults(o)
	p, e := PlanClean(o)
	if e != nil {
		return p, e
	}
	p.Print(o.Out)
	if o.DryRun || len(p.Items) == 0 {
		return p, nil
	}
	if !o.Yes && (o.Confirm == nil || !o.Confirm("Remove these leftovers?", false)) {
		fmt.Fprintln(writer(o.Out), "Nothing removed.")
		return p, nil
	}
	// Re-inventory after confirmation: a newly active item must not be removed.
	fresh, e := PlanClean(o)
	if e != nil {
		return p, e
	}
	allowed := map[string]bool{}
	for _, i := range fresh.Items {
		allowed[i.Path] = true
	}
	var failures []error
	removed := 0
	var bytes int64
	for _, i := range p.Items {
		if !allowed[i.Path] {
			fmt.Fprintln(writer(o.Out), "Kept newly active item:", i.Path)
			continue
		}
		if i.Worktree != nil {
			_, e = git(i.Worktree.Repo, "worktree", "remove", i.Path)
		} else {
			e = removeTree(i.Path)
		}
		if e != nil {
			fmt.Fprintf(writer(o.Out), "Could not remove %s: %v\n", i.Path, e)
			failures = append(failures, e)
		} else {
			removed += i.Count
			bytes += i.Bytes
		}
	}
	fmt.Fprintf(writer(o.Out), "Removed %d leftovers (%d bytes). Sessions and settings retained, except unused zero-message external orchestration sessions.\n", removed, bytes)
	return p, errors.Join(failures...)
}
func removeTree(p string) error {
	st, e := os.Lstat(p)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !st.IsDir() {
		return os.Remove(p)
	}
	_ = filepath.WalkDir(p, func(p string, d fs.DirEntry, e error) error {
		if e == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		} else if e == nil && d.Type().IsRegular() {
			_ = os.Chmod(p, 0o600)
		}
		return nil
	})
	return os.RemoveAll(p)
}
func Prompt(in io.Reader, out io.Writer) func(string, bool) bool {
	reader := bufio.NewReader(in)
	return func(message string, yes bool) bool {
		suffix := "[y/N]"
		if yes {
			suffix = "[Y/n]"
		}
		fmt.Fprintf(out, "%s %s ", message, suffix)
		line, e := reader.ReadString('\n')
		if e != nil {
			return false
		}
		line = strings.ToLower(strings.TrimSpace(line))
		return line == "y" || line == "yes" || line == "" && yes
	}
}

func (p Plan) boundary(file string) string {
	if rawWithin(p.Root, file) || within(p.Root, file) {
		return p.Root
	}
	return p.Temp
}
