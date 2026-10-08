package app

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// Going back, after pi. Sessions are trees (see package session): /tree
// shows every entry, and picking one moves the active leaf there, so the
// conversation continues from that point while the old branch stays in
// the file. Picking a user message goes back to just before it and puts
// its text in the editor to edit and resend. /fork copies the path up to a
// user message into a new session instead. Esc twice on an empty prompt
// opens one of them (settings.json "doubleEscapeAction": tree, fork, none).
// The tree is read through thread/tree; the runtime moves
// (thread/navigate) and every client follows (thread/branchChanged).

// doubleEscWindow is pi's: the second Esc must follow within 500ms.
const doubleEscWindow = 500 * time.Millisecond

// doubleEsc detects Esc pressed twice in quick succession.
type doubleEsc struct {
	last time.Time
	now  func() time.Time // for tests
}

// press records an Esc and reports whether it completes a double Esc.
func (d *doubleEsc) press() bool {
	now := time.Now
	if d.now != nil {
		now = d.now
	}
	t := now()
	if !d.last.IsZero() && t.Sub(d.last) < doubleEscWindow {
		d.last = time.Time{}
		return true
	}
	d.last = t
	return false
}

// reset forgets a pending first Esc (another key came in between).
func (d *doubleEsc) reset() { d.last = time.Time{} }

// onEscape handles Esc while idle. Only an empty prompt arms the double
// Esc; the interrupt Esc during a turn never does (codex and pi agree), so
// pressing Esc twice to stop a turn cannot open a picker.
func (a *App) onEscape() bool {
	if strings.TrimSpace(a.editor.Text()) != "" || a.escAction == "none" {
		a.esc.reset()
		return false
	}
	if !a.esc.press() {
		return true
	}
	if a.escAction == "fork" {
		a.cmdFork("")
	} else {
		a.cmdTree("")
	}
	return true
}

// readTree fetches the runtime's tree without opening its session writer.
func (a *App) readTree(done func([]session.Entry, string)) {
	id := a.threadID
	a.rpc("thread/tree", map[string]any{"offline": a.info.Offline}, func(raw json.RawMessage, err error) {
		if id != a.threadID {
			return
		}
		if err != nil {
			a.errorNotice(err)
			return
		}
		var r struct {
			Entries []session.Entry `json:"entries"`
			Leaf    string          `json:"leaf"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return
		}
		a.treeEntries, a.treeLeaf = r.Entries, r.Leaf
		if done != nil {
			done(r.Entries, r.Leaf)
		}
	})
}

// loadSession returns the latest tree snapshot, never a writer or disk read.
func (a *App) loadSession() []session.Entry { return a.treeEntries }

// pickerRows sizes the tree like pi: half the terminal, at least 5 rows.
func pickerRows() int {
	_, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || h <= 0 {
		h = 24
	}
	return max(5, h/2)
}

// cmdTree opens the session tree. It works mid-turn: picking an entry
// interrupts the turn and moves once it has stopped, as pi does.
func (a *App) cmdTree(string) {
	a.readTree(func(entries []session.Entry, leaf string) {
		if len(entries) == 0 {
			a.notice("No entries in session.")
			return
		}
		p := newTreePicker(session.Tree(entries), leaf, pickerRows())
		p.onCancel = a.closeModal
		p.onSelect = func(id string) {
			a.closeModal()
			a.selectTreeEntry(id)
		}
		p.onLabel = func(id, label string) {
			a.rpcErr("thread/setLabel", map[string]any{"entryId": id, "label": label})
		}
		p.onCopy = func(text string) {
			if strings.TrimSpace(text) == "" {
				a.notice("Selected entry has no text to copy.")
				return
			}
			a.copyText(text, func(note string) { a.showToast("Selected entry: " + note) })
		}
		a.openModal(p)
	})
}

// navigateTree moves the active leaf to entry id: before it for a user
// message (whose text comes back to the editor), onto it otherwise.
func (a *App) navigateTree(id string) { a.moveTo(id, nil) }

// moveTo is navigateTree, first summarizing the branch being left when
// sum is set (see branchsummary.go). A running turn is interrupted first;
// its pending input comes back to the editor.
func (a *App) moveTo(id string, sum *summaryRequest) {
	params := map[string]any{"entryId": id}
	if sum != nil {
		params["summary"] = map[string]any{"mode": "auto", "instructions": sum.instructions}
		a.summaryAsked = true
	}
	a.rpcErr("thread/navigate", params)
}

// cmdFork lists the session's user messages (on every branch, as pi does)
// and starts a new session from just before the chosen one.
func (a *App) cmdFork(string) {
	a.readTree(func(entries []session.Entry, _ string) {
		sel := &tui.SelectList{Title: "Fork from a message (enter to choose, esc to cancel)", Filterable: true, MaxVisible: 10}
		for _, e := range entries {
			m := e.Message
			if e.Type != session.TypeMessage || m == nil || m.Role != "user" || strings.TrimSpace(m.Content) == "" || events.IsEvent(m.Content) {
				continue
			}
			sel.Items = append(sel.Items, tui.SelectItem{Label: oneLine(m.Content), Value: e.ID})
		}
		if len(sel.Items) == 0 {
			a.notice("No messages to fork from.")
			return
		}
		sel.Selected = len(sel.Items) - 1
		sel.OnCancel = a.closeModal
		sel.OnSelect = func(it tui.SelectItem) {
			a.closeModal()
			if a.busy {
				a.notice("Still working — press esc to interrupt first.")
				return
			}
			a.fork(it.Value)
		}
		a.openModal(sel)
	})
}

// fork switches to a new session holding the path to just before user
// message id, with its text in the editor.
func (a *App) fork(id string) {
	a.rpc("thread/fork", map[string]any{"entryId": id}, func(raw json.RawMessage, err error) {
		if err != nil {
			a.errorNotice(err)
			return
		}
		var r struct {
			Path   string             `json:"path"`
			Input  string             `json:"input"`
			Images []server.ItemImage `json:"images"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return
		}
		with := func() {
			if a.editorEmpty() {
				a.editor.SetText(r.Input, attachments(r.Images)...)
			}
			a.notice("Forked to a new session.")
		}
		if _, err := os.Stat(r.Path); err != nil { // forked before the first message: nothing to copy
			a.newSession("other", with)
			return
		}
		s, err := session.Summarize(r.Path)
		if err != nil {
			a.errorNotice(err)
			return
		}
		a.rpc("thread/resume", map[string]any{"threadId": s.ID, "cwd": a.cwd}, func(raw json.RawMessage, err error) {
			if err != nil {
				a.errorNotice(err)
				return
			}
			var info server.ThreadInfo
			if json.Unmarshal(raw, &info) == nil {
				a.switchTo(info, "resume", with)
			}
		})
	})
}
