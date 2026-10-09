package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/mcp"
	"github.com/sebastianrcnt/atto/skills"
)

// Origin says where the model or effort in use came from.
type Origin string

const (
	FromFlag     Origin = "flag"     // -m / -effort, or the client's request
	FromSession  Origin = "session"  // what the resumed session last used
	FromSettings Origin = "settings" // settings.json's default
	FromDefault  Origin = "default"  // the first model available; atto's default effort
	FromCommand  Origin = "command"  // chosen in the session (/model, /effort, shift+tab)
)

// Loaded is what shapes the model's context and behaviour, as a session
// loaded it: the files its system prompt was built from, the skills, the
// hooks, the configuration and the model. Front ends show it when a
// session starts and after a reload; it is never sent to the model.
type Loaded struct {
	Cwd          string         `json:"cwd"`
	Instructions []Instruction  `json:"instructions"`
	Skills       []Skill        `json:"skills"`
	SkillDirs    []string       `json:"skill_dirs"` // those that exist, highest priority first
	SkillIssues  []skills.Issue `json:"skill_issues,omitempty"`
	Hooks        []Hook         `json:"hooks"`
	// Extensions are the JavaScript extensions found, whether they run
	// or not (see package extensions).
	Extensions []extensions.Info `json:"extensions,omitempty"`
	// MCP are the MCP servers configured, started or not (see package mcp).
	MCP []mcp.Info `json:"mcp,omitempty"`
	// MCPIgnored is a <project>/.atto/mcp.json that is not read.
	MCPIgnored string       `json:"mcp_ignored,omitempty"`
	Config     []ConfigFile `json:"config"`
	// Agents says whether atto agent is on, and Presets are the
	// presets it starts agents from (listed in the prompt when on).
	Agents         bool                `json:"agents"`
	Presets        []agentstate.Preset `json:"agent_presets,omitempty"`
	PresetWarnings []string            `json:"agent_preset_warnings,omitempty"`
	// Legacy fields keep context -json consumers on the older spellings working.
	LegacyAgents         bool                `json:"subagents"`
	LegacyPresets        []agentstate.Preset `json:"subagent_presets,omitempty"`
	LegacyPresetWarnings []string            `json:"subagent_preset_warnings,omitempty"`

	Model  Choice `json:"model"`
	Effort Choice `json:"effort"`
	Prompt Prompt `json:"system_prompt"`
	// Context is what else goes to the model besides the system prompt
	// and the conversation: the tool schema, how images are sent, hook
	// output.
	Context  []Part   `json:"context"`
	Warnings []string `json:"warnings,omitempty"`
}

// Instruction is an AGENTS.md, AGENTS.override.md or CLAUDE.md file that
// was found: in the system prompt (Skipped empty), or not and why.
type Instruction struct {
	Path      string `json:"path"`
	Bytes     int    `json:"bytes,omitempty"`
	Truncated bool   `json:"truncated,omitempty"` // cut at the 32 KiB cap
	Kept      int    `json:"kept,omitempty"`      // bytes in the prompt when truncated
	Skipped   string `json:"skipped,omitempty"`
	sum       string
}

// Skill is a skill the session offers.
type Skill struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Dir    string `json:"dir"`              // the skills directory it was found in
	Hidden bool   `json:"hidden,omitempty"` // not in the prompt; run with /skill:name
	// Source is "builtin" for a skill that ships inside atto (Dir is empty:
	// it is written to the cache); empty for one found in a directory.
	Source string `json:"source,omitempty"`
	sum    string
}

// Hook is one configured hook.
type Hook struct {
	File    string `json:"file"`
	Event   string `json:"event"`
	Matcher string `json:"matcher,omitempty"`
	Type    string `json:"type"`
	Command string `json:"command"` // the command, or the URL of an http hook
	Status  string `json:"status,omitempty"`
	Approve string `json:"approve,omitempty"`
}

// ConfigFile is a configuration file atto reads.
type ConfigFile struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	Exists bool   `json:"exists"`
	sum    string
}

// Choice is the model or effort in use and where it came from.
type Choice struct {
	Name   string `json:"name"`         // display name (the effort level itself)
	ID     string `json:"id,omitempty"` // provider/id of a model
	Origin Origin `json:"source"`
	Path   string `json:"path,omitempty"` // the settings file, for FromSettings
}

// Prompt is the system prompt's size and parts.
type Prompt struct {
	Bytes int    `json:"bytes"`
	Parts []Part `json:"parts"`
}

// Part is one named source of context.
type Part struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Collect reports what ag's session loaded. hookSrc are the settings files
// its hooks came from (see LoadHooks); modelFrom and effortFrom say where
// the model and effort in use came from.
func Collect(ag *agent.Agent, hookSrc []config.HookSource, modelFrom, effortFrom Origin) Loaded {
	src := ag.Sources()
	l := Loaded{Cwd: src.Cwd}

	for _, in := range src.Instructions {
		x := Instruction{Path: in.Path, Bytes: in.Bytes, sum: sum([]byte(in.Text))}
		switch {
		case in.Kept == 0:
			x.Skipped = fmt.Sprintf("omitted: earlier files filled the %d KiB cap", agent.MaxInstructionKiB)
		case in.Kept < in.Bytes:
			x.Truncated, x.Kept = true, in.Kept
		}
		l.Instructions = append(l.Instructions, x)
	}
	for _, s := range src.Skipped {
		l.Instructions = append(l.Instructions, Instruction{Path: s.Path, Skipped: s.Reason})
	}

	for _, d := range src.SkillDirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			l.SkillDirs = append(l.SkillDirs, d)
		}
	}
	for _, s := range src.Skills {
		x := Skill{Name: s.Name, Path: s.FilePath, Hidden: s.DisableModelInvocation, Source: s.Source}
		for _, d := range l.SkillDirs {
			if within(d, s.FilePath) {
				x.Dir = d
				break
			}
		}
		if data, err := os.ReadFile(s.FilePath); err == nil {
			x.sum = sum(data)
		}
		l.Skills = append(l.Skills, x)
	}
	l.SkillIssues = src.SkillIssues

	for _, hs := range hookSrc {
		events := make([]string, 0, len(hs.Hooks))
		for ev := range hs.Hooks {
			events = append(events, ev)
		}
		sort.Strings(events)
		for _, ev := range events {
			for _, m := range hs.Hooks[ev] {
				for _, h := range m.Hooks {
					typ, cmd := h.Type, h.Command
					if typ == "" {
						typ = "command"
					}
					if typ == "http" {
						cmd = h.URL
					}
					item := Hook{File: hs.Path, Event: ev, Matcher: m.Matcher, Type: typ, Command: cmd}
					if config.IsProjectHookSource(hs.Path, ag.Cwd) {
						hook := config.ProjectHook{Path: hs.Path, Event: ev, Matcher: m.Matcher, Spec: h}
						item.Status = config.HookApprovalOf(hook)
						item.Approve = "atto trust approve hook " + hook.Name()
						if item.Status != config.HookApproved {
							l.Warnings = append(l.Warnings, "project hook "+hook.Name()+" is "+item.Status+" and will not run: "+item.Approve)
						}
					}
					l.Hooks = append(l.Hooks, item)
				}
			}
		}
	}

	if m := ExtensionsOf(ag); m != nil {
		l.Extensions = m.Report()
		l.Warnings = append(l.Warnings, extensionWarnings(l.Extensions)...)
	}

	var mcpWarn []string
	l.MCP, mcpWarn = mcpInfos(ag)
	l.Warnings = append(l.Warnings, mcpWarn...)
	if m := MCPOf(ag); m != nil {
		if p := m.Ignored(); p != "" {
			l.MCPIgnored = p
			l.Warnings = append(l.Warnings, mcpIgnoredText(p, src.Cwd))
		}
	}

	l.Config = configFiles(src.Cwd)

	if st, _ := config.LoadSettings(); st.AgentsEnabled() {
		l.Agents = true
	}
	l.Presets, l.PresetWarnings = agentstate.LoadPresets(agentstate.Dirs(src.Cwd, agent.ProjectRoot(src.Cwd)))
	l.LegacyAgents, l.LegacyPresets, l.LegacyPresetWarnings = l.Agents, l.Presets, l.PresetWarnings

	m, effort := ag.Current()
	l.Model = Choice{Name: "none", Origin: modelFrom}
	if m.Model.ID != "" {
		l.Model.Name, l.Model.ID = m.Model.DisplayName(), m.String()
	}
	l.Effort = Choice{Name: effort, Origin: effortFrom}
	for _, c := range []*Choice{&l.Model, &l.Effort} {
		if c.Origin == FromSettings {
			c.Path = config.SettingsPath()
		}
	}

	instr, sk := 0, 0
	for _, in := range src.Instructions {
		if in.Kept > 0 {
			instr++
		}
	}
	for _, s := range src.Skills {
		if !s.DisableModelInvocation {
			sk++
		}
	}
	l.Prompt = Prompt{Bytes: src.PromptBytes, Parts: []Part{
		{Name: "atto instructions", Bytes: src.PromptBytes - src.InstructionBytes - src.SkillBytes,
			Detail: "the shell tool, history, background jobs, goals"},
		{Name: "environment", Detail: fmt.Sprintf("cwd %s · %s/%s · %s · session date %s",
			ShortPath(src.Cwd), runtime.GOOS, runtime.GOARCH, src.Shell, src.Start.Format("2006-01-02"))},
	}}
	if instr > 0 {
		l.Prompt.Parts = append(l.Prompt.Parts, Part{Name: "AGENTS files", Bytes: src.InstructionBytes, Detail: plural(instr, "file")})
	}
	if sk > 0 {
		l.Prompt.Parts = append(l.Prompt.Parts, Part{Name: "skills", Bytes: src.SkillBytes, Detail: plural(sk, "skill") + " listed"})
	}
	if len(l.MCP) > 0 {
		var names []string
		for _, in := range l.MCP {
			names = append(names, in.Name)
		}
		l.Prompt.Parts = append(l.Prompt.Parts, Part{Name: "MCP servers", Detail: "one line naming " + strings.Join(names, ", ") + "; used through atto mcp in the shell"})
	}

	if l.Agents {
		l.Prompt.Parts = append(l.Prompt.Parts, Part{Name: "agents", Detail: "atto agent and " + plural(len(l.Presets), "preset")})
	}

	tool := ag.Shell.ToolName()
	l.Context = []Part{{Name: "tool", Detail: tool + " (the only tool)"}}
	if m.Model.ID != "" {
		if m.Model.Images() {
			l.Context = append(l.Context, Part{Name: "images", Detail: "sent to the model"})
		} else {
			l.Context = append(l.Context, Part{Name: "images", Detail: "replaced by a note: the model has no image input"})
		}
	}
	for _, ev := range []string{"UserPromptSubmit", "PostToolUse", "Stop"} {
		if slices.ContainsFunc(l.Hooks, func(h Hook) bool { return h.Event == ev && (h.Status == "" || h.Status == config.HookApproved) }) {
			what := map[string]string{
				"UserPromptSubmit": "can add to prompts", "PostToolUse": "can add to tool results",
				"Stop": "can keep the turn going",
			}[ev]
			l.Context = append(l.Context, Part{Name: ev + " hooks", Detail: what})
		}
	}
	for _, ev := range []string{"user_prompt", "tool_result"} {
		var names []string
		for _, e := range l.Extensions {
			if e.Status == extensions.Loaded && slices.Contains(e.Events, ev) {
				names = append(names, e.Name)
			}
		}
		if len(names) > 0 {
			what := map[string]string{"user_prompt": "can add to prompts", "tool_result": "can rewrite tool results"}[ev]
			l.Context = append(l.Context, Part{Name: ev + " extensions", Detail: what + " (" + strings.Join(names, ", ") + ")"})
		}
	}
	// Side requests extensions made (atto.complete) go to the models they
	// name, with only what the extension sent: none of this conversation.
	var calls []string
	for _, e := range l.Extensions {
		if c := completeSummary(e.Completes); c != "" {
			calls = append(calls, e.Name+" "+c)
		}
	}
	if len(calls) > 0 {
		l.Context = append(l.Context, Part{Name: "extension model calls", Detail: "side requests, not part of the conversation: " + strings.Join(calls, "; ")})
	}
	return l
}

// configFiles lists the configuration files read for a session in cwd.
// Credentials are only checked for, never read here.
func configFiles(cwd string) []ConfigFile {
	root := agent.ProjectRoot(cwd)
	files := []ConfigFile{
		{Path: config.SettingsPath(), Role: "settings"},
		{Path: config.ProjectSettingsPath(cwd), Role: "project settings (hooks)"},
		{Path: config.MCPPath(), Role: "MCP servers"},
		{Path: config.ProjectMCPPath(root), Role: "project MCP servers"},
		{Path: config.LocalMCPPath(root), Role: "local MCP servers (private)"},
		{Path: config.ModelsPath(), Role: "models"},
		{Path: config.AuthPath(), Role: "credentials"},
	}
	for i := range files {
		f := &files[i]
		if f.Role == "credentials" {
			_, err := os.Stat(f.Path)
			f.Exists = err == nil
			continue
		}
		if data, err := os.ReadFile(f.Path); err == nil {
			f.Exists, f.sum = true, sum(data)
		}
	}
	return files
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// within reports whether path is inside dir.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// ShortPath writes the home directory as ~.
func ShortPath(p string) string {
	// Only whole path elements: /Users/bob2 is not under /Users/bob.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(p, home); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
			return "~" + rest
		}
	}
	return p
}

// Size formats a byte count as "2.1 KB" (or "40 B" when tiny).
func Size(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n < 100:
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

// From says where a choice came from, for display: "from -m",
// "from ~/.atto/settings.json". flag is the flag that sets it.
func (c Choice) From(flag string) string {
	switch c.Origin {
	case FromFlag:
		return "from " + flag
	case FromSession:
		return "from the session"
	case FromSettings:
		return "from " + ShortPath(c.Path)
	case FromCommand:
		return "chosen in this session"
	case FromDefault:
		if flag == "-m" {
			return "first available"
		}
		return "default"
	}
	return ""
}

// Row is a label and a text, for display.
type Row struct{ Label, Text string }

// Section is a titled group of rows.
type Section struct {
	Title string
	Rows  []Row
}

// Summary is one row per kind of thing loaded, for the collapsed view.
func (l Loaded) Summary() []Row {
	var rows []Row

	var files []string
	skipped := 0
	for _, in := range l.Instructions {
		if in.Skipped != "" {
			skipped++
			continue
		}
		f := fmt.Sprintf("%s (%s)", ShortPath(in.Path), Size(in.Bytes))
		if in.Truncated {
			f = fmt.Sprintf("%s (%s, truncated at %d KiB)", ShortPath(in.Path), Size(in.Bytes), agent.MaxInstructionKiB)
		}
		files = append(files, f)
	}
	rows = append(rows, Row{"AGENTS.md", orNone(strings.Join(files, ", ")) + skippedNote(skipped)})

	var names []string
	for _, s := range l.Skills {
		if s.Source == skills.Builtin {
			names = append(names, s.Name+" (builtin)")
			continue
		}
		names = append(names, s.Name)
	}
	text := "none"
	if len(names) > 0 {
		text = fmt.Sprintf("%d: %s", len(names), strings.Join(names, ", "))
	}
	skipped, warned := 0, 0
	for _, is := range l.SkillIssues {
		if is.Skipped {
			skipped++
		} else {
			warned++
		}
	}
	text += skippedNote(skipped)
	if warned > 0 {
		text += fmt.Sprintf("; %s", plural(warned, "warning"))
	}
	rows = append(rows, Row{"Skills", text})

	text = "none"
	if len(l.Hooks) > 0 {
		var evs, from []string
		for _, h := range l.Hooks {
			ev := h.Event
			if h.Matcher != "" && h.Matcher != "*" {
				ev += "(" + h.Matcher + ")"
			}
			if !slices.Contains(evs, ev) {
				evs = append(evs, ev)
			}
			if f := ShortPath(h.File); !slices.Contains(from, f) {
				from = append(from, f)
			}
		}
		text = fmt.Sprintf("%d: %s · from %s", len(l.Hooks), strings.Join(evs, ", "), strings.Join(from, ", "))
		for _, status := range []string{config.HookPending, config.HookDenied} {
			count := 0
			for _, h := range l.Hooks {
				if h.Status == status {
					count++
				}
			}
			if count > 0 {
				text += fmt.Sprintf("; %d %s (off)", count, status)
			}
		}
	}
	rows = append(rows, Row{"Hooks", text})
	if len(l.Extensions) > 0 || !extensions.Supported { // slim reports ignored files
		rows = append(rows, Row{"Extensions", extensionSummary(l.Extensions)})
	}

	if len(l.MCP) > 0 { // likewise
		rows = append(rows, Row{"MCP", mcpSummary(l.MCP)})
	} else if l.MCPIgnored != "" {
		rows = append(rows, Row{"MCP", "ignored: " + ShortPath(l.MCPIgnored)})
	}

	if l.Agents { // off by default: no row then
		var names []string
		for _, p := range l.Presets {
			names = append(names, p.Name)
		}
		rows = append(rows, Row{"Agents", "roles: " + strings.Join(names, ", ")})
	}

	rows = append(rows, Row{"Model", l.modelText()})

	var cfg []string
	for _, f := range l.Config {
		if f.Exists && f.Role != "credentials" {
			cfg = append(cfg, ShortPath(f.Path))
		}
	}
	rows = append(rows, Row{"Config", orNone(strings.Join(cfg, ", "))})

	parts := []string{"base", "environment"}
	if n := l.promptFiles(); n > 0 {
		parts = append(parts, plural(n, "AGENTS file"))
	}
	if n := len(l.Skills) - l.hiddenSkills(); n > 0 {
		parts = append(parts, plural(n, "skill"))
	}
	rows = append(rows, Row{"Prompt", fmt.Sprintf("%s system prompt: %s", Size(l.Prompt.Bytes), strings.Join(parts, ", "))})
	return rows
}

// promptFiles counts the AGENTS files in the prompt.
func (l Loaded) promptFiles() int {
	n := 0
	for _, in := range l.Instructions {
		if in.Skipped == "" {
			n++
		}
	}
	return n
}

func (l Loaded) hiddenSkills() int {
	n := 0
	for _, s := range l.Skills {
		if s.Hidden {
			n++
		}
	}
	return n
}

func (l Loaded) modelText() string {
	text := l.Model.Name
	if l.Effort.Name != "" {
		text += " · " + l.Effort.Name
	}
	mf, ef := l.Model.From("-m"), l.Effort.From("-effort")
	switch {
	case l.Model.ID == "":
		text = "none (/login, or add a provider to " + ShortPath(config.ModelsPath()) + ")"
	case mf == "":
	case mf == ef || l.Effort.Name == "":
		text += " (" + mf + ")"
	default:
		text += " (model " + mf + ", effort " + ef + ")"
	}
	return text
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func skippedNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("; %d skipped", n)
}

// Details is everything in Loaded, grouped, for the expanded view and
// atto context.
func (l Loaded) Details() []Section {
	var out []Section

	s := Section{Title: fmt.Sprintf("AGENTS files (in prompt order, %d KiB cap)", agent.MaxInstructionKiB)}
	for _, in := range l.Instructions {
		text := Size(in.Bytes)
		switch {
		case in.Skipped != "" && in.Bytes > 0:
			text += " · " + in.Skipped
		case in.Skipped != "":
			text = "skipped: " + in.Skipped
		case in.Truncated:
			text += fmt.Sprintf(" · truncated at %d KiB (%s kept)", agent.MaxInstructionKiB, Size(in.Kept))
		}
		s.Rows = append(s.Rows, Row{ShortPath(in.Path), text})
	}
	if len(s.Rows) == 0 {
		s.Rows = append(s.Rows, Row{"none", "looked for AGENTS.override.md, AGENTS.md and CLAUDE.md in " +
			ShortPath(config.Dir()) + " and from the project root down to " + ShortPath(l.Cwd)})
	}
	out = append(out, s)

	s = Section{Title: "Skills"}
	if len(l.SkillDirs) > 0 {
		var dirs []string
		for _, d := range l.SkillDirs {
			dirs = append(dirs, ShortPath(d))
		}
		s.Title += " (from " + strings.Join(dirs, ", ") + ")"
	}
	for _, sk := range l.Skills {
		text := ShortPath(sk.Path)
		if sk.Source == skills.Builtin {
			text += " · builtin"
		}
		if sk.Hidden {
			text += " · not in the prompt; run with /skill:" + sk.Name
		}
		s.Rows = append(s.Rows, Row{sk.Name, text})
	}
	for _, is := range l.SkillIssues {
		label := "warning"
		if is.Skipped {
			label = "skipped"
		}
		s.Rows = append(s.Rows, Row{label, ShortPath(is.Path) + ": " + is.Reason})
	}
	if len(s.Rows) == 0 {
		s.Rows = append(s.Rows, Row{"none", "no SKILL.md in " + ShortPath(config.SkillsDir()) + " or the project's .atto/.claude/.agents skills"})
	}
	out = append(out, s)

	s = Section{Title: "Hooks"}
	for _, h := range l.Hooks {
		label := h.Event
		if h.Matcher != "" {
			label += " [" + h.Matcher + "]"
		}
		cmd := h.Command
		if h.Type == "http" {
			cmd = "POST " + cmd
		}
		text := clipWithEllipsis(oneLine(cmd), 80) + " · " + ShortPath(h.File)
		if h.Status != "" {
			text += " · " + h.Status
			if h.Status != config.HookApproved {
				text += ": " + h.Approve
			}
		}
		s.Rows = append(s.Rows, Row{label, text})
	}
	if len(s.Rows) == 0 {
		s.Rows = append(s.Rows, Row{"none", "configure them under \"hooks\" in " + ShortPath(config.SettingsPath())})
	}
	out = append(out, s)

	s = Section{Title: "Extensions"}
	if !extensions.Supported {
		s.Rows = append(s.Rows, Row{"slim", extensions.UnsupportedMessage(extensions.IgnoredCount(l.Extensions))})
	}
	for _, e := range l.Extensions {
		s.Rows = append(s.Rows, extensionRow(e))
	}
	if len(s.Rows) == 0 {
		s.Rows = append(s.Rows, Row{"none", "put .ts or .js files in " + ShortPath(config.ExtensionsDir()) +
			" or the project's .atto/extensions; see atto extensions docs"})
	}
	out = append(out, s)

	s = Section{Title: "MCP servers"}
	for _, in := range l.MCP {
		s.Rows = append(s.Rows, mcpRow(in))
	}
	if l.MCPIgnored != "" {
		s.Rows = append(s.Rows, Row{"ignored", mcpIgnoredText(l.MCPIgnored, l.Cwd)})
	}
	if len(s.Rows) == 0 {
		s.Rows = append(s.Rows, Row{"none", "atto mcp add, or write " + ShortPath(config.MCPPath()) +
			" (Claude Code's .mcp.json format); the project's .mcp.json needs approval"})
	}
	out = append(out, s)

	s = Section{Title: "Agent roles (atto agent)"}
	if !l.Agents {
		s.Title += " · off: \"agents\": {\"enabled\": true} in " + ShortPath(config.SettingsPath()) + " turns them on"
	}
	for _, p := range l.Presets {
		text := "built-in"
		if p.Path != "" {
			text = ShortPath(p.Path)
		}
		if p.Model != "" {
			text += " · model " + p.Model
		}
		if p.Effort != "" {
			text += " · effort " + p.Effort
		}
		if p.Description != "" {
			text += " · " + clipWithEllipsis(oneLine(p.Description), 80)
		}
		s.Rows = append(s.Rows, Row{p.Name, text})
	}
	for _, w := range l.PresetWarnings {
		s.Rows = append(s.Rows, Row{"warning", w})
	}
	out = append(out, s)

	s = Section{Title: "Model"}
	if l.Model.ID == "" {
		s.Rows = append(s.Rows, Row{"model", l.modelText()})
	} else {
		s.Rows = append(s.Rows, Row{"model", withFrom(fmt.Sprintf("%s (%s)", l.Model.Name, l.Model.ID), l.Model.From("-m"))})
		if l.Effort.Name != "" {
			s.Rows = append(s.Rows, Row{"effort", withFrom(l.Effort.Name, l.Effort.From("-effort"))})
		}
	}
	out = append(out, s)

	s = Section{Title: "Configuration"}
	for _, f := range l.Config {
		text := f.Role
		if !f.Exists {
			text += " · not found"
		}
		s.Rows = append(s.Rows, Row{ShortPath(f.Path), text})
	}
	out = append(out, s)

	s = Section{Title: "System prompt (" + Size(l.Prompt.Bytes) + ")"}
	for _, p := range l.Prompt.Parts {
		text := p.Detail
		if p.Bytes > 0 {
			text = Size(p.Bytes) + " · " + text
		}
		s.Rows = append(s.Rows, Row{p.Name, text})
	}
	out = append(out, s)

	s = Section{Title: "Also sent"}
	for _, p := range l.Context {
		s.Rows = append(s.Rows, Row{p.Name, p.Detail})
	}
	out = append(out, s)

	if len(l.Warnings) > 0 {
		s = Section{Title: "Warnings"}
		for _, w := range l.Warnings {
			s.Rows = append(s.Rows, Row{"!", w})
		}
		out = append(out, s)
	}
	return out
}

func withFrom(s, from string) string {
	if from == "" {
		return s
	}
	return s + " · " + from
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clipWithEllipsis(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// FormatRows lays rows out as "label  text" lines with the labels in a
// column (capped at max characters), each line starting with indent.
func FormatRows(rows []Row, indent string, max int) []string {
	w := 0
	for _, r := range rows {
		if n := len([]rune(r.Label)); n > w && n <= max {
			w = n
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		pad := max0(w - len([]rune(r.Label)))
		out[i] = indent + r.Label + strings.Repeat(" ", pad) + "  " + r.Text
	}
	return out
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// Text renders the details as plain text, for atto context.
func (l Loaded) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "What atto loads for a session in %s:\n", ShortPath(l.Cwd))
	for _, s := range l.Details() {
		fmt.Fprintf(&b, "\n%s\n", s.Title)
		for _, line := range FormatRows(s.Rows, "  ", 40) {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// Change is a difference between two Loaded reports.
type Change struct {
	Kind string `json:"kind"` // "added", "removed" or "changed"
	What string `json:"what"` // "AGENTS file", "skill", "hook", "config"
	Name string `json:"name"` // a path, a skill or hook name
}

func (c Change) String() string { return c.Kind + " " + c.What + " " + c.Name }

// Diff lists what changed from prev to cur.
func Diff(prev, cur Loaded) []Change {
	var out []Change
	diff := func(what string, a, b map[string]string, order []string) {
		for _, k := range order {
			old, inA := a[k]
			nw, inB := b[k]
			switch {
			case inA && !inB:
				out = append(out, Change{"removed", what, k})
			case !inA && inB:
				out = append(out, Change{"added", what, k})
			case old != nw:
				out = append(out, Change{"changed", what, k})
			}
		}
	}
	keys := func(a, b map[string]string) []string {
		var ks []string
		for k := range a {
			ks = append(ks, k)
		}
		for k := range b {
			if _, ok := a[k]; !ok {
				ks = append(ks, k)
			}
		}
		sort.Strings(ks)
		return ks
	}

	instr := func(l Loaded) map[string]string {
		m := map[string]string{}
		for _, in := range l.Instructions {
			if in.Skipped == "" || in.Bytes > 0 {
				m[ShortPath(in.Path)] = fmt.Sprintf("%s %v %d %s", in.sum, in.Truncated, in.Kept, in.Skipped)
			}
		}
		return m
	}
	a, b := instr(prev), instr(cur)
	diff("AGENTS file", a, b, keys(a, b))

	sk := func(l Loaded) map[string]string {
		m := map[string]string{}
		for _, s := range l.Skills {
			m[s.Name] = s.Path + " " + s.sum + fmt.Sprint(s.Hidden)
		}
		return m
	}
	a, b = sk(prev), sk(cur)
	diff("skill", a, b, keys(a, b))

	hk := func(l Loaded) map[string]string {
		m := map[string]string{}
		for _, h := range l.Hooks {
			name := h.Event
			if h.Matcher != "" {
				name += " [" + h.Matcher + "]"
			}
			m[name+": "+clipWithEllipsis(oneLine(h.Command), 60)] = h.File + " " + h.Type + " " + h.Status
		}
		return m
	}
	a, b = hk(prev), hk(cur)
	diff("hook", a, b, keys(a, b))

	ext := func(l Loaded) map[string]string {
		m := map[string]string{}
		for _, e := range l.Extensions {
			m[e.Name] = e.Path + " " + e.Hash + " " + e.Status + " " + e.Error
		}
		return m
	}
	a, b = ext(prev), ext(cur)
	diff("extension", a, b, keys(a, b))

	mc := func(l Loaded) map[string]string {
		m := map[string]string{}
		for _, in := range l.MCP {
			gate := ""
			if in.Status == mcp.NeedsApproval || in.Status == mcp.DeniedStatus {
				gate = in.Status
			}
			m[in.Name] = in.Scope + " " + in.Path + " " + in.Hash + " " + gate
		}
		return m
	}
	a, b = mc(prev), mc(cur)
	diff("MCP server", a, b, keys(a, b))

	cfg := func(l Loaded) map[string]string {
		m := map[string]string{}
		for _, f := range l.Config {
			if f.Exists {
				m[ShortPath(f.Path)] = f.sum
			}
		}
		return m
	}
	a, b = cfg(prev), cfg(cur)
	diff("config", a, b, keys(a, b))
	return out
}
