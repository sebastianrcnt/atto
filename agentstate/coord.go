package agentstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/fsutil"
)

// Coordination between processes that change one tree. It lives under
// agent-state/.coord, outside the namespace of records:
//
//	.coord/trees/<root>/.tree.lock   serializes spawns, closes and shutdown
//	.coord/trees/<root>/<id>.closed  new work below that session is refused
//	.coord/spawn/<id>.json           a spawn in progress (see BeginSpawn)
//
// Per-agent files (<id>.turn.lock) sit beside the records. There are no
// slots: every turn starts at once, and one agent runs one turn at a time.

func coordDir() string { return filepath.Join(dir(), ".coord") }

func treeDir(root string) string { return filepath.Join(coordDir(), "trees", root) }

func lockFile(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for !tryLock(f) {
		time.Sleep(10 * time.Millisecond)
	}
	return func() { unlock(f); f.Close() }, nil
}

// LockTurn serializes accepting tasks with retiring an agent's consumer: an
// agent runs one turn at a time. The file stays: unlinking a flock file
// would let waiters lock old inodes.
func LockTurn(id string) (func(), error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	return lockFile(turnLockPath(id))
}

// LockTree serializes new agent work with shutdown, for the tree session is in.
func LockTree(session string) (func(), error) {
	if err := checkID(session); err != nil {
		return nil, err
	}
	return lockFile(filepath.Join(treeDir(Root(session)), ".tree.lock"))
}

func closedPath(session string) string {
	return filepath.Join(treeDir(Root(session)), session+".closed")
}

// StartWork holds the tree until a new agent or turn is durably started. It
// refuses when session or one of its ancestors is closed or closing, and
// fails closed when the ancestry is inconsistent.
func StartWork(session string) (func(), error) {
	release, err := LockTree(session)
	if err != nil {
		return nil, err
	}
	chain, err := Ancestry(session)
	if err != nil {
		release()
		return nil, fmt.Errorf("refusing to start agent work: %w", err)
	}
	for _, s := range chain {
		if _, err := os.Stat(closedPath(s)); !os.IsNotExist(err) {
			release()
			return nil, fmt.Errorf("the parent session is closed: reopen it before starting agent work")
		}
		if sum, ok := summaryOf(s); ok && sum.Lifecycle != Open && sum.Lifecycle != "" {
			release()
			return nil, fmt.Errorf("agent %s is %s: no new work below it", sum.Path, sum.Lifecycle)
		}
	}
	return release, nil
}

// CloseTree prevents new work below session until it is opened again. Hold the
// returned lock while walking and stopping the descendants.
func CloseTree(session string) (func(), error) {
	release, err := LockTree(session)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(closedPath(session), nil, 0o644); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// OpenTree reopens this session, without reopening any closed ancestor.
func OpenTree(session string) {
	if checkID(session) != nil {
		return
	}
	// Opening a session that was never closed takes no lock: a worker that a
	// spawn just started binds its session while the spawn holds the tree.
	if _, err := os.Stat(closedPath(session)); os.IsNotExist(err) {
		return
	}
	release, err := LockTree(session)
	if err != nil {
		return
	}
	defer release()
	_ = os.Remove(closedPath(session))
}

// RequestInterrupt asks one turn to stop without terminating its process
// tree. The turn number prevents an old request from interrupting its successor.
func RequestInterrupt(id string, turn int) error {
	if err := checkID(id); err != nil {
		return err
	}
	if turn <= 0 {
		return fmt.Errorf("invalid turn %d", turn)
	}
	if err := os.MkdirAll(dir(), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(interruptPath(id), []byte(strconv.Itoa(turn)), 0o644)
}

// Interrupted reports whether this turn has a user interrupt request.
func Interrupted(id string, turn int) bool {
	if checkID(id) != nil || turn <= 0 {
		return false
	}
	data, err := fsutil.ReadFile(interruptPath(id))
	return err == nil && strings.TrimSpace(string(data)) == strconv.Itoa(turn)
}

// InterruptRequestPath is the file RequestInterrupt writes, and its content
// for turn: a job that stops a turn on request names them (jobs.Control).
func InterruptRequestPath(id string, turn int) (path, content string) {
	return interruptPath(id), strconv.Itoa(turn)
}

// SpawnEntry is what a spawn in progress has made so far, recorded before
// any of it exists so that a crash can be rolled back.
type SpawnEntry struct {
	ID       string    `json:"id"`
	Parent   string    `json:"parent,omitempty"`
	Name     string    `json:"name"`
	Worktree string    `json:"worktree,omitempty"` // made by this spawn (never an existing one)
	Branch   string    `json:"branch,omitempty"`
	Repo     string    `json:"repo,omitempty"`
	Started  time.Time `json:"started"`
}

// Spawn is a spawn's journal entry, held locked while the spawn runs.
type Spawn struct {
	path string
	f    *os.File
}

func spawnDir() string { return filepath.Join(coordDir(), "spawn") }

// BeginSpawn journals e. The entry stays locked by this process until Done,
// so another process that finds it unlocked knows its spawner is gone.
func BeginSpawn(e SpawnEntry) (*Spawn, error) {
	if err := checkID(e.ID); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(spawnDir(), 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(spawnDir(), e.ID+".json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	if !tryLock(f) {
		f.Close()
		return nil, fmt.Errorf("a spawn of %s is already running", e.ID)
	}
	e.Started = time.Now()
	data, _ := json.Marshal(e)
	if _, err := f.Write(data); err != nil {
		unlock(f)
		f.Close()
		return nil, err
	}
	_ = f.Sync()
	return &Spawn{path: path, f: f}, nil
}

// Update rewrites the entry (for example once the worktree is planned).
func (s *Spawn) Update(e SpawnEntry) error {
	e.Started = time.Now()
	data, _ := json.Marshal(e)
	if err := s.f.Truncate(0); err != nil {
		return err
	}
	if _, err := s.f.WriteAt(data, 0); err != nil {
		return err
	}
	return s.f.Sync()
}

// Done ends the journal entry: the spawn was published or rolled back.
func (s *Spawn) Done() {
	_ = os.Remove(s.path)
	unlock(s.f)
	s.f.Close()
}

// StaleSpawns are the journal entries whose spawner is gone. The caller
// finishes them (rolling back what they made, unless a record was published)
// and calls ClearSpawn.
func StaleSpawns() []SpawnEntry {
	ents, _ := os.ReadDir(spawnDir())
	var out []SpawnEntry
	for _, d := range ents {
		path := filepath.Join(spawnDir(), d.Name())
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		if tryLock(f) {
			var e SpawnEntry
			if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &e) == nil && e.ID != "" {
				out = append(out, e)
			} else {
				_ = os.Remove(path) // empty: it died before writing anything
			}
			unlock(f)
		}
		f.Close()
	}
	return out
}

// ClearSpawn forgets a stale journal entry once it has been dealt with.
func ClearSpawn(id string) {
	if checkID(id) == nil {
		_ = os.Remove(filepath.Join(spawnDir(), id+".json"))
	}
}

// Published reports whether id has a record: a spawn that got that far
// stays, whatever else a crash left unfinished.
func Published(id string) bool {
	_, err := Load(id)
	return err == nil
}
