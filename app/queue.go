package app

import (
	"encoding/json"
	"strings"

	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
)

// Pending input follows codex-rs; the runtime keeps it (server/input.go):
//
//   - Enter while a turn runs sends a steer. It is delivered into the running
//     turn after the next tool call (or when the model stops, which continues
//     the turn). Esc interrupts and sends pending steers immediately.
//   - Tab queues a follow-up that starts as a new turn when the current one
//     ends. Shift+Left pulls the last steer not yet delivered, else the last
//     queued message, back into the editor.
//
// The terminal shows what is pending (turn/pending) and sends the keys'
// intents.

const previewLineLimit = 3

// queueFromEditor handles Tab: queue the draft, or submit it when idle
// (the runtime decides; the terminal's own commands run at once).
func (a *App) queueFromEditor() {
	text, att := a.editor.Commit()
	if text == "" {
		return
	}
	if a.refuseReadOnly(text) {
		return
	}
	if strings.HasPrefix(text, "/") && a.runLocal(text) {
		return
	}
	a.send(text, att, "queue")
}

// sendNowFromEditor handles Ctrl+Enter: while a turn runs, the draft
// interrupts it and goes out as a new turn, after the steers the turn has
// not taken. Otherwise it is Enter.
func (a *App) sendNowFromEditor(text string, att []tui.Attachment) {
	if !a.busy || a.runKind != "turn" || strings.HasPrefix(text, "/") {
		a.submit(text, att)
		return
	}
	if a.refuseReadOnly(text) {
		return
	}
	a.ui.ScrollToBottom()
	a.send(text, att, "replace")
}

// takeBackLast is Shift+Left: the last steer the turn has not taken, else
// the last queued message, back into the editor in front of the draft.
func (a *App) takeBackLast() bool {
	if len(a.pending.Items) == 0 && len(a.pending.Steers) == 0 && len(a.pending.Queued) == 0 {
		return false
	}
	a.rpc("turn/unsteer", nil, func(raw json.RawMessage, err error) {
		if err != nil {
			return // the turn took it meanwhile
		}
		var r struct {
			Text   string             `json:"text"`
			Images []server.ItemImage `json:"images"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return
		}
		text := r.Text
		if cur := a.editor.Text(); strings.TrimSpace(cur) != "" {
			text += "\n" + cur
		}
		a.editor.SetText(text, attachments(r.Images)...)
	})
	return true
}

// restoreToEditor puts texts back in front of the current draft; att are
// image attachments whose labels are in texts.
func (a *App) restoreToEditor(texts []string, att ...tui.Attachment) {
	text := strings.Join(texts, "\n\n")
	if cur := a.editor.Text(); strings.TrimSpace(cur) != "" {
		text += "\n\n" + cur
	}
	a.editor.SetText(text, att...)
}

func previewLines(text string, width int, style func(string) string) []string {
	var out []string
	wrapped := tui.Wrap(text, max(1, width-4))
	for i, l := range wrapped {
		if i == previewLineLimit {
			out = append(out, "    "+tui.Dim(style("…")))
			break
		}
		prefix := "    "
		if i == 0 {
			prefix = tui.Dim("  ↳ ")
		}
		out = append(out, prefix+tui.Dim(style(l)))
	}
	return out
}

func (a *App) renderPending(width int) []string {
	steers, queued := a.pending.Steers, a.pending.Queued
	if len(steers) == 0 && len(queued) == 0 {
		return nil
	}
	plain := func(s string) string { return s }
	out := []string{""}
	if len(steers) > 0 {
		out = append(out, tui.Truncate(tui.Dim("• ")+"Messages to be submitted after next tool call"+
			tui.Dim(" (press esc to interrupt and send immediately)"), width, "…"))
		for _, s := range steers {
			out = append(out, previewLines(s, width, plain)...)
		}
		out = append(out, "    "+tui.FG(6, "shift+←")+tui.Dim(" edit last message"))
	}
	if len(queued) > 0 {
		if len(steers) > 0 {
			out = append(out, "")
		}
		header := "Queued follow-up inputs"
		if a.pending.Paused {
			header += tui.Dim(" (paused — enter on empty prompt to resume)")
		}
		out = append(out, tui.Truncate(tui.Dim("• ")+header, width, "…"))
		for _, q := range queued {
			out = append(out, previewLines(q, width, tui.Italic)...)
		}
		out = append(out, "    "+tui.FG(6, "shift+←")+tui.Dim(" edit last queued message"))
	}
	return out
}
