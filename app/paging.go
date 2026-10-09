package app

import (
	"encoding/json"
	"slices"

	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
)

func (a *App) loadEarlier() {
	if a.pageLoading || !a.view.Info.HasMore || a.view.Info.Before == "" {
		return
	}
	a.pageLoading = true
	id, before, epoch := a.threadID, a.view.Info.Before, a.pageEpoch
	loading := &noticeBlock{text: "loading earlier messages", style: tui.Dim}
	children := a.ui.Body.Children
	at := min(1, len(children))
	a.ui.Body.Children = append(slices.Clone(children[:at]), append([]tui.Component{loading}, children[at:]...)...)
	a.rpc("thread/items", map[string]any{"before": before, "offline": a.info.Offline}, func(raw json.RawMessage, err error) {
		if id != a.threadID || epoch != a.pageEpoch {
			return
		}
		a.pageLoading = false
		a.ui.Body.Remove(loading)
		if err != nil {
			a.errorNotice(err)
			return
		}
		var page server.ItemPage
		if json.Unmarshal(raw, &page) != nil {
			return
		}
		added := a.view.Prepend(page)
		if len(added) == 0 {
			return
		}
		old := a.ui.Body.Children
		// Keep existing component identities so TUI's normal scroll anchoring keeps
		// the first visible line fixed while the older page is inserted above it.
		thinking, text, compact, summary, shell := a.thinking, a.text, a.compact, a.summaryBlk, a.shellBlk
		steerGroup, steerBlock := a.steerGroup, a.steerBlock
		a.thinking, a.text, a.compact, a.summaryBlk, a.shellBlk = nil, nil, nil, nil, nil
		a.steerGroup, a.steerBlock = "", nil
		a.ui.Body.Children = nil
		a.replaying = true
		for _, w := range added {
			a.wireStarted(w)
			if w.Status != "inProgress" {
				a.wireCompleted(w)
			}
		}
		a.replaying = false
		earlier := a.ui.Body.Children
		start := min(1, len(old)) // Keep the session header at the top.
		later := old[start:]
		// Rejoin a tool group cut at a page boundary, preserving the existing group
		// identity (and its expansion state and viewport anchor).
		if len(earlier) > 0 && len(later) > 0 {
			left, lok := earlier[len(earlier)-1].(gap)
			right, rok := later[0].(gap)
			if lok && rok {
				l, lok := left.Component.(*toolRun)
				r, rok := right.Component.(*toolRun)
				if lok && rok {
					r.members = append(l.members, r.members...)
					earlier = earlier[:len(earlier)-1]
				}
			}
		}
		a.ui.Body.Children = append(slices.Clone(old[:start]), append(earlier, later...)...)
		a.thinking, a.text, a.compact, a.summaryBlk, a.shellBlk = thinking, text, compact, summary, shell
		a.steerGroup, a.steerBlock = steerGroup, steerBlock
	})
}
