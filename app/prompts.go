package app

import (
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
)

// The runtime's prompts (an extension's question, an MCP approval, the
// goal's confirmations) show in place of the editor, as the terminal's own
// dialogs did. Any client may answer; the first answer wins, and the
// dialog closes here when another client answered (prompt/closed). A
// prompt that arrives while a picker of this terminal is open waits for
// it to close.

// shownPrompt is the runtime's prompt this terminal shows.
type shownPrompt struct {
	id string
	m  modal
}

// promptOpened shows the runtime's prompt p.
func (a *App) promptOpened(p server.Prompt) {
	if a.conn != nil && p.ClientID == a.conn.id {
		return
	}
	if a.prompt != nil && a.prompt.id == p.ID {
		return
	}
	if a.modal != nil {
		a.promptWaiting = &p
		return
	}
	a.showPrompt(p)
}

// showWaitingPrompt shows the prompt that waited for a picker to close.
func (a *App) showWaitingPrompt() {
	if p := a.promptWaiting; p != nil && a.modal == nil {
		a.promptWaiting = nil
		a.showPrompt(*p)
	}
}

// promptClosed closes prompt id: it was answered (here or elsewhere) or
// withdrawn.
func (a *App) promptClosed(id string) {
	if w := a.promptWaiting; w != nil && w.ID == id {
		a.promptWaiting = nil
	}
	if p := a.prompt; p != nil && p.id == id {
		a.prompt = nil
		if a.modal == p.m {
			a.ui.Screen = nil
			a.modal = nil
			a.ui.SetFocus(a.editor)
		}
	}
}

func (a *App) showPrompt(p server.Prompt) {
	answer := func(params map[string]any) {
		params["id"] = p.ID
		a.promptClosed(p.ID)
		a.rpc("prompt/answer", params, nil) // a late answer is refused: another client was first
	}
	var m modal
	switch {
	case p.Kind == server.PromptInput:
		title := tui.StripControls(p.Title)
		if p.Subtitle != "" {
			title += tui.Dim("  " + tui.StripControls(p.Subtitle))
		}
		in := &labelInput{title: title, hint: "enter submit  esc cancel", text: p.Text}
		in.onDone = func(ok bool, text string) {
			if !ok {
				answer(map[string]any{"cancel": true})
				return
			}
			answer(map[string]any{"text": text})
		}
		m = labelModal{in}
	default:
		l := &tui.SelectList{}
		for i, o := range p.Options {
			l.Items = append(l.Items, tui.SelectItem{Label: o.Label, Detail: o.Description, Value: o.Label, Data: i})
		}
		l.Selected = p.Selected
		l.OnSelect = func(it tui.SelectItem) { answer(map[string]any{"index": it.Data.(int)}) }
		l.OnCancel = func() { answer(map[string]any{"cancel": true}) }
		if p.Origin == "goal" {
			m = goalPrompt{title: p.Title, subtitle: p.Subtitle, list: l}
			break
		}
		l.Title = tui.StripControls(p.Title)
		if p.Subtitle != "" {
			l.Title += tui.Dim("  " + tui.StripControls(p.Subtitle))
		}
		m = l
	}
	a.prompt = &shownPrompt{id: p.ID, m: m}
	a.ui.Screen = nil
	a.modal = m
	a.ui.SetFocus(m)
}
