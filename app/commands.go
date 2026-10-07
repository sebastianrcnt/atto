package app

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// Slash commands: the catalog is the runtime's (commands/list: built-in
// commands, the extensions' and one /skill:<name> per skill). The terminal
// runs its own (pickers, the renderer, quitting, sessions) and the
// display of a few others (/goal, /context, /jobs); everything else goes to
// the runtime as typed, which also queues it behind a running turn.

type command struct {
	name string
	args string
	desc string
}

// local are the terminal's commands, and what of the others it shows
// itself (local returns false: the runtime runs it after all).
var local map[string]func(a *App, arg string) bool

func init() { local = localCommands() }

func localCommands() map[string]func(a *App, arg string) bool {
	return map[string]func(a *App, arg string) bool{
		"model":      func(a *App, arg string) bool { return a.cmdModel(arg) },
		"effort":     func(a *App, arg string) bool { return a.cmdEffort(arg) },
		"copy":       func(a *App, arg string) bool { a.cmdCopy(arg); return true },
		"context":    func(a *App, arg string) bool { return a.cmdContext(arg) },
		"extensions": func(a *App, arg string) bool { return a.cmdExtensions(arg) },
		"request":    func(a *App, arg string) bool { a.cmdRequest(arg); return true },
		"debug":      func(a *App, arg string) bool { a.cmdDebug(arg); return true },
		"login":      func(a *App, arg string) bool { a.cmdLogin(arg); return true },
		"logout":     func(a *App, arg string) bool { a.cmdLogout(arg); return true },
		"resume":     func(a *App, arg string) bool { a.cmdResume(arg); return true },
		"sessions":   func(a *App, arg string) bool { a.cmdSessions(arg); return true },
		"tree":       func(a *App, arg string) bool { a.cmdTree(arg); return true },
		"fork":       func(a *App, arg string) bool { a.cmdFork(arg); return true },
		"name":       func(a *App, arg string) bool { return a.cmdName(arg) },
		"rename":     func(a *App, arg string) bool { return a.cmdName(arg) },
		"archive":    func(a *App, arg string) bool { a.cmdArchive(arg); return true },
		"goal":       func(a *App, arg string) bool { a.cmdGoal(arg); return true },
		"jobs":       func(a *App, arg string) bool { a.cmdJobs(arg); return true },
		"timers":     func(a *App, arg string) bool { a.cmdTimers(arg); return true },
		"tui":        func(a *App, arg string) bool { a.cmdTui(arg); return true },
		"remote":     func(a *App, arg string) bool { a.cmdRemote(arg); return true },
		"clear":      func(a *App, arg string) bool { a.cmdClear(arg); return true },
		"agents":     func(a *App, arg string) bool { a.cmdAgents(arg); return true },
		"detach":     func(a *App, arg string) bool { a.cmdDetach(arg); return true },
		"close":      func(a *App, arg string) bool { a.stopAndQuit(); return true },
		"quit":       func(a *App, arg string) bool { a.cmdQuit(arg); return true },
		"exit":       func(a *App, arg string) bool { a.cmdQuit(arg); return true },
	}
}

// runLocal runs text when it is one of the terminal's commands; false
// sends it to the runtime.
func (a *App) runLocal(text string) bool {
	if strings.HasPrefix(text, "/skill:") {
		return false
	}
	c, arg, err := server.ResolveCommand(a.catalogOrBuiltins(), text)
	if err != nil {
		a.notice("%s", err.Error())
		return true
	}
	run := local[c.Name]
	if run == nil || c.Origin != "builtin" {
		return false
	}
	return run(a, arg)
}

func (a *App) catalogOrBuiltins() []server.CommandInfo {
	if len(a.catalog) > 0 {
		return a.catalog
	}
	return server.Builtins
}

// loadCatalog reads the session's commands again.
func (a *App) loadCatalog() {
	a.rpc("commands/list", nil, func(raw json.RawMessage, err error) {
		var r struct {
			Commands []server.CommandInfo `json:"commands"`
		}
		if err == nil && json.Unmarshal(raw, &r) == nil {
			a.catalog = r.Commands
			a.cmds.gen++
		}
	})
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

// cmdCache is allCommands' result: gen counts catalog changes, built is
// the gen all was made at (-1: never).
type cmdCache struct {
	gen, built int
	all        []command
}

// allCommands is the catalog as the list shows it. It is called every
// frame, so the list is kept until the catalog changes.
func (a *App) allCommands() []command {
	c := &a.cmds
	if c.all != nil && c.built == c.gen {
		return c.all
	}
	var out []command
	for _, x := range a.catalogOrBuiltins() {
		out = append(out, command{x.Name, x.Args, x.Desc})
	}
	c.all, c.built = out, c.gen
	return out
}

// openModal shows m in place of the editor. While a picker of this
// terminal is open, the runtime holds automatic work (events, queued
// input, goal turns), as the terminal always did (client/gate).
func (a *App) openModal(m modal) {
	if a.modal == nil {
		a.rpc("client/gate", map[string]any{"open": true}, nil)
	}
	a.ui.Screen = nil
	a.modal = m
	a.ui.SetFocus(m)
}

func (a *App) closeModal() {
	if a.modal == nil {
		return
	}
	a.ui.Screen = nil
	a.modal = nil
	a.ui.SetFocus(a.editor)
	a.rpc("client/gate", map[string]any{"open": false}, nil)
	a.showWaitingPrompt()
}

func (a *App) cmdModel(arg string) bool {
	if arg != "" {
		ref, ok := a.models.Find("", arg)
		if !ok {
			a.notice("Unknown model %q.", arg)
			return true
		}
		a.setModel(ref)
		return true
	}
	a.modelPicker("")
	return true
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

// setModel makes ref the session's model and the default, as the terminal
// always did.
func (a *App) setModel(ref config.ModelRef) {
	a.info.Model = ref.ProviderName + "/" + ref.Model.ID
	a.rpcErr("thread/setModel", map[string]any{"model": a.info.Model, "saveDefault": true})
	a.notice("Model set to %s (%s).", ref.Model.DisplayName(), ref.ProviderName)
	a.statusTrigger()
}

func (a *App) cmdEffort(arg string) bool {
	levels := a.efforts()
	if len(levels) == 0 {
		a.notice("%s has no effort levels.", a.model().Model.DisplayName())
		return true
	}
	if arg != "" {
		if slices.Contains(levels, arg) {
			a.setEffort(arg, true)
			return true
		}
		a.notice("Unknown effort %q. Levels: %s.", arg, strings.Join(levels, ", "))
		return true
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
	return true
}

// cmdClear starts a new conversation. The one left goes on until it is
// idle (a turn running finishes), then ends.
func (a *App) cmdClear(string) {
	a.newSession("clear", func() { a.notice("Started a new conversation.") })
}

func (a *App) cmdQuit(string) { a.requestQuit() }

// cmdName shows the name; with one, the runtime names the conversation.
func (a *App) cmdName(arg string) bool {
	if arg != "" {
		return false
	}
	if a.sessName == "" {
		a.notice("This conversation has no name. Usage: /name <name>")
	} else {
		a.notice("This conversation is named %q.", a.sessName)
	}
	return true
}

// cmdArchive archives this conversation and starts a new one.
func (a *App) cmdArchive(string) {
	if a.busy {
		a.notice("Still working — press esc to interrupt first.")
		return
	}
	path, id := a.sessPath, a.threadID
	if _, err := os.Stat(path); err != nil {
		a.notice("Nothing to archive yet.")
		return
	}
	a.newSession("other", func() {
		// The old session is closed now (it was idle): its file can move.
		a.rpc("thread/close", map[string]any{"threadId": id, "reason": "other"}, func(json.RawMessage, error) {
			if _, err := session.Archive(path); err != nil {
				a.errorNotice(err)
				return
			}
			a.notice("Archived the conversation. Find it with /resume (shift+tab shows archived).")
		})
	})
}

// cmdExtensions lists the extensions; approving one is the runtime's.
func (a *App) cmdExtensions(arg string) bool {
	if strings.TrimSpace(arg) != "" {
		return false
	}
	var rows []core.Row
	for _, s := range a.loaded.Details() {
		if s.Title == "Extensions" {
			rows = s.Rows
		}
	}
	lines := []string{"Extensions (guide: atto extensions docs; types: atto extensions types):"}
	for _, r := range rows {
		lines = append(lines, "  "+r.Label+"  "+r.Text)
	}
	a.notice("%s", strings.Join(lines, "\n"))
	return true
}

var _ = fmt.Sprint
