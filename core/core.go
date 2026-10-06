// Package core is what every front end (the TUI, atto -p and the server)
// does to run a conversation: load the configuration, pick the model and
// effort, build the agent and hooks for a directory, bind them to a session
// file, restore what a saved session last used, and clean up when leaving
// it. GoalDriver keeps a goal going across turns, and core/transcript
// turns the agent's events and saved sessions into the items they show.
// Front ends own their display and input; the steps live here once.
package core

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/hooks"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
)

// DefaultEffort applies when neither a flag, the session nor settings.json
// says otherwise.
const DefaultEffort = "medium"

// Load creates ~/.atto if needed and reads settings.json and models.json.
func Load() (config.Settings, config.ModelsFile, error) {
	if err := config.Ensure(); err != nil {
		return config.Settings{}, config.ModelsFile{}, err
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return settings, config.ModelsFile{}, fmt.Errorf("%s: %w", config.SettingsPath(), err)
	}
	agent.SetToolOutputTokenLimit(settings.ToolOutputTokenLimit)
	models, err := config.LoadModels()
	if err != nil {
		return settings, models, fmt.Errorf("%s: %w", config.ModelsPath(), err)
	}
	return settings, models, nil
}

// ErrNoModels means no provider has credentials or models configured. The
// TUI starts anyway and shows NoModelsHint; atto -p fails with it.
var ErrNoModels error = noModelsError{}

type noModelsError struct{}

func (noModelsError) Error() string { return NoModelsHint() }

// NoModelsHint tells a first-time user how to get a model (pi:
// formatNoModelsAvailableMessage).
func NoModelsHint() string {
	return "No models available. Use /login (or run: atto login) to sign in or save an API key, or add a provider to " + config.ModelsPath() + "."
}

// PickModel resolves id (provider/id or a bare id). Without one it takes
// the default from settings.json, else the first configured model.
func PickModel(models config.ModelsFile, settings config.Settings, id string) (config.ModelRef, error) {
	r, _, err := PickModelFrom(models, settings, id, "")
	return r, err
}

// PickModelFrom is PickModel for a flag and a resumed session's last model
// (saved, used if still configured), and says which one it took.
func PickModelFrom(models config.ModelsFile, settings config.Settings, flag, saved string) (config.ModelRef, Origin, error) {
	if flag != "" {
		if r, ok := models.Find("", flag); ok {
			return r, FromFlag, nil
		}
		return config.ModelRef{}, FromFlag, fmt.Errorf("unknown model %q (see: atto models)", flag)
	}
	if saved != "" {
		if r, ok := models.Find("", saved); ok {
			return r, FromSession, nil
		}
	}
	if r, ok := models.Find(settings.DefaultProvider, settings.DefaultModel); ok {
		return r, FromSettings, nil
	}
	all := models.List()
	if len(all) == 0 {
		return config.ModelRef{}, FromDefault, ErrNoModels
	}
	return all[0], FromDefault, nil
}

// Effort returns the first effort set: the given one, settings.json's
// default, else DefaultEffort.
func Effort(settings config.Settings, given string) string {
	e, _ := EffortFrom(settings, given, "")
	return e
}

// EffortFrom is Effort for a flag and a resumed session's last effort, and
// says which one it took.
func EffortFrom(settings config.Settings, flag, saved string) (string, Origin) {
	switch {
	case flag != "":
		return flag, FromFlag
	case saved != "":
		return saved, FromSession
	case settings.DefaultEffort != "":
		return settings.DefaultEffort, FromSettings
	}
	return DefaultEffort, FromDefault
}

// CheckEffort reports an effort the model doesn't offer.
func CheckEffort(m config.ModelRef, effort string) error {
	if lv := m.Model.Levels(); len(lv) > 0 && !slices.Contains(lv, effort) {
		return fmt.Errorf("%s has no effort %q (levels: %s)", m.Model.ID, effort, strings.Join(lv, ", "))
	}
	return nil
}

// NewAgent builds an agent for cwd with the hooks configured for it. The
// hooks runner is nil when there are none.
func NewAgent(cwd string, model config.ModelRef, effort string) (*agent.Agent, *hooks.Runner, error) {
	ag, hk, _, err := NewAgentSources(cwd, model, effort)
	return ag, hk, err
}

// NewAgentSources is NewAgent that also returns the settings files the
// hooks came from, for Collect.
func NewAgentSources(cwd string, model config.ModelRef, effort string) (*agent.Agent, *hooks.Runner, []config.HookSource, error) {
	hk, src, err := LoadHooks(cwd)
	if err != nil {
		return nil, nil, nil, err
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return nil, nil, nil, err
	}
	ag := agent.New(model, effort, cwd)
	ag.SetCompaction(settings.Compaction)
	SetHooks(ag, hk)
	return ag, hk, src, nil
}

// LoadHooks reads the hooks configured for cwd: the runner (nil when there
// are none) and the settings files they came from.
func LoadHooks(cwd string) (*hooks.Runner, []config.HookSource, error) {
	src, err := config.LoadHookSources(cwd)
	if err != nil {
		return nil, nil, err
	}
	return hooks.New(config.MergeHooks(src), cwd), src, nil
}

// SetHooks makes ag run hk's hooks; nil removes them. Call it while no
// request is in flight.
func SetHooks(ag *agent.Agent, hk *hooks.Runner) {
	if hk == nil {
		ag.Hooks = nil // not a typed nil in the interface
		return
	}
	ag.Hooks = hk
}

// Bind points ag and hk at a session file. start fixes the date in the
// system prompt (a resumed session keeps its own, which keeps the prefix
// cache); record makes the agent append its messages to the file.
func Bind(ag *agent.Agent, hk *hooks.Runner, file *session.Writer, start time.Time, record bool) {
	ag.SetStart(start)
	ag.SetSession(file.ID, Env(file.ID))
	ag.Record, ag.EntryID = nil, nil
	if record {
		ag.Record, ag.EntryID = file.Append, file.Leaf
	}
	hk.SetSession(file.ID, file.Path)
	if m := ExtensionsOf(ag); m != nil {
		m.SetSession(file.ID)
	}
	if m := MCPOf(ag); m != nil {
		m.SetSession(file.ID)
	}
}

// Env is the environment of the commands an agent runs: the session ID
// (for atto history, job, goal...), the ATTO_AGENT guard, and the atto
// binary's directory first on PATH.
func Env(id string) []string {
	env := []string{"ATTO_SESSION_ID=" + id, config.EnvAgent + "=1"}
	if exe, err := os.Executable(); err == nil {
		env = append(env, "PATH="+filepath.Dir(exe)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return env
}

// Saved is what a session file says to restore beyond its messages: the
// last model, effort, context mode and name. They are session-wide; the latest value
// wins whichever branch it was recorded on.
type Saved struct {
	Header      session.Entry
	Entries     []session.Entry
	Model       string // provider/id
	Effort      string
	Name        string
	LongContext bool
}

// Open loads a saved session and reopens its file for appending, on the
// branch it was left on.
func Open(path string) (Saved, *session.Writer, error) {
	h, entries, err := session.Load(path)
	if err != nil {
		return Saved{}, nil, err
	}
	s := Saved{Header: h, Entries: entries}
	for _, e := range entries {
		switch e.Type {
		case session.TypeModel:
			s.Model = e.Provider + "/" + e.Model
		case session.TypeEffort:
			s.Effort = e.Effort
		case session.TypeContext:
			s.LongContext = e.LongContext
		case session.TypeName:
			s.Name = e.Name
		}
	}
	file := session.Resume(path, h)
	file.SetLeaf(session.Leaf(entries))
	return s, file, nil
}

// Branch is the active branch, which is what the agent restores.
func (s Saved) Branch() []session.Entry { return session.Active(s.Entries) }

// Leave cleans up after a session: its background jobs end with it (as in
// codex) and its goal file goes (the session file keeps the goal's last
// snapshot). Returns how many jobs were stopped.
func Leave(id string) int {
	_ = goal.Clear(id)
	return jobs.KillAll(id)
}

// Poll fires the session's due timers and takes the events waiting in its
// inbox (finished jobs, monitors, timers).
func Poll(id string) []events.Event {
	events.FireDue(id, time.Now())
	return events.Drain(id)
}
