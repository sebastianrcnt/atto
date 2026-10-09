package agentstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/session"
)

// An agent is a session; its record is a file named by the session ID:
//
//	agent-state/<id>.json                  the record (State)
//	agent-state/<id>.turn.json             how its latest turn went
//	agent-state/<id>.turn.json.interrupt   a request to stop that turn
//	agent-state/<id>.turn.lock             one turn at a time
//	agent-state/.coord/                    locks that coordinate several agents
//	agent-state/.format                    the format marker (format.go)
//
// There are no per-parent directories: a parent, a root and a path are
// attributes of the record (and of the session header), never where it lives.

func dir() string { return config.AgentStateDir() }

func recordPath(id string) string    { return filepath.Join(dir(), id+".json") }
func turnPath(id string) string      { return filepath.Join(dir(), id+".turn.json") }
func turnLockPath(id string) string  { return filepath.Join(dir(), id+".turn.lock") }
func interruptPath(id string) string { return turnPath(id) + ".interrupt" }

func checkID(id string) error {
	if !fsutil.ValidID(id) {
		return fmt.Errorf("invalid agent session ID %q", id)
	}
	return nil
}

// ErrNotFound is wrapped by lookups for an agent that does not exist.
var ErrNotFound = errors.New("no such agent")

// Load reads the agent whose session is id (exactly).
func Load(id string) (State, error) {
	var s State
	if err := checkID(id); err != nil {
		return s, err
	}
	data, err := fsutil.ReadFile(recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return s, fmt.Errorf("%w with session %q (see atto agent list)", ErrNotFound, id)
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("agent record %s: %w", id, err)
	}
	if s.Session == "" {
		s.Session = id
	}
	if s.Session != id {
		return s, fmt.Errorf("agent record %s names session %q", id, s.Session)
	}
	return s, nil
}

// LoadChild reads the live child called name of parent.
func LoadChild(parent, name string) (State, error) {
	if err := ValidName(name); err != nil {
		return State{}, err
	}
	for _, sum := range inventory() {
		if sum.Parent == parent && sum.Name == name && sum.Lifecycle != Closed {
			return Load(sum.ID)
		}
	}
	return State{}, fmt.Errorf("%w %q (see atto agent list)", ErrNotFound, name)
}

// normalize fills what a record derives from its tree position, so a
// caller can give no more than a name, a parent and a session.
func normalize(s *State) error {
	if err := ValidName(s.Name); err != nil {
		return err
	}
	if err := checkID(s.Session); err != nil {
		return err
	}
	if s.Parent != "" {
		if err := checkID(s.Parent); err != nil {
			return err
		}
	}
	s.Version = RecordVersion
	if s.Lifecycle == "" {
		s.Lifecycle = Open
	}
	if s.Root == "" || s.Path == "" {
		switch {
		case s.Parent == "":
			s.Root, s.Depth, s.Path = s.Session, 0, "/root"
		default:
			if p, err := Load(s.Parent); err == nil {
				s.Root, s.Depth, s.Path = p.Root, p.Depth+1, p.Path+"/"+s.Name
			} else {
				s.Root, s.Depth, s.Path = s.Parent, 1, "/root/"+s.Name
			}
		}
	}
	if s.Origin == "" {
		s.Origin = session.OriginAgent
		if s.Parent == "" {
			s.Origin = session.OriginExternal
		}
	}
	if s.Project == "" {
		s.Project = s.SpawnCwd
	}
	s.Summary = Summary{ID: s.Session, Parent: s.Parent, Root: s.Root, Name: s.Name, Path: s.Path, Depth: s.Depth,
		Origin: s.Origin, Project: s.Project, Lifecycle: s.Lifecycle, Created: s.Created}
	return nil
}

// Save writes s, replacing its record.
func Save(s State) error {
	if err := normalize(&s); err != nil {
		return err
	}
	if err := os.MkdirAll(dir(), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	err := fsutil.WriteAtomic(recordPath(s.Session), data, 0o644)
	forget(s.Session)
	return err
}

// Create publishes s as a new agent: only a published record can be
// addressed. Among the live children of one parent a name is used once,
// closed agents' names are free again; roots may share names. The caller
// holds the tree lock (StartWork), so two spawns cannot take one name.
func Create(s State) error {
	if err := normalize(&s); err != nil {
		return err
	}
	if s.Parent != "" {
		for _, sum := range inventory() {
			if sum.Parent == s.Parent && sum.Name == s.Name && sum.Lifecycle != Closed && sum.ID != s.Session {
				return fmt.Errorf("an agent named %q exists: give it a follow-up with atto agent task %s, or pick another name", s.Name, s.Name)
			}
		}
	}
	if err := os.MkdirAll(dir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(recordPath(s.Session), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("agent %s exists", s.Session)
	}
	if err != nil {
		return err
	}
	f.Close()
	if err := Save(s); err != nil {
		_ = os.Remove(recordPath(s.Session))
		return err
	}
	return nil
}

// Discard deletes an agent that never ran, after a failed spawn. Unlike
// closing, it leaves no trace: nothing was ever addressed by its ID.
func Discard(id string) {
	if checkID(id) != nil {
		return
	}
	for _, p := range []string{recordPath(id), turnPath(id), interruptPath(id)} {
		_ = os.Remove(p)
	}
	forget(id)
}

// MarkClosing records that the agent is being torn down; a close that
// stopped halfway is resumed by closing it again.
func MarkClosing(id string) error {
	s, err := Load(id)
	if err != nil {
		return err
	}
	if s.Lifecycle == Closed {
		return nil
	}
	s.Lifecycle = Closing
	return Save(s)
}

// MarkClosed commits the close, last: the record stays, with its last turn
// and where its transcript went. The pending interrupt request goes.
func MarkClosed(id, archive string) error {
	s, err := Load(id)
	if err != nil {
		return err
	}
	s.Lifecycle, s.Closed, s.Archive = Closed, time.Now(), archive
	if err := Save(s); err != nil {
		return err
	}
	_ = os.Remove(interruptPath(id))
	return nil
}

// SaveTurn records how turn t of agent id is going.
func SaveTurn(id string, t Turn) error {
	if err := checkID(id); err != nil {
		return err
	}
	if err := os.MkdirAll(dir(), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(t, "", "  ")
	return fsutil.WriteAtomic(turnPath(id), data, 0o644)
}

// LoadTurn reads what the latest turn process recorded.
func LoadTurn(id string) (Turn, bool) {
	var t Turn
	if checkID(id) != nil {
		return Turn{}, false
	}
	data, err := fsutil.ReadFile(turnPath(id))
	if err != nil || json.Unmarshal(data, &t) != nil {
		return Turn{}, false
	}
	return t, true
}

// --- the inventory ---

// The inventory reads the directory once and decodes only each record's
// Summary, caching it by the file's modification time and size; callers load
// the full record only for the agents they want.

type cached struct {
	mod  time.Time
	size int64
	sum  Summary
	ok   bool
}

var (
	cacheMu sync.Mutex
	cache   = map[string]cached{}
)

func forget(id string) {
	cacheMu.Lock()
	delete(cache, recordPath(id))
	cacheMu.Unlock()
}

// readSummary decodes the first field of a record, "summary", and stops;
// a record that does not start with it is decoded whole.
func readSummary(path string) (Summary, bool) {
	f, err := fsutil.Open(path)
	if err != nil {
		return Summary{}, false
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	if tok, err := dec.Token(); err == nil && tok == json.Delim('{') {
		if key, err := dec.Token(); err == nil && key == "summary" {
			var sum Summary
			if dec.Decode(&sum) == nil && sum.ID != "" {
				return sum, true
			}
		}
	}
	data, err := fsutil.ReadFile(path)
	if err != nil {
		return Summary{}, false
	}
	var s State
	if json.Unmarshal(data, &s) != nil || s.Session == "" {
		return Summary{}, false
	}
	if normalize(&s) != nil {
		return Summary{}, false
	}
	return s.Summary, true
}

// inventory lists the summaries of every record, closed ones included,
// oldest first. Unreadable files are skipped.
func inventory() []Summary {
	ents, err := os.ReadDir(dir())
	if err != nil {
		return nil
	}
	var out []Summary
	live := map[string]bool{}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".turn.json") || strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir(), name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		live[path] = true
		c, hit := cache[path]
		if !hit || !c.mod.Equal(info.ModTime()) || c.size != info.Size() {
			c = cached{mod: info.ModTime(), size: info.Size()}
			c.sum, c.ok = readSummary(path)
			cache[path] = c
		}
		if c.ok && c.sum.ID == strings.TrimSuffix(name, ".json") {
			out = append(out, c.sum)
		}
	}
	for path := range cache { // removed files
		if !live[path] && filepath.Dir(path) == dir() {
			delete(cache, path)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.Before(out[j].Created)
	})
	return out
}

func loadAll(sums []Summary, keep func(Summary) bool) []State {
	var out []State
	for _, sum := range sums {
		if !keep(sum) {
			continue
		}
		if s, err := Load(sum.ID); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// Children are the live agents parent started, oldest first.
func Children(parent string) []State {
	return loadAll(inventory(), func(s Summary) bool { return s.Parent == parent && parent != "" && s.Lifecycle != Closed })
}

// All are the live agents of every tree, oldest first.
func All() []State {
	return loadAll(inventory(), func(s Summary) bool { return s.Lifecycle != Closed })
}

// ListAll is All.
func ListAll() []State { return All() }

// AllWithClosed are the agents of every tree, closed ones too.
func AllWithClosed() []State {
	return loadAll(inventory(), func(Summary) bool { return true })
}

// Tree is the live agents below root (not root itself), breadth first,
// each parent's children oldest first. A visited set keeps a corrupt cycle
// finite.
func Tree(root string) []State {
	sums := inventory()
	byParent := map[string][]Summary{}
	for _, s := range sums {
		if s.Lifecycle != Closed && s.Parent != "" {
			byParent[s.Parent] = append(byParent[s.Parent], s)
		}
	}
	var out []State
	seen := map[string]bool{root: true}
	queue := []string{root}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, s := range byParent[p] {
			if seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			if st, err := Load(s.ID); err == nil {
				out = append(out, st)
				queue = append(queue, s.ID)
			}
		}
	}
	return out
}

// ExternalRoots are the agents started from a shell that have no parent, in
// project (every project when project is empty), oldest first. Closed ones
// are included on request.
func ExternalRoots(project string, includeClosed bool) []State {
	return loadAll(inventory(), func(s Summary) bool {
		return s.Parent == "" && s.Origin == session.OriginExternal && (project == "" || s.Project == project) && (includeClosed || s.Lifecycle != Closed)
	})
}

// summaryOf is the inventory row of id.
func summaryOf(id string) (Summary, bool) {
	if checkID(id) != nil {
		return Summary{}, false
	}
	if sum, ok := readSummary(recordPath(id)); ok {
		return sum, true
	}
	return Summary{}, false
}
