package server

import (
	"context"
	"github.com/sebastianrcnt/atto/ui"
)

// Pending inputs are session data drawn once, shared by terminal and browser.
// Taking back an input remains native: it returns a draft to that client only.
func queueTree(p *PendingInput) *ui.Node {
	if p == nil {
		return nil
	}
	var children []ui.Node
	for _, group := range []struct {
		title string
		texts []string
	}{{"Messages to be submitted after next tool call", p.Steers}, {"Queued follow-up inputs", p.Queued}} {
		if len(group.texts) == 0 {
			continue
		}
		children = append(children, ui.Text(ui.TextProps{Color: ui.Muted, Text: group.title, Bold: true}))
		for _, text := range group.texts {
			children = append(children, ui.Text(ui.TextProps{Color: ui.Muted, Text: "↳ " + text, MaxLines: 3}))
		}
	}
	if p.Paused {
		children = append(children, ui.Button(ui.ButtonProps{Key: "resume", Label: "Resume queue"}))
	}
	if len(children) == 0 {
		return nil
	}
	n := ui.Box(ui.BoxProps{}, children...)
	return &n
}
func (t *thread) initUIQueue() {
	m := ui.Match{Site: ui.Band, ID: "atto/queue"}
	r := t.uiRegistry()
	r.Render("atto", m, func(ui.Event, ui.Next) (*ui.Node, error) { return queueTree(t.pending()), nil })
	r.Bind("atto", m, "resume", ui.Press, func(context.Context, ui.Action) error { t.resumeQueue(); t.pendingChanged(); return nil })
	_ = r.Open("atto", ui.OpenOptions{Site: ui.Band, ID: m.ID})
}
