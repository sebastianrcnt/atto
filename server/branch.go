package server

import (
	"context"
	"errors"
	"runtime/debug"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// Going back, after pi. Sessions are trees: moving the active leaf to an
// entry continues the conversation from there while the old branch stays
// in the file. Moving to a user message goes back to just before it and
// gives its text (and images) to the client that asked, to edit and send
// again. A summary of the branch left can be written first
// (thread/navigate's summary); it is recorded where the leaf moves to.
// Every client is told (thread/branchChanged) and reads the thread again.

// summaryRequest asks for a summary of the branch being left;
// instructions are the user's own focus for it ("" for none).
type summaryRequest struct{ instructions string }

// summaryRun is a branch summary being written: where the move goes once
// it is done, and the summary when it is.
type summaryRun struct {
	id, leaf, text string
	client         string
	start          time.Time
	summary        string
}

// moveTo moves the leaf to entry id for client, first summarizing the
// branch being left when sum is set. While a run goes on it is
// interrupted first, and the move happens when it has stopped.
func (t *thread) moveTo(id string, sum *summaryRequest, client string) {
	if t.turns.Busy {
		// Queued and pending input belonged to the old branch: back to the
		// editor, as pi does before aborting.
		t.pendingTree, t.pendingSummary, t.treeClient = id, sum, client
		t.stashPending()
		t.turns.Cancel(nil)
		return
	}
	if id == t.sess.Leaf() {
		t.notice("", "Already at this point.")
		return
	}
	leaf, text, ok, err := session.BranchPointFile(t.sess.Path, id)
	if err != nil {
		t.errorNotice(err)
		return
	}
	if !ok {
		t.notice("", "That entry is no longer in the session.")
		return
	}
	if sum != nil {
		origin, content := "", false
		err := session.VisitAbandoned(t.sess.Path, t.sess.Leaf(), leaf, func(e session.Entry) error {
			content = content || agent.HasBranchContent([]session.Entry{e})
			if origin == "" {
				if text, ok := agent.BranchOrigin(e); ok {
					origin = text
				}
			}
			return nil
		})
		if err != nil {
			t.errorNotice(err)
			return
		}
		if content {
			if origin == "" {
				origin = "the last message"
			}
			t.summarizeBranch(id, leaf, text, origin, sum.instructions, client)
			return
		}
	}
	entry, _, _ := session.ReadEntry(t.sess.Path, id)
	t.finishMove([]session.Entry{entry}, id, leaf, text, nil, client)
}

// finishMove moves the leaf to leaf, recording summary (if any) there,
// and shows the branch. id is the entry picked and text the message to
// edit.
func (t *thread) finishMove(entries []session.Entry, id, leaf, text string, summary *session.Entry, client string) {
	t.stashPending()
	if summary != nil {
		t.sess.BranchSummary(leaf, *summary)
	} else {
		t.sess.Branch(leaf)
	}
	if err := t.sess.Err(); err != nil {
		t.errorNotice(err)
		return
	}
	t.showBranchDisk()
	if text != "" {
		t.recover(client, true, []string{text}, entryImages(entries, id))
	}
	t.notice("", "Navigated to the selected point. The earlier branch is kept (/tree).")
	t.afterGoingBack()
}

// stashPending gives queued messages and unsent steers back.
func (t *thread) stashPending() {
	byClient := map[string][]*pendingInput{}
	var order []string
	add := func(p *pendingInput) {
		if _, ok := byClient[p.Client]; !ok {
			order = append(order, p.Client)
		}
		byClient[p.Client] = append(byClient[p.Client], p)
	}
	drained := t.agent.DrainSteers()
	for _, s := range t.steers {
		add(s)
	}
	for _, q := range t.turns.Queued {
		add(q)
	}
	if ptr := t.turns.SendNow; ptr != nil {
		n := *ptr
		add(n)
	}
	_ = drained // events and goal notes among them are dropped
	t.turns.Queued, t.steers, t.turns.QueuePaused, t.turns.SendSteersAfterInterrupt, t.turns.SendNow = nil, nil, false, false, nil
	t.turns.Steers = nil
	for _, c := range order {
		var texts []string
		var imgs []provider.Image
		for _, p := range byClient[c] {
			texts, imgs = append(texts, p.Text), append(imgs, p.Images...)
		}
		t.recover(c, false, texts, imgs)
	}
	t.pendingChanged()
}

// entryImages are the images of user message id, loaded.
func entryImages(entries []session.Entry, id string) []provider.Image {
	var out []provider.Image
	for _, e := range entries {
		if e.ID != id || e.Message == nil {
			continue
		}
		for _, im := range e.Message.Images {
			if loaded, err := images.Load(im); err == nil {
				im = loaded
			}
			out = append(out, im)
		}
		break
	}
	return out
}

// showBranchDisk restores only model context and a bounded display tail.
func (t *thread) showBranchDisk() {
	loaded, err := session.ReadContext(t.sess.Path)
	if err != nil {
		t.errorNotice(err)
		return
	}
	t.agent.Restore(loaded.Entries)
	t.ctx = t.agent.ContextTokens()
	if err := t.replayDisk(); err != nil {
		t.errorNotice(err)
		return
	}
	t.publish("thread/branchChanged", map[string]any{})
	t.updated()
	if len(t.attached) == 0 {
		t.dropDisplay()
	}
	debug.FreeOSMemory()
}

// afterGoingBack pauses an active goal (its progress may be gone) and
// points out background jobs, which keep running.
func (t *thread) afterGoingBack() {
	if g := t.goal.Goal; g != nil && g.Status == goal.Active {
		g.Status, g.Note = goal.Paused, "went back in the session"
		t.goal.Set(g)
		t.notice("", "The goal is paused. /goal resume to continue.")
	}
	if t.jobCount > 0 {
		t.notice("", "%d background job(s) keep running (/jobs).", t.jobCount)
	}
	t.notice("", "Files changed by commands on the old branch stay changed.")
}

// summarizeBranch has the model summarize left, the entries a move to
// leaf leaves behind, then moves (afterBranchSummary).
func (t *thread) summarizeBranch(id, leaf, text string, origin, instructions, client string) {
	run := &summaryRun{id: id, leaf: leaf, text: text, client: client, start: time.Now()}
	t.summary = run
	t.start("branchSummary", "Summarizing branch", func(ctx context.Context, emit func(any)) error {
		s, err := t.agent.SummarizeBranchFrom(ctx, origin, instructions, emit)
		run.summary = s // read on the lane once the run is over
		return err
	})
}

// afterBranchSummary finishes the move once the summary is written, and
// reports whether it did. Failed or canceled, the leaf stays.
func (t *thread) afterBranchSummary(err error) bool {
	run := t.summary
	t.summary = nil
	if run == nil || err != nil {
		return false
	}
	entry, _, _ := session.ReadEntry(t.sess.Path, run.id)
	t.finishMove([]session.Entry{entry}, run.id, run.leaf, run.text, &session.Entry{
		Summary:   run.summary,
		ElapsedMs: time.Since(run.start).Milliseconds(),
	}, run.client)
	return true
}

// fork writes a new session holding the path to just before user message
// id, and returns it with the message's text and images.
func (t *thread) fork(id string) (path, threadID, text string, imgs []provider.Image, err error) {
	leaf, text, ok, readErr := session.BranchPointFile(t.sess.Path, id)
	if readErr != nil {
		return "", "", "", nil, readErr
	}
	if !ok {
		return "", "", "", nil, errors.New("that entry is no longer in the session")
	}
	w, forkErr := session.ForkFile(t.sess.Path, t.cwd, leaf)
	if forkErr != nil {
		return "", "", "", nil, forkErr
	}
	// Even a fork before the first message must be resumable by a remote
	// client; an empty branch marker forces the lazy writer's header out.
	if w.Leaf() == "" {
		w.Branch("")
	}
	w.Close()
	if err := w.Err(); err != nil {
		return "", "", "", nil, err
	}
	entry, _, _ := session.ReadEntry(t.sess.Path, id)
	debug.FreeOSMemory()
	return w.Path, w.ID, text, entryImages([]session.Entry{entry}, id), nil
}
