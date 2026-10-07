package app

import (
	"encoding/json"
	"strings"

	"github.com/sebastianrcnt/atto/tui"
)

// Like codex, leaving atto while a turn is running (or a goal will go on)
// asks what to do: cancel the task, exit, or leave it running. In a daemon
// worker leaving is a detach: the session goes on as it is. Without the
// daemon the runtime lives in this process, so "Run in background" hands
// the session to a detached "atto _continue <id>" (thread/handoff,
// server/handoff.go) and exits; while that runs it holds the session's
// lock and atto opens the session read-only.
//
// settings.json "backgroundExit": false turns the menu off.

// bgExit is the state of the exit menu.
type bgExit struct {
	off     bool // settings.json backgroundExit: false
	pending bool // "Run in background" was picked
}

// exitRunning reports whether leaving now would stop work: a turn is
// running, or a goal is active and will start the next one.
func (a *App) exitRunning() bool {
	return (a.busy && a.runKind == "turn") || (a.goalActive() && !a.goalHeld())
}

// requestQuit is every way of exiting (ctrl+c, ctrl+d, /quit, /exit): the
// menu when work would be lost, else it quits.
func (a *App) requestQuit() {
	if a.bgx.off || a.bgx.pending || a.readOnly != "" || !a.exitRunning() {
		a.doQuit()
		return
	}
	a.exitMenu()
}

// exitMenuAvailable is whether ctrl+d may open the menu while a turn runs.
func (a *App) exitMenuAvailable() bool {
	return !a.bgx.off && a.readOnly == "" && a.exitRunning()
}

const (
	exitCancel = "cancel"
	exitBG     = "background"
	exitDetach = "detach"
	exitQuit   = "exit"
)

type exitMenuModal struct{ list *tui.SelectList }

func (m exitMenuModal) HandleInput(data string) {
	if i := strings.Index("123", data); len(data) == 1 && i >= 0 && i < len(m.list.Items) {
		m.list.Selected = i
		m.list.HandleInput("\r")
		return
	}
	m.list.HandleInput(data)
}

func (m exitMenuModal) Render(width int) []string {
	return append(m.list.Render(width), "", tui.Truncate(tui.Dim("  enter select · esc back"), width, "…"))
}

func (a *App) exitMenu() {
	bg := tui.SelectItem{Label: "2. Run in background", Detail: "Exit atto and leave the task running", Value: exitBG}
	if a.pane.on || a.conn.own == nil {
		// In the daemon the task goes on as it is: leave the terminal only.
		back := "atto attach"
		if !a.pane.on {
			back = "atto connect"
		}
		bg = tui.SelectItem{Label: "2. Detach", Detail: "Leave atto running; " + back + " to return", Value: exitDetach}
	}
	l := &tui.SelectList{Items: []tui.SelectItem{
		{Label: "1. Cancel task", Detail: "Stop the current task and stay in atto", Value: exitCancel},
		bg,
		{Label: "3. Exit", Detail: "Stop the current task and exit atto", Value: exitQuit},
	}}
	l.RenderRow = func(it tui.SelectItem, selected bool, width int) []string {
		label := it.Label + strings.Repeat(" ", max(0, 22-len(it.Label)))
		line := "  " + label
		if selected {
			line = tui.FG(6, "› "+label)
		}
		return []string{tui.Truncate(line+tui.Dim(it.Detail), width, "…")}
	}
	l.OnCancel = a.closeModal
	l.OnSelect = func(it tui.SelectItem) {
		switch it.Value {
		case exitCancel:
			a.cancelTask()
			a.closeModal()
		case exitBG:
			a.closeModal()
			a.runInBackground()
		case exitDetach:
			a.closeModal()
			if !a.detach() { // atto connect: leaving is detaching
				a.doQuit()
			}
		case exitQuit:
			a.closeModal()
			a.stopAndQuit()
		}
	}
	a.openModal(exitMenuModal{l})
}

// cancelTask stops the running turn (which pauses the goal, as Esc
// does); an idle goal is paused.
func (a *App) cancelTask() {
	if a.busy {
		a.rpcErr("turn/interrupt", map[string]any{"mode": "cancel"})
		return
	}
	if a.goalActive() {
		a.rpcErr("goal/pause", nil)
	}
}

// stopAndQuit is "Exit": the task stops with the session.
func (a *App) stopAndQuit() {
	if a.conn.own != nil {
		a.doQuit() // the runtime of this process closes the session
		return
	}
	a.rpc("thread/close", map[string]any{"reason": "exit"}, func(json.RawMessage, error) { a.doQuit() })
}

// runInBackground hands the session to a background run; atto exits once
// it has (thread/handedOff).
func (a *App) runInBackground() {
	a.bgx.pending = true
	if a.busy {
		a.notice("Stopping the current step to continue in the background…")
	}
	a.rpc("thread/handoff", nil, func(_ json.RawMessage, err error) {
		if err != nil {
			a.bgx.pending = false
			a.errorNotice(err)
		}
	})
}

// --- read-only sessions ---

// renderReadOnly is the banner above the input of a read-only session.
func (a *App) renderReadOnly(width int) []string {
	if a.readOnly == "" {
		return nil
	}
	return []string{tui.Truncate(tui.Dim("  "+a.readOnly+" · ctrl+r to refresh"), width, "…")}
}

// readOnlyAllowed are the commands that work on a read-only session.
var readOnlyAllowed = []string{"quit", "exit", "resume", "clear", "tui", "agents"}

// refuseReadOnly refuses text typed into a read-only session (restoring it
// to the editor), except the commands of readOnlyAllowed.
func (a *App) refuseReadOnly(text string) bool {
	why := a.readOnly
	if why == "" || strings.TrimSpace(text) == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(text, "/"); ok {
		name, _, _ := strings.Cut(rest, " ")
		for _, c := range readOnlyAllowed {
			if name != "" && strings.HasPrefix(c, name) {
				return false
			}
		}
	}
	a.restoreToEditor([]string{text})
	a.notice("%s", why)
	return true
}

// readOnlyKey handles the keys of a read-only session: ctrl+r refreshes
// (and opens the session normally once the run has finished), tab (which
// would queue a message) does nothing.
func (a *App) readOnlyKey(data string) bool {
	if a.readOnly == "" {
		return false
	}
	switch tui.Key(data) {
	case "ctrl+r":
		a.resumeID(a.threadID)
		return true
	case "tab":
		return true
	}
	return false
}
