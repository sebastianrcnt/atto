package server

import (
	"time"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// wireItem is the protocol form of a transcript item of session sid.
func wireItem(sid string, it *transcript.Item) Item {
	w := Item{ID: it.ID, Text: it.Text, Status: string(it.Status), EntryID: it.EntryID, DurationMs: it.Duration.Milliseconds()}
	if !it.Started.IsZero() {
		w.StartedMs = it.Started.UnixMilli()
	}
	switch it.Kind {
	case transcript.User:
		w.Type = ItemUser
		w.Images = wireImages(it.Images)
	case transcript.Assistant:
		w.Type, w.BlockID = ItemAgent, blockID(sid, it)
	case transcript.Reasoning:
		w.Type, w.DurationMs, w.BlockID = ItemReasoning, it.Duration.Milliseconds(), blockID(sid, it)
	case transcript.Event:
		w.Type = ItemEvent
		for _, e := range events.Split(it.Text) {
			w.Titles = append(w.Titles, events.TitleOf(e))
		}
	case transcript.Goal:
		w.Type = ItemGoal
	case transcript.Hook:
		w.Type, w.HookEvent, w.Blocked = ItemHook, it.HookEvent, it.Blocked
	case transcript.Notice:
		w.Type, w.Level = ItemNotice, it.Level
	case transcript.GoalStatus:
		w.Type = ItemGoalStatus
		if g := it.GoalState; g != nil {
			c := *g
			w.GoalStatus, w.Text, w.GoalState = string(g.Status), g.Note, &c
		}
	case transcript.Tool:
		w.Type, w.Description, w.Command, w.Output, w.Pending = ItemCommand, it.Description, it.Command, it.Output, it.Pending
		w.CallID, w.TimeoutMs, w.Dropped = it.CallID, it.Timeout.Milliseconds(), it.Dropped
		if r := it.Result; r != nil {
			code := r.ExitCode
			w.ExitCode, w.DurationMs, w.TimedOut = &code, it.Duration.Milliseconds(), r.TimedOut
			w.Canceled, w.Error, w.ResultText = r.Canceled, r.Err, r.Text
			if r.Job > 0 { // still running
				w.ExitCode, w.Job, w.Background = nil, r.Job, r.Background
			}
		}
		w.Images = wireImages(it.Images)
	case transcript.Shell:
		// A command the user ran ("!cmd"), shown as a command.
		w.Type, w.Description, w.Command, w.Output, w.Shell = ItemCommand, "user command", it.Command, it.Output, true
		w.Excluded, w.Truncated, w.FullOutput, w.Dropped = it.Excluded, it.Truncated, it.FullOutput, it.Dropped
		if r := it.Result; r != nil {
			code := r.ExitCode
			w.ExitCode, w.DurationMs, w.Canceled = &code, it.Duration.Milliseconds(), r.Canceled
		}
	case transcript.UIBlock:
		w.Type, w.Title, w.Ext, w.UITree, w.UIRev, w.UIID = ItemUIBlock, it.Title, it.Ext, it.UITree, it.UIRevision, it.UIID
	case transcript.BranchSummary:
		w.Type, w.DurationMs = ItemBranchSummary, it.Duration.Milliseconds()
	case transcript.Compaction:
		w.Type, w.Auto, w.TokensBefore, w.TokensAfter = ItemCompaction, it.Auto, it.TokensBefore, it.TokensAfter
		w.DurationMs, w.Reason, w.Cap = it.Duration.Milliseconds(), it.Reason, it.Cap
	}
	return w
}

func wireImages(imgs []provider.Image) []ItemImage {
	var out []ItemImage
	for _, im := range imgs {
		out = append(out, ItemImage{Name: im.Name, Width: im.Width, Height: im.Height, File: im.File, MIME: im.MIME})
	}
	return out
}

// TranscriptItem is the transcript item a protocol item stands for: what
// a client that renders transcript items (the TUI) shows. It is the
// inverse of the server's mapping for every field a client renders.
func TranscriptItem(w Item) transcript.Item {
	it := transcript.Item{ID: w.ID, Text: w.Text, Status: transcript.Status(w.Status), EntryID: w.EntryID,
		Duration: time.Duration(w.DurationMs) * time.Millisecond}
	for _, im := range w.Images {
		it.Images = append(it.Images, provider.Image{Name: im.Name, Width: im.Width, Height: im.Height, File: im.File, MIME: im.MIME})
	}
	if w.StartedMs > 0 {
		it.Started = time.UnixMilli(w.StartedMs)
	}
	switch w.Type {
	case ItemUser:
		it.Kind = transcript.User
	case ItemAgent:
		it.Kind = transcript.Assistant
	case ItemReasoning:
		it.Kind = transcript.Reasoning
	case ItemEvent:
		it.Kind = transcript.Event
	case ItemGoal:
		it.Kind = transcript.Goal
	case ItemHook:
		it.Kind, it.HookEvent, it.Blocked = transcript.Hook, w.HookEvent, w.Blocked
	case ItemNotice:
		it.Kind, it.Level = transcript.Notice, w.Level
	case ItemGoalStatus:
		it.Kind, it.GoalState = transcript.GoalStatus, w.GoalState
		if w.GoalState != nil {
			it.Text = "" // the wire's text is the goal's note
		}
	case ItemCommand:
		it.Kind, it.Command, it.Output, it.Dropped = transcript.Tool, w.Command, w.Output, w.Dropped
		if w.Shell {
			it.Kind, it.Excluded, it.Truncated, it.FullOutput = transcript.Shell, w.Excluded, w.Truncated, w.FullOutput
		} else {
			it.Description, it.Pending, it.CallID = w.Description, w.Pending, w.CallID
			it.Timeout = time.Duration(w.TimeoutMs) * time.Millisecond
		}
		if w.ExitCode != nil || w.Job > 0 || w.Canceled || w.Error != "" || w.ResultText != "" || w.TimedOut {
			r := &transcript.ToolResult{TimedOut: w.TimedOut, Canceled: w.Canceled, Err: w.Error, Job: w.Job, Background: w.Background, Text: w.ResultText}
			if w.ExitCode != nil {
				r.ExitCode = *w.ExitCode
			}
			it.Result = r
		}
	case ItemCompaction:
		it.Kind, it.Auto, it.TokensBefore, it.TokensAfter = transcript.Compaction, w.Auto, w.TokensBefore, w.TokensAfter
		it.Reason, it.Cap = w.Reason, w.Cap
	case ItemBranchSummary:
		it.Kind = transcript.BranchSummary
	case ItemUIBlock:
		it.Kind, it.Title, it.Ext, it.UITree, it.UIRevision, it.UIID = transcript.UIBlock, w.Title, w.Ext, w.UITree, w.UIRev, w.UIID
	}
	return it
}

// blockID is the ID extensions name a reasoning or assistant item's block
// by: "" until its response is saved.
func blockID(sid string, it *transcript.Item) string {
	if it.EntryID == "" {
		return ""
	}
	kind := session.BlockText
	if it.Kind == transcript.Reasoning {
		kind = session.BlockReasoning
	}
	return session.BlockID(sid, it.EntryID, kind)
}

// ItemsFromEntries rebuilds a thread's items from its session file, with
// what extensions showed on them: pass the active branch.
func ItemsFromEntries(threadID string, entries []session.Entry) []Item {
	b := transcript.Builder{IDPrefix: itemPrefix(threadID)}
	items, _ := replayItems(&b, threadID, entries)
	return items
}

// replayItems replays the active branch of a session into b (reset first)
// and returns the items, with what extensions showed on them, and the
// blocks extensions can name.
func replayItems(b *transcript.Builder, sid string, branch []session.Entry) ([]Item, blocks) {
	bl := blocks{}
	b.Handler = transcript.Handler{ // no notifications for the replay
		Saved: func(it *transcript.Item) { bl.saved(sid, it) },
		Display: func(d transcript.Display) {
			if x := bl[session.BlockID(sid, d.EntryID, d.Block)]; x != nil {
				x.disp.Apply(d)
			}
		},
	}
	b.Reset()
	b.Replay(branch)
	b.Handler = transcript.Handler{}
	all := b.Items()
	items := make([]Item, 0, len(all))
	for i := range all {
		items = append(items, bl.attach(wireItem(sid, &all[i])))
	}
	for _, e := range branch {
		applyUIItems(items, e)
	}
	return items, bl
}

func itemPrefix(threadID string) string { return threadID + "-i" }
