package app

import (
	"strings"

	"github.com/sebastianrcnt/atto/trust"
	"github.com/sebastianrcnt/atto/tui"
)

const (
	trustAllowAll = "Allow all"
	trustReview   = "Review one by one"
	trustDeny     = "Deny"
	trustAllow    = "Allow"
)

// projectTrustPrompt keeps the existing select component and gives each item
// its own wrapped line, so no executable content disappears behind a title.
type projectTrustPrompt struct {
	list  *tui.SelectList
	items []trust.Item
}

func (p projectTrustPrompt) HandleInput(data string) { p.list.HandleInput(data) }

func (p projectTrustPrompt) Render(width int) []string {
	out := []string{tui.Truncate(tui.Bold("This project wants to run code on your machine:"), width, "…")}
	for _, in := range p.items {
		label := in.Label()
		if in.Kind == trust.Hook {
			label = in.Kind + "  " + in.Target
		}
		for _, line := range tui.Wrap(oneLine(tui.StripEscapes(label)), max(1, width-2)) {
			out = append(out, "  "+tui.Dim(line))
		}
	}
	return append(append(out, ""), p.list.Render(width)...)
}

func (p projectTrustPrompt) detail() string {
	var labels []string
	for _, in := range p.items {
		labels = append(labels, in.Label())
	}
	return strings.Join(labels, "\n")
}

// askProjectApprovals asks once for every pending kind together. Content
// skipped with Esc is not asked about again during this run; explicit denials
// are persisted by hash, just like approvals.
func (a *App) askProjectApprovals() {
	if a.trustActive {
		return
	}
	if a.modal != nil {
		a.trustWaiting = true
		return
	}
	a.trustWaiting = false
	items, err := trust.Discover(a.agent.Cwd)
	if err != nil {
		a.errorNotice(err)
	}
	var pending []trust.Item
	for _, in := range trust.Unapproved(items) {
		if !a.trustAsked[in.Key()] {
			pending = append(pending, in)
		}
	}
	if len(pending) == 0 {
		a.finishProjectTrust(false)
		return
	}
	if a.trustAsked == nil {
		a.trustAsked = map[string]bool{}
	}
	for _, in := range pending {
		a.trustAsked[in.Key()] = true
	}
	a.trustActive = true
	l := &tui.SelectList{Items: []tui.SelectItem{
		{Label: trustAllowAll, Value: trustAllowAll},
		{Label: trustReview, Value: trustReview},
		{Label: trustDeny, Value: trustDeny},
	}}
	l.OnSelect = func(it tui.SelectItem) {
		a.closeModal()
		if it.Value == trustReview {
			a.reviewProjectTrust(pending, false)
			return
		}
		for _, in := range pending {
			var err error
			if it.Value == trustAllowAll {
				err = trust.Approve(in)
			} else {
				err = trust.Deny(in)
			}
			if err != nil {
				a.errorNotice(err)
			}
		}
		a.finishProjectTrust(it.Value == trustAllowAll)
	}
	l.OnCancel = func() {
		a.closeModal()
		a.finishProjectTrust(false)
	}
	a.openModal(projectTrustPrompt{list: l, items: pending})
}

func (a *App) reviewProjectTrust(items []trust.Item, changed bool) {
	if len(items) == 0 {
		a.finishProjectTrust(changed)
		return
	}
	in := items[0]
	l := &tui.SelectList{Items: []tui.SelectItem{
		{Label: trustAllow, Value: trustAllow},
		{Label: trustDeny, Value: trustDeny},
	}}
	l.OnSelect = func(it tui.SelectItem) {
		a.closeModal()
		var err error
		if it.Value == trustAllow {
			err = trust.Approve(in)
			changed = changed || err == nil
		} else {
			err = trust.Deny(in)
		}
		if err != nil {
			a.errorNotice(err)
		}
		a.reviewProjectTrust(items[1:], changed)
	}
	l.OnCancel = func() {
		a.closeModal()
		a.reviewProjectTrust(items[1:], changed)
	}
	a.openModal(projectTrustPrompt{list: l, items: []trust.Item{in}})
}

func (a *App) finishProjectTrust(changed bool) {
	// Keep the queue paused while approvals take effect. During a running
	// turn /reload applies them at a safe boundary rather than replacing hooks
	// that the turn's goroutine is using.
	if changed {
		a.cmdReload("")
	}
	a.trustActive = false
	if done := a.trustDone; done != nil {
		a.trustDone = nil
		done()
	}
	a.maybeSendNextQueued()
}
