package agentstate

import (
	"fmt"
	"regexp"
	"time"

	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
)

// Lifecycle is where an agent is in its life. A closed agent keeps its
// record (its ID stays reserved, with its last turn and the place of its
// archived transcript) but is no longer live.
type Lifecycle string

const (
	Open    Lifecycle = "open"
	Closing Lifecycle = "closing" // teardown started; resumed by the next close
	Closed  Lifecycle = "closed"
)

// RecordVersion is the version of the agent record.
const RecordVersion = 1

// Summary is the small first field of a record: what a directory inventory
// needs, readable without decoding the task and instructions after it. It
// is derived from the rest of the record whenever the record is saved.
type Summary struct {
	ID        string    `json:"id"`
	Parent    string    `json:"parent,omitempty"` // "" for a root
	Root      string    `json:"root"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Depth     int       `json:"depth"`
	Origin    string    `json:"origin,omitempty"`
	Project   string    `json:"project,omitempty"`
	Lifecycle Lifecycle `json:"lifecycle"`
	Created   time.Time `json:"created,omitzero"`
}

// SpawnedBy is who started an agent (see session.SpawnedBy).
type SpawnedBy = session.SpawnedBy

// State is an agent as atto agent left it, one file per agent named by the
// agent's session ID. Only atto agent writes it (spawn, task, close); the
// process running a turn writes the Turn.
type State struct {
	// Summary stays the first field: inventories decode only it.
	Summary Summary `json:"summary"`
	Version int     `json:"version"`

	Name    string `json:"name"`    // among its parent's children; a project label for a root
	Parent  string `json:"parent"`  // the session it works for; "" for a root started from a shell
	Session string `json:"session"` // its own session: the agent's identity
	Preset  string `json:"preset"`
	// Instructions are the preset's, as they were when it started.
	Instructions string `json:"instructions,omitempty"`
	Model        string `json:"model"` // provider/id
	Effort       string `json:"effort,omitempty"`
	Cwd          string `json:"cwd"`
	// Worktree, with -worktree: the git worktree it works in (Cwd is in
	// it), on Branch, made from commit Base of the spawning checkout's
	// repository Repo. Existing worktrees keep the paths and names they
	// were made with; new ones are worktrees/<id> on atto/<id>.
	Worktree string    `json:"worktree,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Base     string    `json:"base,omitempty"`
	Repo     string    `json:"repo,omitempty"`
	Task     string    `json:"task"` // the first message
	Created  time.Time `json:"created"`
	Turns    int       `json:"turns"`  // turns started
	Prompt   string    `json:"prompt"` // the latest turn's message
	// Job is the job running the latest turn, and JobOwner the session whose
	// job it is: the parent for a child, the agent itself for a root. Empty
	// means the parent (records written before roots had jobs).
	Job      int    `json:"job,omitempty"`
	JobOwner string `json:"jobOwner,omitempty"`

	Root      string     `json:"root"`
	Path      string     `json:"path"`
	Depth     int        `json:"depth"`
	Origin    string     `json:"origin,omitempty"` // session.OriginExternal or OriginAgent
	Project   string     `json:"project,omitempty"`
	SpawnCwd  string     `json:"spawnCwd,omitempty"`
	SpawnedBy *SpawnedBy `json:"spawnedBy,omitempty"`
	Lifecycle Lifecycle  `json:"lifecycle"`
	Closed    time.Time  `json:"closed,omitzero"`
	// Archive is where the transcript went when the agent closed.
	Archive string `json:"archive,omitempty"`
}

// IsRoot reports whether the agent has no parent: it was started from a
// shell and is the root of a tree of its own.
func (s State) IsRoot() bool { return s.Parent == "" }

// Live reports whether the agent has not been closed (or started closing).
func (s State) Live() bool { return s.Lifecycle == Open || s.Lifecycle == "" }

// JobRef is the session and job that run the latest turn.
func (s State) JobRef() (owner string, id int) {
	owner = s.JobOwner
	if owner == "" {
		owner = s.Parent
	}
	return owner, s.Job
}

// Status is where an agent's latest turn is.
type Status string

const (
	Idle    Status = "idle" // no turn yet
	Queued  Status = "queued"
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
	Stopped Status = "stopped" // atto agent stop
)

// Active reports whether a turn is queued or running.
func (s Status) Active() bool { return s == Queued || s == Running }

// Turn is how an agent's turn went, written by the process running it.
type Turn struct {
	N       int       `json:"turn"`
	Status  Status    `json:"status"`
	Queued  time.Time `json:"queued"`
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
	// Tokens the turn's model calls used, summed, and their cost in US
	// dollars (0 when the model has no prices).
	PromptTokens int     `json:"promptTokens,omitempty"`
	CachedTokens int     `json:"cachedTokens,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	Cost         float64 `json:"cost,omitempty"`
	Steps        int     `json:"steps,omitempty"`
	Error        string  `json:"error,omitempty"`
}

// Duration is how long the turn ran (so far), not counting the queue.
func (t Turn) Duration() time.Duration {
	if t.Started.IsZero() {
		return 0
	}
	end := t.Ended
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(t.Started)
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidName checks an agent or preset name: lowercase letters, digits
// and dashes.
func ValidName(name string) error {
	if !nameRE.MatchString(name) || len(name) > 40 {
		return fmt.Errorf("name %q: use 1-40 lowercase letters, digits and dashes (e.g. test-runner)", name)
	}
	return nil
}

// Latest returns the latest turn and its status: what the turn process
// recorded, corrected by its job (a stopped or crashed turn records
// nothing more).
func (s State) Latest() Turn {
	t, ok := LoadTurn(s.Session)
	return s.latest(t, ok)
}

// latest is Latest given what the turn process recorded (ok: it did).
func (s State) latest(t Turn, ok bool) Turn {
	if s.Turns == 0 {
		return Turn{Status: Idle}
	}
	if !ok || t.N != s.Turns { // the turn process hasn't written yet
		t = Turn{N: s.Turns, Status: Queued}
	}
	if s.Job == 0 {
		if t.Status.Active() {
			t.Status, t.Error = Failed, "the turn did not start"
		}
		return t
	}
	owner, id := s.JobRef()
	j, err := jobs.Get(owner, id)
	switch {
	case err != nil:
		t.Status, t.Error = Failed, err.Error()
	case j.Active():
		if t.Status.Active() && t.Status != Running {
			t.Status = Queued
		}
	case t.Status.Active(): // the process ended without saying how
		if j.Ended != nil && t.Ended.IsZero() {
			t.Ended = *j.Ended
		}
		switch {
		case j.Status == jobs.Killed:
			t.Status = Stopped
		case j.Error != "":
			t.Status, t.Error = Failed, j.Error
		case j.ExitCode != nil:
			t.Status, t.Error = Failed, fmt.Sprintf("the turn exited with code %d (log: atto job output %d)", *j.ExitCode, j.ID)
		default:
			t.Status, t.Error = Failed, fmt.Sprintf("the turn's process is %s", j.Status)
		}
	}
	return t
}
