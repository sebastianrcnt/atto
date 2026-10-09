package server

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/skills"
)

// Slash commands. The runtime runs the ones that act on the session (they
// may be queued behind a turn, and work the same from every client);
// Local ones are the terminal's own (pickers, the renderer, quitting) and
// clients that have no such thing say so. commands/list is the whole
// catalog: built-in commands, then the extensions' and one /skill:<name>
// per skill.

// CommandInfo describes a slash command.
type CommandInfo struct {
	Name  string `json:"name"`
	Args  string `json:"args,omitempty"`
	Desc  string `json:"description"`
	Local bool   `json:"local,omitempty"` // the client's own
	// Origin is "builtin", "extension" (Ext names it) or "skill".
	Origin string `json:"origin"`
	Ext    string `json:"extension,omitempty"`
}

// Builtins are atto's built-in commands, in the order the terminal lists
// them.
var Builtins = []CommandInfo{
	{Name: "model", Args: "[id]", Desc: "Switch model"},
	{Name: "effort", Args: "[level]", Desc: "Set reasoning effort (also shift+tab)"},
	{Name: "compact", Desc: "Compact the conversation into handoff notes"},
	{Name: "copy", Desc: "Copy the last answer (works over SSH via OSC 52)", Local: true},
	{Name: "context", Args: "[system|long|normal]", Desc: "Show what fills the context and cache use"},
	{Name: "reload", Desc: "Re-read AGENTS.md, skills, hooks, extensions, settings and models"},
	{Name: "extensions", Args: "[approve <name>]", Desc: "List extensions, or approve a project extension"},
	{Name: "request", Desc: "Save the raw last request to a file", Local: true},
	{Name: "debug", Desc: "Save a heap profile and memory figures to ~/.atto/debug", Local: true},
	{Name: "login", Args: "[provider]", Desc: "Sign in with an account or save an API key", Local: true},
	{Name: "logout", Args: "[provider]", Desc: "Remove stored credentials", Local: true},
	{Name: "resume", Desc: "Switch session in the agent center", Local: true},
	{Name: "sessions", Desc: "Pick, archive, rename or preview saved conversations", Local: true},
	{Name: "tree", Desc: "Go back to any point of the conversation (also esc esc)", Local: true},
	{Name: "fork", Desc: "Start a new conversation from an earlier message", Local: true},
	{Name: "name", Args: "<name>", Desc: "Name this conversation"},
	{Name: "rename", Args: "<name>", Desc: "Rename this conversation"},
	{Name: "archive", Desc: "Archive this conversation and start a new one", Local: true},
	{Name: "goal", Args: "[objective|clear|edit|pause|resume]", Desc: "Set or view the goal for a long-running task"},
	{Name: "jobs", Desc: "List background jobs and monitors", Local: true},
	{Name: "stop", Desc: "Stop all background jobs"},
	{Name: "timer", Args: "<when> <msg>", Desc: "Wake the agent later (10m, 15:30)"},
	{Name: "timers", Desc: "List pending timers", Local: true},
	{Name: "tui", Args: "[auto|fullscreen|inline]", Desc: "Choose the renderer (fullscreen or inline)", Local: true},
	{Name: "remote", Desc: "The web UI is being rebuilt; how to attach other clients meanwhile", Local: true},
	{Name: "clear", Desc: "Start a new conversation", Local: true},
	{Name: "new", Desc: "Start a new conversation", Local: true},
	{Name: "agents", Desc: "Every atto session, its goal and subagents (also ← on an empty prompt)", Local: true},
	{Name: "close", Desc: "Stop this session and its work, then exit", Local: true},
	{Name: "quit", Desc: "Leave this client (daemon sessions keep running)", Local: true},
	{Name: "exit", Desc: "Exit atto", Local: true},
}

func init() {
	for i := range Builtins {
		Builtins[i].Origin = "builtin"
	}
}

// commands is the thread's catalog.
func (t *thread) commands() []CommandInfo {
	all := slices.Clone(Builtins)
	all = append(all, t.extensionCommands()...)
	sk, _ := t.agent.Skills()
	for _, s := range sk {
		all = append(all, CommandInfo{Name: "skill:" + s.Name, Args: "[text]", Desc: s.Description, Origin: "skill"})
	}
	return all
}

// extensionCommands are the slash commands extensions registered, except
// those a built-in command shadows.
func (t *thread) extensionCommands() []CommandInfo {
	if t.ext == nil {
		return nil
	}
	var out []CommandInfo
	for _, c := range t.ext.Commands() {
		if slices.ContainsFunc(Builtins, func(b CommandInfo) bool { return b.Name == c.Name }) {
			continue
		}
		if t.ext.NativeCommand(c) {
			out = append(out, CommandInfo{Name: c.Name, Args: "[args]", Desc: c.Description, Origin: "builtin"})
			continue
		}
		desc := c.Description
		if desc != "" {
			desc += " "
		}
		desc += "(extension " + c.Ext + ")"
		out = append(out, CommandInfo{Name: c.Name, Args: "[args]", Desc: desc, Origin: "extension", Ext: c.Ext})
	}
	return out
}

// catalogVersion changes whenever the catalog may have: skills rescanned
// or extension commands registered.
func (t *thread) catalogVersion() string {
	sk, _ := t.agent.Skills()
	var ver uint64
	if t.ext != nil {
		ver = t.ext.CommandsVersion()
	}
	return fmt.Sprintf("%d.%d.%p", ver, len(sk), firstSkill(sk))
}

func firstSkill(sk []skills.Skill) *skills.Skill {
	if len(sk) == 0 {
		return nil
	}
	return &sk[0]
}

// catalogChanged tells clients when the command catalog changed.
func (t *thread) catalogChanged() {
	if v := t.catalogVersion(); v != t.catalogVer {
		t.catalogVer = v
		t.publish("commands/changed", map[string]any{})
	}
}

// ResolveCommand finds the command text names among cmds: an exact name,
// else a unique prefix. Skills are named in full.
func ResolveCommand(cmds []CommandInfo, text string) (CommandInfo, string, error) {
	name, arg, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	arg = strings.TrimSpace(arg)
	var match []CommandInfo
	for _, c := range cmds {
		if c.Origin == "skill" {
			if c.Name == name {
				return c, arg, nil
			}
			continue
		}
		if c.Name == name {
			return c, arg, nil
		}
		if strings.HasPrefix(c.Name, name) {
			match = append(match, c)
		}
	}
	switch len(match) {
	case 0:
		if name == "detach" {
			return CommandInfo{}, arg, fmt.Errorf("/detach was removed; use /quit to leave and atto resume to return.")
		}
		return CommandInfo{}, arg, fmt.Errorf("Unknown command /%s.", name)
	case 1:
		return match[0], arg, nil
	}
	return CommandInfo{}, arg, fmt.Errorf("Ambiguous command /%s.", name)
}

// runCommand runs a slash command for client.
func (t *thread) runCommand(client, text string) {
	if strings.HasPrefix(text, "/skill:") {
		t.runSkill(client, text)
		return
	}
	c, arg, err := ResolveCommand(append(slices.Clone(Builtins), t.extensionCommands()...), text)
	if err != nil {
		t.notice("", "%s", err.Error())
		return
	}
	if c.Origin == "extension" {
		if t.ext == nil || !t.ext.RunCommand(c.Name, arg) {
			t.notice("", "The extension command /%s is gone (reloaded?).", c.Name)
		}
		return
	}
	switch c.Name {
	case "diff", "autorename":
		if t.ext != nil {
			t.ext.RunCommand(c.Name, arg)
		}
	case "model":
		t.cmdModel(arg)
	case "effort":
		t.cmdEffort(arg)
	case "compact":
		t.cmdCompact()
	case "context":
		t.cmdContext(arg)
	case "reload":
		t.requestReload(false)
	case "extensions":
		t.cmdExtensions(arg)
	case "name", "rename":
		t.cmdName(arg)
	case "goal":
		t.cmdGoal(client, arg)
	case "stop":
		n := jobs.KillAll(t.id)
		t.setCounts(0, t.timerCount)
		t.notice("", "Stopped %d background jobs.", n)
	case "timer":
		t.cmdTimer(arg)
	default:
		t.notice("", "/%s is a command of the terminal.", c.Name)
	}
}

// runSkill sends "/skill:name text" as a user message made of the skill's
// instructions and the text, the way pi expands it.
func (t *thread) runSkill(client, text string) {
	sk, _ := t.agent.Skills()
	msg, ok, err := skills.Expand(sk, text)
	switch {
	case err != nil:
		t.errorNotice(err)
	case !ok:
		t.notice("", "Unknown skill: %s", strings.Fields(text)[0])
	case t.noModel():
		t.recover(client, false, []string{text}, nil)
	case t.turns.Busy && t.runKind == "turn":
		t.steer(client, msg)
	case t.turns.Busy:
		t.enqueue(client, msg, nil)
	default:
		t.runTurn(t.newInput(client, msg, nil), false)
	}
}

func (t *thread) cmdModel(arg string) {
	if arg == "" {
		t.notice("", "Usage: /model <provider/id>")
		return
	}
	ref, ok := t.models.Find("", arg)
	if !ok {
		t.notice("", "Unknown model %q.", arg)
		return
	}
	t.setModel(ref, true)
	t.notice("", "Model set to %s (%s).", ref.Model.DisplayName(), ref.ProviderName)
}

func (t *thread) cmdEffort(arg string) {
	levels := t.model().Model.Levels()
	if len(levels) == 0 {
		t.notice("", "%s has no effort levels.", t.model().Model.DisplayName())
		return
	}
	if !slices.Contains(levels, arg) {
		t.notice("", "Unknown effort %q. Levels: %s.", arg, strings.Join(levels, ", "))
		return
	}
	t.setEffort(arg, true)
	t.notice("", "Effort set to %s.", arg)
}

// setModel makes ref the model; saveDefault also makes it the default in
// settings.json, as the terminal's /model does.
func (t *thread) setModel(ref config.ModelRef, saveDefault bool) {
	t.agent.SetModel(ref)
	t.modelFrom = core.FromCommand
	if saveDefault {
		err := config.UpdateSettings(map[string]any{
			"defaultProvider": ref.ProviderName,
			"defaultModel":    ref.Model.ID,
			"defaultEffort":   t.effort(),
		})
		if err != nil {
			t.errorNotice(err)
		}
	}
	t.updated()
}

func (t *thread) effort() string {
	_, e := t.agent.Current()
	return e
}

func (t *thread) setEffort(level string, saveDefault bool) {
	t.agent.SetEffort(level)
	t.effortFrom = core.FromCommand
	if saveDefault {
		if err := config.UpdateSettings(map[string]any{"defaultEffort": level}); err != nil {
			t.errorNotice(err)
		}
	}
	t.updated()
}

func (t *thread) cmdCompact() {
	if t.turns.Busy {
		t.enqueue("", "/compact", nil)
		return
	}
	t.recordSettings()
	t.start("compact", "Compacting context", t.agent.Compact)
}

// cmdContext: long and normal change the context mode; anything else is
// for the terminal's /context report (thread/context).
func (t *thread) cmdContext(arg string) {
	switch arg {
	case "long", "normal":
		t.setContextMode(arg == "long")
	default:
		t.notice("", "/context is shown by the terminal; thread/context has its data.")
	}
}

func (t *thread) setContextMode(long bool) {
	t.agent.SetLongContext(long)
	t.sess.Append(session.Entry{Type: session.TypeContext, LongContext: long})
	t.updated()
}

func (t *thread) cmdExtensions(arg string) {
	if !extensions.Supported {
		t.notice("", "%s", extensions.UnsupportedMessage(extensions.IgnoredCount(extensions.Inspect(t.cwd))))
		return
	}
	fields := strings.Fields(arg)
	if len(fields) == 2 && fields[0] == "approve" {
		s, err := extensions.Approve(t.cwd, fields[1])
		if err != nil {
			t.errorNotice(err)
			return
		}
		t.notice("", "Approved %s (%s); reloading.", s.Name, core.ShortPath(s.Path))
		t.requestReload(false)
		return
	}
	t.notice("", "Usage: /extensions [approve <name>]")
}

// nameSession names the conversation (/name, and extensions).
func (t *thread) nameSession(name string) {
	t.name = name
	t.sess.Append(session.Entry{Type: session.TypeName, Name: name})
	t.updated()
}

func (t *thread) cmdName(arg string) {
	if arg == "" {
		if t.name == "" {
			t.notice("", "This conversation has no name. Usage: /name <name>")
		} else {
			t.notice("", "This conversation is named %q.", t.name)
		}
		return
	}
	t.nameSession(arg)
	t.notice("", "Named this conversation %q.", arg)
}

// cmdTimer: /timer 10m <message>, /timer 15:30 <message>, /timer cancel <id>.
func (t *thread) cmdTimer(arg string) {
	when, msg, _ := strings.Cut(strings.TrimSpace(arg), " ")
	if when == "cancel" {
		if err := events.CancelTimer(t.id, strings.TrimSpace(msg)); err != nil {
			t.errorNotice(err)
		} else {
			t.notice("", "Timer canceled.")
		}
		return
	}
	if when == "" || strings.TrimSpace(msg) == "" {
		t.notice("", "Usage: /timer <10m|15:30> <message> — the message is sent to the agent when it fires.")
		return
	}
	due, err := events.ParseWhen(when, time.Now())
	if err != nil {
		t.errorNotice(err)
		return
	}
	tm, err := events.AddTimer(t.id, due, strings.TrimSpace(msg))
	if err != nil {
		t.errorNotice(err)
		return
	}
	t.setCounts(t.jobCount, t.timerCount+1)
	t.notice("", "Timer %s set for %s.", tm.ID, due.Format("15:04:05"))
}
