package app

import (
	"os"
	"slices"
	"strings"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/skills"
	"github.com/sebastianrcnt/atto/tui"
)

type command struct {
	name string
	args string
	desc string
	run  func(a *App, arg string)
}

var commands []command

func init() {
	commands = []command{
		{"model", "[id]", "Switch model", (*App).cmdModel},
		{"effort", "[level]", "Set reasoning effort (also shift+tab)", (*App).cmdEffort},
		{"compact", "", "Compact the conversation into handoff notes", (*App).cmdCompact},
		{"copy", "", "Copy the last answer (works over SSH via OSC 52)", (*App).cmdCopy},
		{"context", "[system|long|normal]", "Show what fills the context and cache use", (*App).cmdContext},
		{"reload", "", "Re-read AGENTS.md, skills, hooks, extensions, settings and models", (*App).cmdReload},
		{"extensions", "[approve <name>]", "List extensions, or approve a project extension", (*App).cmdExtensions},
		{"request", "", "Save the raw last request to a file", (*App).cmdRequest},
		{"debug", "", "Save a heap profile and memory figures to ~/.atto/debug", (*App).cmdDebug},
		{"login", "[provider]", "Sign in with an account or save an API key", (*App).cmdLogin},
		{"logout", "[provider]", "Remove stored credentials", (*App).cmdLogout},
		{"resume", "", "Resume a saved conversation (the agent center's Inactive tab)", (*App).cmdResume},
		{"sessions", "", "Pick, archive, rename or preview saved conversations", (*App).cmdSessions},
		{"tree", "", "Go back to any point of the conversation (also esc esc)", (*App).cmdTree},
		{"fork", "", "Start a new conversation from an earlier message", (*App).cmdFork},
		{"name", "<name>", "Name this conversation", (*App).cmdName},
		{"rename", "<name>", "Rename this conversation", (*App).cmdName},
		{"archive", "", "Archive this conversation and start a new one", (*App).cmdArchive},
		{"goal", "[objective|clear|edit|pause|resume]", "Set or view the goal for a long-running task", (*App).cmdGoal},
		{"jobs", "", "List background jobs and monitors", (*App).cmdJobs},
		{"stop", "", "Stop all background jobs", (*App).cmdStop},
		{"timer", "<when> <msg>", "Wake the agent later (10m, 15:30)", (*App).cmdTimer},
		{"timers", "", "List pending timers", (*App).cmdTimers},
		{"tui", "[auto|fullscreen|inline]", "Choose the renderer (fullscreen or inline)", (*App).cmdTui},
		{"remote", "[on [port]|off]", "Control this session from a phone or browser (QR code)", (*App).cmdRemote},
		{"clear", "", "Start a new conversation", (*App).cmdClear},
		{"agents", "", "Every atto session, its goal and subagents (also ← on an empty prompt)", (*App).cmdAgents},
		{"detach", "", "Leave atto running in the daemon (atto attach returns)", (*App).cmdDetach},
		{"quit", "", "Exit atto", (*App).cmdQuit},
		{"exit", "", "Exit atto", (*App).cmdQuit},
	}
}

// maxSuggestions is how many commands the list shows at once (pi: 5).
const maxSuggestions = 5

// suggestions is the slash-command list: a SelectList driven by the editor's
// text rather than its own filter. It is closed (matches nothing) unless the
// text is a partly typed "/name", or after Esc for that same text.
func (a *App) suggestions() *tui.SelectList {
	cmds := a.allCommands()
	if a.sugList != nil {
		// A resumed or cleared session rescans the skills, and extensions
		// add commands; follow them.
		if a.sugGen != a.cmds.gen {
			a.sugList.Items = commandItems(cmds)
			a.sugGen = a.cmds.gen
		}
		return a.sugList
	}
	l := &tui.SelectList{MaxVisible: maxSuggestions, Indent: " ", LabelWidth: 18, Items: commandItems(cmds)}
	a.sugGen = a.cmds.gen
	l.Source = a.editor.Text
	l.Match = func(it tui.SelectItem, t string) bool {
		if !strings.HasPrefix(t, "/") || strings.ContainsAny(t, " \n") || t == a.sugDismissed {
			return false
		}
		return strings.HasPrefix(it.Value, t[1:])
	}
	a.sugList = l
	return l
}

func commandItems(cmds []command) []tui.SelectItem {
	var items []tui.SelectItem
	for _, c := range cmds {
		name := "/" + c.name
		if c.args != "" {
			name += " " + c.args
		}
		items = append(items, tui.SelectItem{Label: name, Detail: c.desc, Value: c.name, Data: c})
	}
	return items
}

// suggestionKey handles the keys of an open command list, like pi: up/down
// move, tab completes, enter completes and runs (or only completes when the
// command needs an argument), esc closes the list. Without one, an open "@"
// file list gets them (mention.go).
func (a *App) suggestionKey(key string) bool {
	l := a.suggestions()
	it, ok := l.Current()
	if !ok {
		return a.mentionKey(key)
	}
	c := it.Data.(command)
	switch key {
	case "up":
		l.Move(-1)
	case "down":
		l.Move(1)
	case "tab":
		a.editor.SetText("/" + c.name + " ")
	case "enter":
		if strings.HasPrefix(c.args, "<") { // a required argument
			a.editor.SetText("/" + c.name + " ")
			return true
		}
		a.editor.SetText("/" + c.name)
		return false // the editor submits it, recording it in its history
	case "escape":
		a.sugDismissed = a.editor.Text()
	default:
		return false
	}
	return true
}

func (a *App) renderSuggestions(width int) []string {
	if a.modal != nil {
		return nil
	}
	if lines := a.suggestions().Render(width); len(lines) > 0 {
		return lines
	}
	return a.renderMentions(width)
}

// cmdCache is allCommands' result and what it was built from.
type cmdCache struct {
	skills []skills.Skill
	ext    *extensions.Manager
	extVer uint64
	all    []command
	gen    int // counts rebuilds
}

// allCommands is the built-in commands plus one /skill:<name> per skill of
// this session, like pi. Skills hidden from the model are listed too: the
// command is how you run them. It is called every frame, so the list is
// kept until the skills (a rescan makes a new slice) or the extension
// commands change.
func (a *App) allCommands() []command {
	if a.agent == nil {
		return commands
	}
	sk, _ := a.agent.Skills()
	var ver uint64
	if a.ext != nil {
		ver = a.ext.CommandsVersion()
	}
	c := &a.cmds
	if c.all != nil && c.ext == a.ext && c.extVer == ver && len(c.skills) == len(sk) && (len(sk) == 0 || &c.skills[0] == &sk[0]) {
		return c.all
	}
	all := slices.Clone(commands)
	all = append(all, a.extensionCommands()...)
	for _, s := range sk {
		all = append(all, command{"skill:" + s.Name, "[text]", s.Description, (*App).cmdSkill})
	}
	c.skills, c.ext, c.extVer, c.all = sk, a.ext, ver, all
	c.gen++
	return all
}

// cmdSkill sends "/skill:name text" as a user message made of the skill's
// instructions and the text, the way pi expands it. It takes the whole
// command text, not just an argument (see runCommand).
func (a *App) cmdSkill(text string) {
	sk, _ := a.agent.Skills()
	msg, ok, err := skills.Expand(sk, text)
	switch {
	case err != nil:
		a.errorNotice(err)
	case !ok:
		a.notice("Unknown skill: %s", strings.Fields(text)[0])
	case a.noModel():
		a.restoreToEditor([]string{text})
	case a.busy && a.runKind == "turn":
		a.steer(msg)
	case a.busy:
		a.enqueue(msg, nil)
	default:
		a.startTurn(msg, nil)
	}
}

func (a *App) runCommand(text string) {
	if strings.HasPrefix(text, "/skill:") {
		a.cmdSkill(text)
		return
	}
	name, arg, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	arg = strings.TrimSpace(arg)
	var match []command
	// Built-in commands first, then the extensions': an exact name wins,
	// else a unique prefix.
	for _, c := range append(slices.Clone(commands), a.extensionCommands()...) {
		if c.name == name {
			match = []command{c}
			break
		}
		if strings.HasPrefix(c.name, name) {
			match = append(match, c)
		}
	}
	switch len(match) {
	case 0:
		a.notice("Unknown command /%s.", name)
	case 1:
		match[0].run(a, arg)
	default:
		a.notice("Ambiguous command /%s.", name)
	}
}

// openModal shows m in place of the editor; a prompt (see remoteprompt.go)
// shows on /remote's clients too.
func (a *App) openModal(m modal) {
	a.promptGone() // one open modal replaced by another
	a.modal = m
	a.ui.SetFocus(m)
	a.promptOpened(m)
}

func (a *App) closeModal() {
	a.promptGone()
	a.modal = nil
	a.ui.SetFocus(a.editor)
	a.maybeSendNextQueued()
}

func (a *App) cmdModel(arg string) {
	if arg != "" {
		ref, ok := a.models.Find("", arg)
		if !ok {
			a.notice("Unknown model %q.", arg)
			return
		}
		a.setModel(ref)
		return
	}
	a.modelPicker("")
}

// modelPicker opens the model list, optionally for one provider.
func (a *App) modelPicker(provider string) {
	cur := a.model()
	p := &tui.SelectList{Title: "Select model (enter to choose, esc to cancel)", Filterable: true}
	refs := a.models.List()
	if len(refs) == 0 {
		a.notice("%s", core.NoModelsHint())
		return
	}
	for _, r := range refs {
		if provider != "" && r.ProviderName != provider {
			continue
		}
		i := len(p.Items)
		detail := r.ProviderName
		if r.APIKey == "" && r.Provider.Env != nil {
			detail += " (no key: /login)"
		}
		if r.Model.ContextWindow > 0 {
			detail += " · " + tui.FormatTokens(r.Model.ContextWindow) + " ctx"
		}
		p.Items = append(p.Items, tui.SelectItem{Label: r.Model.DisplayName(), Detail: detail, Value: r.ProviderName + "/" + r.Model.ID})
		if r.ProviderName == cur.ProviderName && r.Model.ID == cur.Model.ID {
			p.Selected = i
		}
	}
	p.OnCancel = a.closeModal
	p.OnSelect = func(it tui.SelectItem) {
		a.closeModal()
		if ref, ok := a.models.Find("", it.Value); ok {
			a.setModel(ref)
		}
	}
	a.openModal(p)
}

func (a *App) setModel(ref config.ModelRef) {
	a.agent.SetModel(ref)
	a.modelFrom = core.FromCommand
	err := config.UpdateSettings(map[string]any{
		"defaultProvider": ref.ProviderName,
		"defaultModel":    ref.Model.ID,
		"defaultEffort":   a.effort(),
	})
	if err != nil {
		a.errorNotice(err)
	}
	a.notice("Model set to %s (%s).", ref.Model.DisplayName(), ref.ProviderName)
	a.statusTrigger()
	a.remoteUpdated()
}

func (a *App) cmdEffort(arg string) {
	levels := a.efforts()
	if len(levels) == 0 {
		a.notice("%s has no effort levels.", a.model().Model.DisplayName())
		return
	}
	if arg != "" {
		for _, l := range levels {
			if l == arg {
				a.setEffort(l, true)
				return
			}
		}
		a.notice("Unknown effort %q. Levels: %s.", arg, strings.Join(levels, ", "))
		return
	}
	p := &tui.SelectList{Title: "Reasoning effort (enter to choose, esc to cancel)"}
	for i, l := range levels {
		p.Items = append(p.Items, tui.SelectItem{Label: effortStyle(l), Value: l})
		if l == a.effort() {
			p.Selected = i
		}
	}
	p.OnCancel = a.closeModal
	p.OnSelect = func(it tui.SelectItem) {
		a.closeModal()
		a.setEffort(it.Value, true)
	}
	a.openModal(p)
}

func (a *App) cmdCompact(string) {
	if a.busy {
		a.enqueue("/compact", nil)
		return
	}
	a.runKind = "compact"
	a.recordSettings()
	a.start("Compacting context", a.agent.Compact)
}

// reset clears the transcript and pending input (for /clear and /resume).
func (a *App) reset() {
	a.agent.Reset()
	a.agent.SetLongContext(false)
	a.queued, a.pendingSteers, a.queuePaused = nil, nil, false
	a.remoteSteers = nil
	a.ctxTokens = 0
	a.usage = usageStats{}
	a.ui.Body.Clear()
	a.resetItems()
	a.ui.Redraw()
	a.ui.ScrollToBottom()
	a.addHeader()
}

func (a *App) cmdClear(string) {
	if a.busy {
		a.enqueue("/clear", nil)
		return
	}
	a.reset()
	a.newSession("clear")
	a.sessionStartHook("clear")
	a.notice("Started a new conversation.")
}

func (a *App) cmdQuit(string) { a.requestQuit() }

// nameSession names the conversation (/name, and extensions).
func (a *App) nameSession(name string) {
	a.sessName = name
	a.editor.Title = a.sessName
	a.sess.Append(session.Entry{Type: session.TypeName, Name: name})
	a.statusTrigger()
	a.remoteUpdated()
}

func (a *App) cmdName(arg string) {
	if arg == "" {
		if a.sessName == "" {
			a.notice("This conversation has no name. Usage: /name <name>")
		} else {
			a.notice("This conversation is named %q.", a.sessName)
		}
		return
	}
	a.nameSession(arg)
	a.notice("Named this conversation %q.", arg)
	a.remoteUpdated()
}

func (a *App) cmdArchive(string) {
	if a.busy {
		a.notice("Still working — press esc to interrupt first.")
		return
	}
	path := a.sess.Path
	a.sess.Close()
	if _, err := os.Stat(path); err != nil {
		a.notice("Nothing to archive yet.")
		return
	}
	if _, err := session.Archive(path); err != nil {
		a.errorNotice(err)
		return
	}
	a.reset()
	a.newSession("other")
	a.notice("Archived the conversation. Find it with /resume (shift+tab shows archived).")
}
