package agentstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/jobs"
)

// State is an agent as atto agent left it. Only atto agent (start,
// next) writes it; the turn process writes its Turn.
type State struct {
	Name    string `json:"name"`
	Parent  string `json:"parent"`  // the session it works for
	Session string `json:"session"` // its own session
	Preset  string `json:"preset"`
	// Instructions are the preset's, as they were when it started.
	Instructions string `json:"instructions,omitempty"`
	Model        string `json:"model"` // provider/id
	Effort       string `json:"effort,omitempty"`
	Cwd          string `json:"cwd"`
	// Worktree, with -worktree: the git worktree it works in (Cwd is in
	// it), on Branch, made from commit Base of the parent's repository
	// Repo.
	Worktree string    `json:"worktree,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Base     string    `json:"base,omitempty"`
	Repo     string    `json:"repo,omitempty"`
	Task     string    `json:"task"` // the first message
	Created  time.Time `json:"created"`
	Turns    int       `json:"turns"`         // turns started
	Prompt   string    `json:"prompt"`        // the latest turn's message
	Job      int       `json:"job,omitempty"` // the parent's job running the latest turn
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

// Dir holds the agents of session parent.
func Dir(parent string) string { return filepath.Join(config.AgentStateDir(), parent) }

func statePath(parent, name string) string { return filepath.Join(Dir(parent), name+".json") }
func turnPath(parent, name string) string  { return filepath.Join(Dir(parent), name+".turn.json") }

// ErrNotFound is wrapped by Load for a name no agent has.
var ErrNotFound = errors.New("no such agent")

// Load reads the agent name of session parent.
func Load(parent, name string) (State, error) {
	var s State
	if err := ValidName(name); err != nil {
		return s, err
	}
	data, err := os.ReadFile(statePath(parent, name))
	if errors.Is(err, os.ErrNotExist) {
		return s, fmt.Errorf("%w %q (see atto agent list)", ErrNotFound, name)
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// Save writes s.
func Save(s State) error {
	if err := ValidName(s.Name); err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(s.Parent), 0o755); err != nil {
		return err
	}
	if err := saveUp(s); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	return fsutil.WriteAtomic(statePath(s.Parent, s.Name), data, 0o644)
}

// Create saves s as a new agent; the name must be free.
func Create(s State) error {
	if err := ValidName(s.Name); err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(s.Parent), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(statePath(s.Parent, s.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("an agent named %q exists: give it a follow-up with atto agent next %s, or pick another name", s.Name, s.Name)
	}
	if err != nil {
		return err
	}
	f.Close()
	return Save(s)
}

// Remove deletes the agent's state (its session stays).
func Remove(parent, name string) {
	if s, err := Load(parent, name); err == nil && s.Session != "" {
		_ = os.Remove(upPath(s.Session))
	}
	_ = os.Remove(statePath(parent, name))
	_ = os.Remove(turnPath(parent, name))
}

// List returns the agents of session parent, oldest first.
func List(parent string) []State {
	paths, _ := filepath.Glob(filepath.Join(Dir(parent), "*.json"))
	var out []State
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		if strings.HasSuffix(name, ".turn") {
			continue
		}
		if s, err := Load(parent, name); err == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// SaveTurn records how turn t of agent name is going.
func SaveTurn(parent, name string, t Turn) error {
	if err := os.MkdirAll(Dir(parent), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(t, "", "  ")
	return fsutil.WriteAtomic(turnPath(parent, name), data, 0o644)
}

// LoadTurn reads what the latest turn process recorded.
func LoadTurn(parent, name string) (Turn, bool) {
	var t Turn
	data, err := os.ReadFile(turnPath(parent, name))
	if err != nil || json.Unmarshal(data, &t) != nil {
		return Turn{}, false
	}
	return t, true
}

// Latest returns the latest turn and its status: what the turn process
// recorded, corrected by its job (a stopped or crashed turn records
// nothing more).
func (s State) Latest() Turn {
	if s.Turns == 0 {
		return Turn{Status: Idle}
	}
	t, ok := LoadTurn(s.Parent, s.Name)
	if !ok || t.N != s.Turns { // the turn process hasn't written yet
		t = Turn{N: s.Turns, Status: Queued}
	}
	if s.Job == 0 {
		if t.Status.Active() {
			t.Status, t.Error = Failed, "the turn did not start"
		}
		return t
	}
	j, err := jobs.Get(s.Parent, s.Job)
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

// ListAll returns agents from every parent, including agents whose parent
// session no longer exists. Like List, it reads only the agent state files.
func ListAll() []State {
	dirs, _ := os.ReadDir(config.AgentStateDir())
	var out []State
	for _, d := range dirs {
		if d.IsDir() && d.Name() != "_up" {
			out = append(out, List(d.Name())...)
		}
	}
	return out
}
