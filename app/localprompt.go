package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

const maxPromptOptions = 200

// localPrompt describes a picker this client implements. The runtime owns
// its lifetime and answer arbitration; callbacks run only after it accepts.
type localPrompt struct {
	wire   server.Prompt
	choose func(int)
	submit func(string)
	cancel func()
	keys   []any
	modal  modal
}

func (a *App) advertiseModal(m modal) {
	if p := a.localPrompt; p != nil && p.modal == m {
		return
	}
	if a.remote == nil || a.conn == nil || a.conn.c == nil {
		return
	}
	p := a.localPromptOf(m)
	if p == nil {
		return
	}
	a.localSeq++
	p.wire.RequestID = fmt.Sprintf("ui-%d", a.localSeq)
	p.modal = m
	a.localPrompt = p
	a.rpc("prompt/clientOpen", map[string]any{"prompt": p.wire}, func(raw json.RawMessage, err error) {
		if err != nil {
			return
		}
		var wire server.Prompt
		if json.Unmarshal(raw, &wire) == nil {
			p.wire = wire
		}
	})
}

func (a *App) withdrawModal() {
	if p := a.localPrompt; p != nil {
		a.localPrompt = nil
		a.rpc("prompt/clientClose", map[string]any{"id": p.wire.RequestID}, nil)
	}
}

func (a *App) answerLocal(p *localPrompt, params map[string]any) {
	// Answer only once the earlier open request has returned. Both calls
	// are on the same ordered connection, never waited for by the UI.
	a.rpc("ping", nil, func(json.RawMessage, error) {
		if a.localPrompt != p || p.wire.ID == "" {
			return
		}
		params["id"] = p.wire.ID
		a.rpc("prompt/answer", params, nil)
	})
}

func localIndex(p *localPrompt, key any) int {
	for i, x := range p.keys {
		switch k := key.(type) {
		case tui.SelectItem:
			if it, ok := x.(tui.SelectItem); ok && it.Label == k.Label && it.Value == k.Value {
				return i
			}
		case session.Summary:
			if it, ok := x.(session.Summary); ok && it.ID == k.ID {
				return i
			}
		case string:
			if x == k {
				return i
			}
		}
	}
	return -1
}

func (a *App) localAnswered(client, request string, ans server.PromptAnswer) {
	p := a.localPrompt
	if p == nil || a.conn == nil || client != a.conn.id || request != p.wire.RequestID {
		return
	}
	a.localPrompt = nil
	if a.modal != p.modal {
		return
	}
	switch {
	case ans.Cancel:
		if p.cancel != nil {
			p.cancel()
		}
	case ans.Index != nil:
		if p.choose != nil {
			p.choose(*ans.Index)
		}
	case ans.Text != nil:
		if p.submit != nil {
			p.submit(*ans.Text)
		}
	}
}

func plainText(s string) string { return strings.TrimSpace(oneLine(tui.StripEscapes(s))) }

// withoutKeys drops a title's key hint, "(enter to choose, esc to
// cancel)": the phone has none of those keys.
func withoutKeys(title string) string {
	if i := strings.LastIndex(title, " ("); i >= 0 && strings.HasSuffix(title, ")") && strings.Contains(title[i:], "esc") {
		return title[:i]
	}
	return title
}

// promptOf describes m as a prompt, or nil when it is not one.
func (a *App) localPromptOf(m modal) *localPrompt {
	switch m := m.(type) {
	case *tui.SelectList:
		return a.selectPrompt(m, withoutKeys(plainText(m.Title)), "")
	case projectTrustPrompt:
		return a.selectPrompt(m.list, "This project wants to run code on your machine", m.detail())
	case goalPrompt:
		return a.selectPrompt(m.list, m.title, m.subtitle)
	case exitMenuModal:
		return a.selectPrompt(m.list, "A task is still running", "What should happen to it?")
	case labelModal:
		return a.inputPrompt(m.labelInput)
	case *resumePicker:
		return a.resumePrompt(m)
	case *treePicker:
		return a.treePrompt(m)
	}
	return nil
}

func (a *App) selectPrompt(l *tui.SelectList, title, subtitle string) *localPrompt {
	if l.Source != nil {
		return nil
	}
	p := &localPrompt{wire: server.Prompt{Kind: server.PromptSelect, Title: title, Subtitle: subtitle, Filterable: l.Filterable}}
	items := l.Items
	if len(items) > maxPromptOptions {
		p.wire.Total, p.wire.Note = len(items), fmt.Sprintf("Showing the first %d of %d; search in the terminal for the others.", maxPromptOptions, len(items))
		items = items[:maxPromptOptions]
	}
	for _, it := range items {
		p.wire.Options = append(p.wire.Options, server.PromptOption{Label: plainText(it.Label), Description: plainText(it.Detail)})
		p.keys = append(p.keys, it)
	}
	if l.Query() == "" && l.Selected < len(items) {
		p.wire.Selected = l.Selected
	}
	onSelect, onCancel := l.OnSelect, l.OnCancel
	if onSelect != nil {
		l.OnSelect = func(it tui.SelectItem) { a.answerLocal(p, map[string]any{"index": localIndex(p, it)}) }
	}
	if onCancel != nil {
		l.OnCancel = func() { a.answerLocal(p, map[string]any{"cancel": true}) }
	}
	p.choose = func(i int) {
		if onSelect != nil {
			onSelect(p.keys[i].(tui.SelectItem))
		}
	}
	p.cancel = func() {
		if onCancel != nil {
			onCancel()
		}
	}
	return p
}

func (a *App) inputPrompt(in *labelInput) *localPrompt {
	title := plainText(in.title)
	if title == "" {
		title = "Label (empty to remove)"
	}
	p := &localPrompt{wire: server.Prompt{Kind: server.PromptInput, Title: strings.TrimSuffix(title, ":"), Text: in.text, Placeholder: in.placeholder}}
	onDone := in.onDone
	in.onDone = func(ok bool, text string) {
		if ok {
			a.answerLocal(p, map[string]any{"text": text})
		} else {
			a.answerLocal(p, map[string]any{"cancel": true})
		}
	}
	p.submit = func(text string) { onDone(true, oneLine(text)) }
	p.cancel = func() { onDone(false, "") }
	return p
}

func (a *App) resumePrompt(r *resumePicker) *localPrompt {
	p := &localPrompt{wire: server.Prompt{Kind: server.PromptSelect, Title: "Resume a session", Filterable: true}}
	if r.all {
		p.wire.Subtitle = "All projects"
	} else {
		p.wire.Subtitle = "Sessions in " + core.ShortPath(r.cwd)
	}
	items := r.list.Items
	if len(items) > maxPromptOptions {
		p.wire.Total, p.wire.Note = len(items), fmt.Sprintf("Showing the newest %d of %d sessions.", maxPromptOptions, len(items))
		items = items[:maxPromptOptions]
	}
	for _, it := range items {
		s := it.Data.(session.Summary)
		p.wire.Options = append(p.wire.Options, server.PromptOption{Label: rowTitle(s), Description: r.rowMeta(s)})
		p.keys = append(p.keys, s)
	}
	if r.list.Query() == "" && r.list.Selected < len(items) {
		p.wire.Selected = r.list.Selected
	}
	onPick, onCancel := r.onPick, r.onCancel
	r.onPick = func(s session.Summary) { a.answerLocal(p, map[string]any{"index": localIndex(p, s)}) }
	r.onCancel = func() { a.answerLocal(p, map[string]any{"cancel": true}) }
	p.choose = func(i int) { onPick(p.keys[i].(session.Summary)) }
	p.cancel = func() { onCancel() }
	return p
}

func (a *App) treePrompt(t *treePicker) *localPrompt {
	p := &localPrompt{wire: server.Prompt{Kind: server.PromptSelect, Title: "Session tree", Subtitle: "Go back to an entry; the conversation continues from there.", Filterable: true}}
	nodes := t.visible
	start := 0
	if len(nodes) > maxPromptOptions {
		start = max(0, min(t.selected-maxPromptOptions/2, len(nodes)-maxPromptOptions))
		p.wire.Total, p.wire.Note = len(nodes), fmt.Sprintf("Showing %d of %d entries around the current one.", maxPromptOptions, len(nodes))
		nodes = nodes[start : start+maxPromptOptions]
	}
	for _, f := range nodes {
		label := plainText(t.entryText(f.n))
		if l := f.n.Label; l != "" {
			label = "[" + l + "] " + label
		}
		desc := ""
		switch id := f.n.Entry.ID; {
		case id == t.leaf:
			desc = "current position"
		case t.active[id]:
			desc = "on the current branch"
		}
		p.wire.Options = append(p.wire.Options, server.PromptOption{Label: label, Description: desc})
		p.keys = append(p.keys, f.n.Entry.ID)
	}
	p.wire.Selected = max(0, min(t.selected-start, len(nodes)-1))
	onSelect, onCancel := t.onSelect, t.onCancel
	t.onSelect = func(id string) { a.answerLocal(p, map[string]any{"index": localIndex(p, id)}) }
	t.onCancel = func() { a.answerLocal(p, map[string]any{"cancel": true}) }
	p.choose = func(i int) { onSelect(p.keys[i].(string)) }
	p.cancel = func() { onCancel() }
	return p
}
