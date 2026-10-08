package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// Prompts mirrored to /remote. Every picker and one-line input opens
// through openModal; promptOf describes the ones a phone can answer
// (select lists, the goal's confirmations, /resume, /tree, the exit menu,
// line inputs) and how to answer them. While such a modal is open it is
// a.prompt: clients get prompt/open, thread/read has it, and
// prompt/answer applies an answer as if it had been given in the terminal.
// When the modal closes, whichever side answered, clients get
// prompt/closed. Login dialogs are not mirrored: API keys and redirect
// URLs stay in the terminal.

// maxPromptOptions caps the options sent for one prompt.
const maxPromptOptions = 200

// openPrompt is the open modal as a prompt.
type openPrompt struct {
	wire   server.Prompt
	choose func(i int)  // select: picks wire.Options[i]
	submit func(string) // input
	cancel func()       // Esc
	keys   []any        // per option, for choose (items, sessions, tree IDs)
}

// promptOpened makes m the open prompt, when it is one, and tells clients.
func (a *App) promptOpened(m modal) {
	p := a.promptOf(m)
	if p == nil {
		return
	}
	a.promptSeq++
	p.wire.ID = fmt.Sprintf("p%d", a.promptSeq)
	a.prompt = p
	a.remotePublish("prompt/open", map[string]any{"prompt": p.wire})
}

// promptGone tells clients the open prompt closed: how it was answered,
// if a callback said so, else "closed" (replaced or dismissed).
func (a *App) promptGone() {
	p := a.prompt
	if p == nil {
		return
	}
	a.prompt = nil
	how, by := a.promptHow, "terminal"
	if how == "" {
		how = "closed"
	}
	if a.promptByRemote {
		by = "remote"
	}
	a.remotePublish("prompt/closed", map[string]any{"id": p.wire.ID, "how": how, "by": by})
}

// answered wraps a modal's callback so that, called from either side, it
// records how the prompt was closed.
func (a *App) answered(how string, fn func()) {
	prev := a.promptHow
	a.promptHow = how
	fn()
	a.promptHow = prev
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
func (a *App) promptOf(m modal) *openPrompt {
	switch m := m.(type) {
	case *tui.SelectList:
		return a.selectPrompt(m, withoutKeys(plainText(m.Title)), "")
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

func (a *App) selectPrompt(l *tui.SelectList, title, subtitle string) *openPrompt {
	if l.Source != nil {
		return nil
	}
	p := &openPrompt{wire: server.Prompt{Kind: server.PromptSelect, Title: title, Subtitle: subtitle, Filterable: l.Filterable}}
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
		l.OnSelect = func(it tui.SelectItem) { a.answered("answered", func() { onSelect(it) }) }
	}
	if onCancel != nil {
		l.OnCancel = func() { a.answered("cancelled", onCancel) }
	}
	p.choose = func(i int) {
		if l.OnSelect != nil {
			l.OnSelect(p.keys[i].(tui.SelectItem))
		}
	}
	p.cancel = func() {
		if l.OnCancel != nil {
			l.OnCancel()
		}
	}
	return p
}

func (a *App) inputPrompt(in *labelInput) *openPrompt {
	title := plainText(in.title)
	if title == "" {
		title = "Label (empty to remove)"
	}
	p := &openPrompt{wire: server.Prompt{Kind: server.PromptInput, Title: strings.TrimSuffix(title, ":"), Text: in.text, Placeholder: in.placeholder}}
	onDone := in.onDone
	in.onDone = func(ok bool, text string) {
		how := "answered"
		if !ok {
			how = "cancelled"
		}
		a.answered(how, func() { onDone(ok, text) })
	}
	p.submit = func(text string) { in.onDone(true, oneLine(text)) }
	p.cancel = func() { in.onDone(false, "") }
	return p
}

func (a *App) resumePrompt(r *resumePicker) *openPrompt {
	p := &openPrompt{wire: server.Prompt{Kind: server.PromptSelect, Title: "Resume a session", Filterable: true}}
	if r.all {
		p.wire.Subtitle = "All projects"
	} else {
		p.wire.Subtitle = "Sessions in " + shortPath(r.cwd)
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
	r.onPick = func(s session.Summary) { a.answered("answered", func() { onPick(s) }) }
	r.onCancel = func() { a.answered("cancelled", onCancel) }
	p.choose = func(i int) { r.onPick(p.keys[i].(session.Summary)) }
	p.cancel = func() { r.onCancel() }
	return p
}

func (a *App) treePrompt(t *treePicker) *openPrompt {
	p := &openPrompt{wire: server.Prompt{Kind: server.PromptSelect, Title: "Session tree", Subtitle: "Go back to an entry; the conversation continues from there.", Filterable: true}}
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
	t.onSelect = func(id string) { a.answered("answered", func() { onSelect(id) }) }
	t.onCancel = func() { a.answered("cancelled", onCancel) }
	p.choose = func(i int) { t.onSelect(p.keys[i].(string)) }
	p.cancel = func() { t.onCancel() }
	return p
}

// answerPrompt applies a client's answer to prompt id.
func (a *App) answerPrompt(id string, ans server.PromptAnswer) error {
	p := a.prompt
	if p == nil || p.wire.ID != id {
		return fmt.Errorf("prompt %q is not open", id)
	}
	a.promptByRemote = true
	defer func() { a.promptByRemote = false }()
	switch {
	case ans.Cancel:
		p.cancel()
	case p.wire.Kind == server.PromptSelect:
		if ans.Index == nil {
			return errors.New("index is required for a select prompt")
		}
		if i := *ans.Index; i < 0 || i >= len(p.wire.Options) {
			return fmt.Errorf("index %d is out of range (%d options)", i, len(p.wire.Options))
		}
		p.choose(*ans.Index)
	default:
		if ans.Text == nil {
			return errors.New("text is required for an input prompt")
		}
		p.submit(*ans.Text)
	}
	return nil
}

func (l remoteSession) Answer(id string, ans server.PromptAnswer) error {
	return l.input(func() error { return l.a.answerPrompt(id, ans) })
}
