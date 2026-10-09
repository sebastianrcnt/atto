// Package agentmigrate converts agent data from the layout of one directory
// per parent session (format 1) to one record per agent session (format 2,
// package agentstate). It is deliberately simple: no dual layouts, no
// journal, no draining protocol. It refuses while any agent work runs, takes
// a backup, converts everything in one pass and writes the format marker
// last. On any error it stops before the marker; the user restores the
// backup with atto restore, and running it again is safe.
package agentmigrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/maint"
	"github.com/sebastianrcnt/atto/session"
)

// Options configures a migration.
type Options struct {
	Out     io.Writer
	Version string // atto version, for the backup and the marker
	// Project is the canonical project of a directory (cli's externalProject).
	Project func(cwd string) string
}

// Report says what a migration did.
type Report struct {
	Backup                      string
	Agents, Roots, Closed       int // converted live agents, of which new outside roots; closed records written
	FakeDeleted, FakeKept       int // lightweight external parents removed, and kept as ordinary sessions
	AlreadyCurrent              bool
	RemovedLegacy, HeadersFixed int
}

// ErrBusy is wrapped when agent work is running.
var ErrBusy = errors.New("agent work is running")

func (o *Options) say(format string, args ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, format+"\n", args...)
	}
}

// Busy lists the agent work that is running: turns, the jobs they run as,
// and the session workers of agent sessions. A migration waits for none of it.
func Busy() ([]string, error) {
	l, err := agentstate.ScanLegacy()
	if err != nil {
		return nil, err
	}
	var names []string
	sessions := map[string]bool{}
	check := func(label string, st agentstate.State, t agentstate.Turn) {
		sessions[st.Session] = true
		if t.Status.Active() {
			names = append(names, fmt.Sprintf("agent %s (@%s): turn %d %s", label, st.Session, t.N, t.Status))
		}
		if owner, id := st.JobRef(); id > 0 && owner != "" {
			if j, err := jobs.Get(owner, id); err == nil && j.Active() {
				names = append(names, fmt.Sprintf("job %s/%d: the process of agent %s's turn", owner, id, label))
			}
		}
	}
	for _, a := range l.Agents {
		check(a.State.Name, a.State, a.Latest())
	}
	for _, st := range agentstate.AllWithClosed() {
		if st.Live() {
			check(st.Name, st, st.Latest())
		}
	}
	for id := range sessions {
		for _, j := range jobs.List(id) {
			if j.Active() {
				names = append(names, fmt.Sprintf("job %s/%d (%s): a job of agent session %s", id, j.ID, j.Label(), id))
			}
		}
	}
	workers, err := daemon.Status()
	if err != nil {
		return nil, err
	}
	for _, w := range workers {
		if sessions[w.Session] {
			names = append(names, fmt.Sprintf("worker %d for agent session %s", w.PID, w.Session))
		}
	}
	sort.Strings(names)
	return slices.Compact(names), nil
}

// Run migrates agent data to the current format.
func Run(o Options) (Report, error) {
	var r Report
	layout, why, err := agentstate.Detect()
	if err != nil {
		return r, err
	}
	if layout == agentstate.LayoutNewer {
		return r, fmt.Errorf("%w: %s; upgrade atto", agentstate.ErrNewerFormat, why)
	}
	l, err := agentstate.ScanLegacy()
	if err != nil {
		return r, err
	}
	if layout == agentstate.LayoutCurrent && len(l.Agents) == 0 && len(l.Tombs) == 0 && len(l.Remove) == 0 {
		r.AlreadyCurrent = true
		o.say("agent data already has the current format (%d)", agentstate.FormatVersion)
		return r, nil
	}
	if layout == agentstate.LayoutEmpty && len(l.Agents) == 0 && len(l.Tombs) == 0 && len(l.Remove) == 0 {
		if err := agentstate.WriteMarker(o.Version, ""); err != nil {
			return r, err
		}
		r.AlreadyCurrent = true
		o.say("no agent data to convert; format marker written")
		return r, nil
	}
	if len(l.Problems) > 0 {
		return r, fmt.Errorf("cannot read the old agent data, nothing changed:\n  %s", strings.Join(l.Problems, "\n  "))
	}
	busy, err := Busy()
	if err != nil {
		return r, err
	}
	if len(busy) > 0 {
		return r, fmt.Errorf("%w; wait for it or stop it (atto agent interrupt, atto daemon kill), then run atto agent migrate again:\n  %s", ErrBusy, strings.Join(busy, "\n  "))
	}

	// 1. Backup, before anything changes.
	if err := os.MkdirAll(filepath.Join(config.Dir(), maint.BackupsDir), 0o700); err != nil {
		return r, err
	}
	r.Backup = filepath.Join(config.Dir(), maint.BackupsDir, "atto-before-agent-migration-"+time.Now().Format("20060102-150405.000")+".tar.zst")
	if _, err := maint.Backup(maint.BackupOptions{Output: r.Backup, Version: o.Version, Force: true, Out: o.Out}); err != nil {
		return r, fmt.Errorf("backup failed, nothing changed: %w", err)
	}
	fail := func(err error) (Report, error) {
		return r, fmt.Errorf("%w\nThe format marker was not written. To go back to the old data: atto restore -force %s\nRunning atto agent migrate again is safe.", err, r.Backup)
	}

	// 2. Convert everything.
	c := &converter{o: o, legacy: l, agents: map[string]agentstate.LegacyAgent{}, done: map[string]agentstate.State{}, fakes: map[string]session.Summary{}}
	if err := c.run(&r); err != nil {
		return fail(err)
	}

	// 3. The old layout goes, then the marker, last.
	for _, p := range l.Remove {
		if err := os.RemoveAll(p); err != nil {
			return fail(fmt.Errorf("removing %s: %w", p, err))
		}
		r.RemovedLegacy++
	}
	for _, p := range c.copiedJobs {
		_ = os.RemoveAll(p)
	}
	if err := agentstate.WriteMarker(o.Version, r.Backup); err != nil {
		return fail(err)
	}
	o.say("migrated agent data: %d agents (%d now outside roots), %d closed records, %d empty external parents deleted, %d kept as ordinary sessions", r.Agents, r.Roots, r.Closed, r.FakeDeleted, r.FakeKept)
	return r, nil
}

type converter struct {
	o      Options
	legacy *agentstate.Legacy
	agents map[string]agentstate.LegacyAgent
	done   map[string]agentstate.State
	fakes  map[string]session.Summary // lightweight external parents, by session ID
	// copiedJobs are the old job directories of roots, removed at the end.
	copiedJobs []string
}

func (c *converter) run(r *Report) error {
	for _, a := range c.legacy.Agents {
		c.agents[a.State.Session] = a
	}
	for _, archived := range []bool{false, true} {
		all, err := session.ListAll("", archived)
		if err != nil {
			return err
		}
		for _, s := range all {
			if s.External && !s.IsAgent() {
				c.fakes[s.ID] = s
			}
		}
	}

	// Live agents, parents before children.
	var order []string
	for _, a := range c.legacy.Agents {
		order = append(order, a.State.Session)
	}
	for _, id := range order {
		if _, err := c.convert(id, map[string]bool{}); err != nil {
			return err
		}
	}
	live := 0
	for _, id := range order {
		st := c.done[id]
		if existing, err := agentstate.Load(id); err == nil && c.preserve(existing) {
			continue // a record already in the new format wins
		}
		if err := agentstate.Save(st); err != nil {
			return fmt.Errorf("agent %s: %w", st.Name, err)
		}
		live++
		if st.IsRoot() {
			r.Roots++
		}
		if a := c.agents[id]; a.Turn != nil {
			if err := agentstate.SaveTurn(id, *a.Turn); err != nil {
				return err
			}
		}
	}
	r.Agents = live

	// Headers: the agent object says what the record projects.
	for _, id := range order {
		st := c.done[id]
		path, err := session.Find(id)
		if err != nil {
			c.o.say("note: agent %s has no session file; its record is converted without a header", st.Name)
			continue
		}
		meta := metaOf(st)
		err = session.RewriteHeader(path, func(h *session.Entry) {
			h.Agent = &meta
			h.AgentOf = st.Parent
			h.External = false
		})
		if err != nil {
			if strings.HasSuffix(path, ".zst") {
				c.o.say("note: agent %s's session is a compressed archive; its header keeps agentOf only", st.Name)
				continue
			}
			return fmt.Errorf("agent %s: rewriting its session header: %w", st.Name, err)
		}
		r.HeadersFixed++
	}

	// Closed agents, from tombstones.
	if err := c.closed(r); err != nil {
		return err
	}

	// Verify before anything is deleted: every converted agent has a
	// consistent ancestry.
	for _, id := range order {
		if _, err := agentstate.Ancestry(id); err != nil {
			return fmt.Errorf("agent %s: %w", c.done[id].Name, err)
		}
	}

	// Lightweight parents: empty ones go, the rest are ordinary sessions.
	return c.fakeParents(r)
}

// preserve reports whether an existing new-format record is kept over the
// old one (only possible after the marker, when an old binary wrote more).
func (c *converter) preserve(existing agentstate.State) bool {
	m, ok, _ := agentstate.ReadMarker()
	return ok && m.Version >= agentstate.FormatVersion && existing.Session != ""
}

func metaOf(st agentstate.State) session.AgentMeta {
	m := session.AgentMeta{Version: session.AgentMetaVersion, RootSessionID: st.Root, Depth: st.Depth, Path: st.Path, Name: st.Name,
		Role: st.Preset, SpawnCwd: st.SpawnCwd, Project: st.Project, Origin: st.Origin, SpawnedBy: st.SpawnedBy}
	if st.Parent != "" {
		p := st.Parent
		m.ParentSessionID = &p
	}
	return m
}

// convert gives the legacy agent id its place in the new model.
func (c *converter) convert(id string, visiting map[string]bool) (agentstate.State, error) {
	if st, ok := c.done[id]; ok {
		return st, nil
	}
	a, ok := c.agents[id]
	if !ok {
		return agentstate.State{}, fmt.Errorf("no agent %s", id)
	}
	if visiting[id] {
		return agentstate.State{}, fmt.Errorf("the old agent data has a loop at agent %s", id)
	}
	visiting[id] = true
	st := a.State
	st.Lifecycle = agentstate.Open
	st.Version = agentstate.RecordVersion
	project := func(cwd string) string {
		if c.o.Project != nil && cwd != "" {
			return c.o.Project(cwd)
		}
		return cwd
	}
	parent := st.Parent
	switch {
	case c.agents[parent].State.Session != "": // a child of an agent
		p, err := c.convert(parent, visiting)
		if err != nil {
			return agentstate.State{}, err
		}
		st.Root, st.Depth, st.Path, st.Project = p.Root, p.Depth+1, p.Path+"/"+st.Name, p.Project
		st.Origin = session.OriginAgent
		st.SpawnCwd = a.State.Cwd
	case c.fakes[parent].ID != "": // a direct child of a lightweight parent: its own root
		fake := c.fakes[parent]
		st.Parent, st.Root, st.Depth, st.Path = "", st.Session, 0, agentstate.RootPath
		st.Origin, st.Project, st.SpawnCwd = session.OriginExternal, project(fake.Cwd), fake.Cwd
		// Its turns were jobs of that parent; they belong to the agent now.
		if a.State.Job > 0 {
			id, err := c.moveJob(parent, a.State.Job, st.Session)
			if err != nil {
				return agentstate.State{}, err
			}
			st.Job, st.JobOwner = id, st.Session
		}
	default: // a child of an ordinary session (or of one that no longer exists)
		cwd := ""
		if p, err := session.Find(parent); err == nil {
			if s, err := session.Summarize(p); err == nil {
				cwd = s.Cwd
			}
		}
		if cwd == "" {
			cwd = a.State.Cwd
		}
		st.Root, st.Depth, st.Path = parent, 1, agentstate.RootPath+"/"+st.Name
		st.Origin, st.Project, st.SpawnCwd = session.OriginAgent, project(cwd), cwd
	}
	if st.JobOwner == "" && st.Parent != "" {
		st.JobOwner = st.Parent
	}
	delete(visiting, id)
	c.done[id] = st
	return st, nil
}

// moveJob copies job id of session from to a new job of session to (its
// ID), so the agent's turn keeps its job and log; the old directory goes
// with the old layout.
func (c *converter) moveJob(from string, id int, to string) (int, error) {
	src := filepath.Join(jobs.Root(from), strconv.Itoa(id))
	if _, err := os.Stat(src); err != nil {
		return id, nil // nothing to move: the turn record says what happened
	}
	dst := filepath.Join(jobs.Root(to), "1")
	if _, err := os.Stat(dst); err == nil {
		return 1, nil // copied by an earlier run
	}
	if err := copyTree(src, dst); err != nil {
		return 0, fmt.Errorf("copying job %s/%d: %w", from, id, err)
	}
	var j map[string]any
	data, err := os.ReadFile(filepath.Join(dst, "job.json"))
	if err != nil || json.Unmarshal(data, &j) != nil {
		return 0, fmt.Errorf("job %s/%d: unreadable job.json", from, id)
	}
	j["session"], j["id"] = to, 1
	data, _ = json.MarshalIndent(j, "", "  ")
	if err := os.WriteFile(filepath.Join(dst, "job.json"), data, 0o600); err != nil {
		return 0, err
	}
	c.copiedJobs = append(c.copiedJobs, src)
	return 1, nil
}

func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// closed writes a closed record for every removed agent the old layout
// remembered, so its ID stays reserved and addressable as closed. A
// tombstone knows its old root and path; the parent is the agent that had
// the parent path in that tree (live or removed), and the new position is
// taken from it. The direct children of a lightweight external parent are
// roots now, like the live ones.
func (c *converter) closed(r *Report) error {
	tombs := slices.Clone(c.legacy.Tombs)
	depth := func(t agentstate.LegacyTomb) int { return strings.Count(t.Path, "/") }
	sort.SliceStable(tombs, func(i, j int) bool { return depth(tombs[i]) < depth(tombs[j]) })
	byPath := map[string]agentstate.State{} // old root + old path -> its record
	for id, st := range c.done {
		a := c.agents[id]
		// Live agents sit where the old layout put them: root and path of the old tree.
		byPath[c.oldRoot(a)+" "+c.oldPath(a)] = st
	}
	for _, t := range tombs {
		if live, ok := c.done[t.Session]; ok {
			byPath[t.Root+" "+t.Path] = live
			continue
		}
		name := path.Base(t.Path)
		if agentstate.ValidName(name) != nil {
			name = "closed"
		}
		st := agentstate.State{Name: name, Session: t.Session, Lifecycle: agentstate.Closed, Preset: "general", Origin: session.OriginAgent}
		parentPath := path.Dir(t.Path)
		switch parent, hasParent := byPath[t.Root+" "+parentPath]; {
		case c.fakes[t.Root].ID != "" && parentPath == agentstate.RootPath: // a direct child of a lightweight parent
			fake := c.fakes[t.Root]
			st.Root, st.Path, st.Origin = t.Session, agentstate.RootPath, session.OriginExternal
			if c.o.Project != nil && fake.Cwd != "" {
				st.Project = c.o.Project(fake.Cwd)
			}
		case hasParent && parent.Session != t.Session:
			st.Parent, st.Root, st.Depth, st.Path, st.Project = parent.Session, parent.Root, parent.Depth+1, parent.Path+"/"+name, parent.Project
		default: // below a real session, or the best the tombstone says
			st.Parent, st.Root, st.Depth, st.Path = t.Root, t.Root, max(depth(t), 1), t.Path
			if parentPath == agentstate.RootPath {
				st.Depth, st.Path = 1, agentstate.RootPath+"/"+name
			}
		}
		if existing, err := agentstate.Load(t.Session); err == nil && c.preserve(existing) {
			continue
		}
		if err := agentstate.Save(st); err != nil {
			return fmt.Errorf("closed agent %s: %w", t.Session, err)
		}
		byPath[t.Root+" "+t.Path] = st
		r.Closed++
	}
	return nil
}

// oldRoot is the root the old layout recorded for an agent: following
// parents until one is not an agent.
func (c *converter) oldRoot(a agentstate.LegacyAgent) string {
	cur := a.State.Parent
	for range 256 {
		next, ok := c.agents[cur]
		if !ok {
			return cur
		}
		cur = next.State.Parent
	}
	return cur
}

// oldPath is the path of an agent in its old tree, below the old root.
func (c *converter) oldPath(a agentstate.LegacyAgent) string {
	names := []string{a.State.Name}
	cur := a.State.Parent
	for range 256 {
		next, ok := c.agents[cur]
		if !ok {
			break
		}
		names = append([]string{next.State.Name}, names...)
		cur = next.State.Parent
	}
	return agentstate.RootPath + "/" + strings.Join(names, "/")
}

// fakeParents deals with the lightweight sessions older versions made for
// agents started from a shell: those without a message are deleted (with
// their name mappings), those with real messages stay as ordinary sessions.
func (c *converter) fakeParents(r *Report) error {
	ids := make([]string, 0, len(c.fakes))
	for id := range c.fakes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := c.fakes[id]
		if s.Messages == 0 {
			p := s.Path
			for _, f := range []string{p, session.LockPath(p), session.LogPath(p)} {
				if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("deleting the empty external parent %s: %w", id, err)
				}
			}
			r.FakeDeleted++
			continue
		}
		if strings.HasSuffix(s.Path, ".zst") {
			r.FakeKept++
			continue
		}
		if err := session.RewriteHeader(s.Path, func(h *session.Entry) { h.External = false }); err != nil {
			return fmt.Errorf("external parent %s has messages and stays as an ordinary session, but its header could not be rewritten: %w", id, err)
		}
		r.FakeKept++
	}
	// The project → parent mappings of the old shared parents.
	maps, _ := filepath.Glob(filepath.Join(config.Dir(), "external_parents", "*"))
	for _, m := range maps {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		if _, ok := c.fakes[strings.TrimSpace(string(b))]; ok {
			_ = os.Remove(m)
		}
	}
	_ = os.Remove(filepath.Join(config.Dir(), "external_parents")) // once empty
	return nil
}
