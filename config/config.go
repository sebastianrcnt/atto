// Package config locates atto's user directory (~/.atto) and its files.
//
// Layout mirrors pi's ~/.pi/agent, flattened since atto has a single binary:
//
//	~/.atto/
//	  settings.json   user settings
//	  models.json     providers and models
//	  auth.json       API keys / credentials
//	  sessions/       session transcripts
//	  extensions/     hooks and extensions
//	  prompts/        prompt templates
//	  skills/         skills
//	  themes/         custom themes
//	  bin/            helper binaries
//
// ATTO_DIR overrides the root.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/fsutil"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
)

const EnvDir = "ATTO_DIR"

// EnvAgent is set in the environment of every command an atto agent runs.
// atto refuses to start another agent (or change credentials) when it is
// set, so a model cannot recurse into atto by accident. It is a guard, not
// a sandbox: the model can unset it.
const EnvAgent = "ATTO_AGENT"

// InAgent reports whether this process was started by an atto agent.
func InAgent() bool { return os.Getenv(EnvAgent) != "" }

// EnvLegacyAgent is the ATTO_SUBAGENT compatibility spelling of EnvAgent
// for commands launched by older installs.
const EnvLegacyAgent = "ATTO_SUBAGENT"

// InAgentCommand reports whether this process was started by an agent.
func InAgentCommand() bool { return InAgent() || os.Getenv(EnvLegacyAgent) != "" }

// EnvToolCallID is set, like Codex's CODEX_TOOL_CALL_ID, in the environment
// of every command the model runs through its shell tool: the ID of that
// tool call. Hosted commands and the background jobs they start get it too;
// commands the user runs with "!" get none. It is tracking, not proof: the
// model can change its own environment.
const EnvToolCallID = "ATTO_TOOL_CALL_ID"

// EnvView names the directory "atto view" leaves images in for the
// command that ran it: the agent sets it for each foreground shell call
// and attaches what it finds there to that call's result.
const EnvView = "ATTO_VIEW_DIR"

// Dir returns the atto root directory.
func Dir() string {
	if d := os.Getenv(EnvDir); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".atto"
	}
	return filepath.Join(home, ".atto")
}

func SettingsPath() string  { return filepath.Join(Dir(), "settings.json") }
func ModelsPath() string    { return filepath.Join(Dir(), "models.json") }
func AuthPath() string      { return filepath.Join(Dir(), "auth.json") }
func SessionsDir() string   { return filepath.Join(Dir(), "sessions") }
func ArchivedDir() string   { return filepath.Join(Dir(), "archived_sessions") }
func ImagesDir() string     { return filepath.Join(Dir(), "images") }
func ExtensionsDir() string { return filepath.Join(Dir(), "extensions") }
func PromptsDir() string    { return filepath.Join(Dir(), "prompts") }
func SkillsDir() string     { return filepath.Join(Dir(), "skills") }

// AgentsDir holds the user's agent profiles (<name>.md); a project's
// are in .atto/agents.
func AgentsDir() string { return filepath.Join(Dir(), "agents") }

// AgentStateDir holds the state of the agents each session started.
func AgentStateDir() string { return filepath.Join(Dir(), "agent-state") }

// SkillsCacheDir is where the built-in skills are written, so the model
// can read them as files (see package skills).
func SkillsCacheDir() string { return filepath.Join(Dir(), "cache", "skills") }
func ThemesDir() string      { return filepath.Join(Dir(), "themes") }
func BinDir() string         { return filepath.Join(Dir(), "bin") }

// Ensure creates the root and its subdirectories if missing.
func Ensure() error {
	for _, d := range []string{Dir(), SessionsDir(), ExtensionsDir(), PromptsDir(), SkillsDir(), ThemesDir(), BinDir()} {
		if err := fsutil.PrivateDirs(Dir(), d); err != nil {
			return err
		}
	}
	return nil
}

// Settings is the contents of settings.json. Unknown fields are ignored.
type Settings struct {
	Compaction      *Compaction `json:"compaction,omitempty"`
	DefaultProvider string      `json:"defaultProvider,omitempty"`
	DefaultModel    string      `json:"defaultModel,omitempty"`
	DefaultEffort   string      `json:"defaultEffort,omitempty"`
	// Renderer is "fullscreen" (default) or "inline".
	Renderer string `json:"renderer,omitempty"`
	// StatusLine replaces the built-in status line with a command's output,
	// like Claude Code's statusLine setting.
	StatusLine *StatusLine `json:"statusLine,omitempty"`
	// Hooks maps an event name (PreToolUse, PostToolUse, UserPromptSubmit,
	// Stop, PreCompact, SessionStart, SessionEnd, Notification) to matchers,
	// in Claude Code's format.
	Hooks map[string][]HookMatcher `json:"hooks,omitempty"`
	// UpdateCheck: false stops the once-a-day release check (a GET to the
	// GitHub API). atto never installs updates by itself either way.
	UpdateCheck *bool `json:"updateCheck,omitempty"`
	// DoubleEscapeAction is what Esc twice on an empty prompt opens, as in
	// pi: "tree" (default, the session tree), "fork" (pick a message to
	// fork a new session from) or "none".
	DoubleEscapeAction string `json:"doubleEscapeAction,omitempty"`
	// Mouse: false leaves the mouse to the terminal in fullscreen mode, so
	// its own selection works without a modifier key; atto then scrolls
	// with PageUp/PageDown only. ATTO_NO_MOUSE=1 does the same.
	Mouse *bool `json:"mouse,omitempty"`
	// ToolGroups: false shows every command the model runs as its own
	// block. By default consecutive commands collapse into one summary
	// line of their descriptions (the last, and any that failed, stay
	// shown); a click or ctrl+t expands it.
	ToolGroups *bool `json:"toolGroups,omitempty"`
	// SpinnerVerbs picks the words the activity line shows while a command
	// runs, one per turn: "en" (default, made-up English verbs), "ko"
	// (made-up Korean words), "ko-literary" (Korean verbs) or "off" (just
	// "Working").
	SpinnerVerbs string `json:"spinnerVerbs,omitempty"`
	// SpinnerScanner: true puts a sweeping scanner (▰▱) before the
	// activity line's word. Off by default: the word's shimmer is enough.
	SpinnerScanner bool `json:"spinnerScanner,omitempty"`
	// BranchSummary configures what going back in the session tree (/tree)
	// does with the branch being left, as pi's setting of the same name.
	BranchSummary *BranchSummary `json:"branchSummary,omitempty"`
	// Extensions configures JavaScript extensions (package extensions).
	Extensions *ExtensionSettings `json:"extensions,omitempty"`
	// Skills configures skills.
	Skills *SkillSettings `json:"skills,omitempty"`
	// ToolOutputTokenLimit caps how much of a command's output goes back to
	// the model, in tokens (about 4 bytes each), as codex's
	// tool_output_token_limit. 0 means the default, 10000. The cut is in
	// the middle, and the full output is saved to a file the model is told
	// about.
	ToolOutputTokenLimit int `json:"toolOutputTokenLimit,omitempty"`
	// ToolOutput caps the files that keep a command's full output (package
	// outputs).
	ToolOutput *ToolOutputSettings `json:"toolOutput,omitempty"`
	// BackgroundExit: false turns off the exit menu that offers "Run in
	// background" while a turn is running (experimental; default on).
	BackgroundExit *bool `json:"backgroundExit,omitempty"`
	// Remote configures /remote, which serves the TUI's session to a
	// phone or browser.
	Remote *RemoteSettings `json:"remote,omitempty"`
	// Agents configures atto agent: agents other agents start. "subagents",
	// its old name, is read when "agents" is absent.
	Agents       *AgentSettings `json:"agents,omitempty"`
	LegacyAgents *AgentSettings `json:"subagents,omitempty"`
	// Daemon: false runs the TUI in the terminal's own process instead of
	// a session worker of the atto daemon (package daemon). ATTO_NO_DAEMON=1 does
	// the same for one run.
	Daemon *bool `json:"daemon,omitempty"`
}

// DaemonOn reports whether interactive atto runs in the daemon.
func (s Settings) DaemonOn() bool { return s.Daemon == nil || *s.Daemon }

// AgentSettings configures agents in settings.json. Agents are always
// available: the "enabled", "maxDepth" and "maxConcurrent" keys older
// versions read are accepted in the file and ignored.
type AgentSettings struct {
	// Model and Effort apply to agents whose preset names none;
	// without them an agent uses the model and effort of the session
	// that starts it.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// agents is the agent settings, under either name.
func (s Settings) agents() *AgentSettings {
	if s.Agents != nil {
		return s.Agents
	}
	return s.LegacyAgents
}

// AgentDefaults is the model and effort settings give agents, if any.
func (s Settings) AgentDefaults() (model, effort string) {
	if a := s.agents(); a != nil {
		return a.Model, a.Effort
	}
	return "", ""
}

// ToolOutputSettings is settings.json's "toolOutput": how the full output of
// commands is kept under ~/.atto/outputs (compressed with zstd) when it is
// cut for the model. Sizes are in MiB; 0 or absent is the default.
type ToolOutputSettings struct {
	// FileHeadMB and FileTailMB are how much of the start and of the end of
	// one command's output its file keeps (32 each); the middle of a longer
	// output is replaced by a marker line.
	FileHeadMB int `json:"fileHeadMB,omitempty"`
	FileTailMB int `json:"fileTailMB,omitempty"`
	// TotalMB caps all saved output (1024): above it the oldest files are
	// deleted. Negative: no cap.
	TotalMB int `json:"totalMB,omitempty"`
	// MinFreeMB is the free disk space needed to save output at all
	// (1024); with less, the model gets the cut text and is told the rest
	// was not saved. Negative: no check.
	MinFreeMB int `json:"minFreeMB,omitempty"`
}

// RemoteSettings is settings.json's "remote".
type RemoteSettings struct {
	// Port is where /remote listens (all interfaces); default 7879.
	Port int `json:"port,omitempty"`
}

// SkillSettings is settings.json's "skills".
type SkillSettings struct {
	// Disabled names built-in skills not to load.
	Disabled []string `json:"disabled,omitempty"`
}

// ExtensionSettings is settings.json's "extensions".
type ExtensionSettings struct {
	// Disabled names extensions not to load.
	Disabled []string `json:"disabled,omitempty"`
	// Timeout, in seconds, bounds a handler that atto waits for (tool_call,
	// tool_result, user_prompt) and any stretch of script that runs
	// without yielding. Default 5.
	Timeout int `json:"timeout,omitempty"`
}

// ExtensionApprovalsPath records the project extensions the user approved.
func ExtensionApprovalsPath() string { return filepath.Join(Dir(), "extension-approvals.json") }

// ExtensionLogPath is where atto.log and extension errors are written.
func ExtensionLogPath() string { return filepath.Join(Dir(), "extensions.log") }

// MCPPath is the user's MCP server file, in Claude Code's .mcp.json format.
func MCPPath() string { return filepath.Join(Dir(), "mcp.json") }

// MCPApprovalsPath records the project MCP servers the user approved.
func MCPApprovalsPath() string { return filepath.Join(Dir(), "mcp-approvals.json") }

// MCPRunDir holds the endpoints of the running sessions' MCP managers.
func MCPRunDir() string { return filepath.Join(Dir(), "mcp") }

// ProjectMCPPath is a project's shared MCP server file (checked in).
func ProjectMCPPath(root string) string { return filepath.Join(root, ".mcp.json") }

// LocalMCPPath is a project's private MCP server file. It lives under
// ~/.atto, never in the repository (which could bring one of its own and
// have atto start its commands unapproved): projects/<name>-<hash of the
// absolute root>/mcp.json.
func LocalMCPPath(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return filepath.Join(Dir(), "projects", filepath.Base(root)+"-"+hex.EncodeToString(sum[:4]), "mcp.json")
}

// RepoMCPPath is where an earlier version looked for local servers, inside
// the repository. It is not read; atto only reports that it is there.
func RepoMCPPath(root string) string { return filepath.Join(root, ".atto", "mcp.json") }

// ProjectExtensionsDir is a project's own extensions directory.
func ProjectExtensionsDir(root string) string { return filepath.Join(root, ".atto", "extensions") }

// BranchSummary is settings.json's "branchSummary".
type BranchSummary struct {
	// SkipPrompt: true never asks "Summarize branch?" and goes back
	// without a summary.
	SkipPrompt bool `json:"skipPrompt,omitempty"`
}

// HookMatcher selects hooks by tool name (regexp; "" or "*" match all).
type HookMatcher struct {
	Matcher string     `json:"matcher,omitempty"`
	Hooks   []HookSpec `json:"hooks"`
}

// HookSpec is one hook: a shell command, or an HTTP endpoint that receives
// the event JSON as a POST body.
type HookSpec struct {
	Type    string            `json:"type"` // "command" or "http"
	Command string            `json:"command,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // seconds, default 60
}

// ProjectSettingsPath is a project's own settings file (hooks only).
func ProjectSettingsPath(cwd string) string { return filepath.Join(cwd, ".atto", "settings.json") }

// HookSource is the hooks one settings file defines.
type HookSource struct {
	Path  string
	Hooks map[string][]HookMatcher
}

// LoadHookSources reads the hooks of user settings (~/.atto/settings.json)
// and project settings (<cwd>/.atto/settings.json), in the order they run.
// Files that are missing or define no hooks are left out.
func LoadHookSources(cwd string) ([]HookSource, error) {
	var out []HookSource
	for i, path := range []string{SettingsPath(), ProjectSettingsPath(cwd)} {
		if i > 0 && sameHookPath(path, SettingsPath()) {
			continue
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var s Settings
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if len(s.Hooks) > 0 {
			out = append(out, HookSource{Path: path, Hooks: s.Hooks})
		}
	}
	return out, nil
}

// MergeHooks combines hook sources; later sources' hooks run after earlier
// ones'.
func MergeHooks(srcs []HookSource) map[string][]HookMatcher {
	out := map[string][]HookMatcher{}
	for _, src := range srcs {
		for ev, ms := range src.Hooks {
			out[ev] = append(out[ev], ms...)
		}
	}
	return out
}

// LoadHooks merges user hooks (~/.atto/settings.json) with project hooks
// (<cwd>/.atto/settings.json); approved project hooks run after user hooks.
func LoadHooks(cwd string) (map[string][]HookMatcher, error) {
	srcs, err := LoadHookSources(cwd)
	if err != nil {
		return nil, err
	}
	return MergeHooks(ApprovedHookSources(srcs, cwd)), nil
}

// StatusLine configures a custom status line. The command runs with a JSON
// description of the session on stdin; each line it prints becomes a status
// line (ANSI colors allowed).
type StatusLine struct {
	Type    string `json:"type"` // "command"
	Command string `json:"command"`
	// RefreshInterval, in seconds, re-runs the command periodically even if
	// nothing changed. 0 runs it only on changes.
	RefreshInterval int `json:"refreshInterval,omitempty"`
}

// LoadSettings reads settings.json; a missing file yields zero settings.
func LoadSettings() (Settings, error) {
	var s Settings
	data, err := os.ReadFile(SettingsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// UpdateSettings sets the given top-level keys in settings.json, preserving
// any other keys already present.
func UpdateSettings(kv map[string]any) error {
	return fsutil.WithFileLock(SettingsPath(), func() error {
		raw := map[string]any{}
		data, err := os.ReadFile(SettingsPath())
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if len(data) > 0 {
			if err := json.Unmarshal(data, &raw); err != nil {
				return err
			}
		}
		maps.Copy(raw, kv)
		// Migrate the older "subagents" settings key on write; "agents" wins.
		if old, ok := raw["subagents"]; ok {
			if _, ok := raw["agents"]; !ok {
				raw["agents"] = old
			}
			delete(raw, "subagents")
		}
		out, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		return fsutil.WriteAtomic(SettingsPath(), append(out, '\n'), 0o600)
	})
}

// Compaction overrides the tier-aware auto-compaction cap per provider/model.
// Limits are token caps; zero disables the tier cap, not compaction itself.
type Compaction struct {
	Limits map[string]int `json:"limits,omitempty"`
}
