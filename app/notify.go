package app

import (
	"encoding/json"
	"time"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
)

// onNotification applies one of the runtime's notifications, on the UI
// goroutine. Those of other threads are not this terminal's; those the
// last snapshot already has (eventId at most its own) were applied.
func (a *App) onNotification(n server.Notification) {
	if n.Method == "events/reset" {
		a.reread()
		return
	}
	if id := n.ThreadID(); id != a.threadID || id == "" {
		return
	}
	if n.EventID != 0 && n.EventID <= a.snapEvent {
		return
	}
	var p struct {
		Item     *server.Item         `json:"item"`
		ItemID   string               `json:"itemId"`
		Delta    string               `json:"delta"`
		BlockID  string               `json:"blockId"`
		Display  *server.BlockDisplay `json:"display"`
		Thread   *server.ThreadInfo   `json:"thread"`
		TurnID   string               `json:"turnId"`
		RunKind  string               `json:"runKind"`
		Status   string               `json:"status"`
		Activity json.RawMessage      `json:"activity"`
		Started  int64                `json:"startedAt"`
		Usage    *server.Usage        `json:"usage"`
		Step     *server.Usage        `json:"step"`
		Context  *int                 `json:"contextTokens"`
		Pending  *server.PendingInput `json:"pending"`
		Goal     *server.GoalInfo     `json:"goal"`
		UI       *server.ExtensionUI  `json:"ui"`
		Prompt   *server.Prompt       `json:"prompt"`
		ID       string               `json:"id"`
		Title    string               `json:"title"`
		ClientID string               `json:"clientId"`
		Text     string               `json:"text"`
		Images   []server.ItemImage   `json:"images"`
		IfEmpty  bool                 `json:"ifEmpty"`
		Jobs     int                  `json:"jobs"`
		Timers   int                  `json:"timers"`
		Line     string               `json:"line"`
		Finished bool                 `json:"finished"`
		Handoff  bool                 `json:"handoff"`
		At       int64                `json:"at"`
	}
	if json.Unmarshal(n.Params, &p) != nil {
		return
	}
	if a.busy {
		a.lastEvent = a.clock()
	}
	switch n.Method {
	case "item/started":
		if p.Item != nil {
			a.wireStarted(*p.Item)
		}
	case "item/delta":
		a.wireDelta(p.ItemID, p.Delta)
	case "item/updated":
		if p.Item != nil {
			a.wireUpdated(*p.Item)
		}
	case "item/completed":
		if p.Item != nil {
			a.wireCompleted(*p.Item)
		}
	case "item/display":
		a.wireDisplay(p.BlockID, p.Display)
	case "turn/started":
		a.turnStarted(p.RunKind, p.Started, p.Activity)
	case "turn/activity":
		a.setActivity(p.Activity)
	case "turn/completed":
		a.turnCompleted(p.RunKind, p.Status, p.Context)
	case "thread/usage":
		if p.Usage != nil && p.Step != nil {
			a.stepEnded(*p.Usage, *p.Step, p.Context)
		}
	case "turn/pending":
		if p.Pending != nil {
			a.pending = *p.Pending
		}
	case "thread/updated":
		if p.Thread != nil {
			a.setInfo(*p.Thread)
			a.busy, a.runKind = p.Thread.Busy, p.Thread.RunKind
		}
	case "thread/status":
		a.jobCount, a.timerCount = p.Jobs, p.Timers
	case "goal/updated":
		a.info.Goal = p.Goal
		if p.Goal == nil || !p.Goal.Held || a.busy {
			a.goalRetryAt = time.Time{}
		}
	case "goal/retry":
		a.goalRetryAt = time.UnixMilli(p.At)
	case "extension/ui":
		a.info.ExtensionUI = p.UI
	case "event":
		title := p.Title
		a.add(&eventBlock{title: title})
	case "prompt/open":
		if p.Prompt != nil {
			a.promptOpened(*p.Prompt)
		}
	case "prompt/closed":
		a.promptClosed(p.ID)
	case "input/recovered":
		if p.ClientID != "" && p.ClientID != a.conn.id {
			return
		}
		att := attachments(p.Images)
		if p.IfEmpty {
			if a.editorEmpty() {
				a.editor.SetText(p.Text, att...)
			}
			return
		}
		a.restoreToEditor([]string{p.Text}, att...)
	case "thread/branchChanged":
		a.reread()
	case "thread/reloaded":
		a.reloaded()
	case "commands/changed":
		a.loadCatalog()
	case "thread/handedOff":
		if p.Line != "" {
			a.bgLine = p.Line
		}
		a.doQuit()
	case "thread/closed":
		if !p.Handoff && !a.quitting {
			a.notice("This session was closed.")
			a.busy = false
		}
	}
}

// reread reads the thread again and shows it from scratch.
func (a *App) reread() {
	a.rpc("thread/read", nil, func(raw json.RawMessage, err error) {
		if err != nil {
			a.errorNotice(err)
			return
		}
		var info server.ThreadInfo
		if json.Unmarshal(raw, &info) == nil && info.ID == a.threadID {
			a.applySnapshot(info)
		}
	})
}

// applySnapshot shows a thread as the runtime has it: the transcript is
// drawn again from its items, those in progress included, and the
// footer's state taken over.
func (a *App) applySnapshot(info server.ThreadInfo) {
	a.ui.Body.Clear()
	a.ui.Redraw()
	a.ui.ScrollToBottom()
	a.addHeader()
	a.resetItems()
	a.setInfo(info)
	a.snapEvent = info.EventID
	a.busy, a.runKind = info.Busy, info.RunKind
	a.pending = server.PendingInput{}
	if info.Pending != nil {
		a.pending = *info.Pending
	}
	a.info.Prompt, a.info.Goal, a.info.ExtensionUI = info.Prompt, info.Goal, info.ExtensionUI
	if info.Busy {
		started := time.Now()
		if info.Turn != nil {
			started = time.UnixMilli(info.Turn.StartedAt)
		}
		a.beginRun(info.RunKind, started)
		if act := info.Activity; act != nil {
			a.activity, a.toolsRunning = act.Phase, act.ToolsRunning
		}
	}
	a.replaying = true
	for _, w := range info.Items {
		a.wireStarted(w)
		if w.Status != string(transcript.InProgress) {
			a.wireCompleted(w)
		}
	}
	a.replaying = false
	if p := info.Prompt; p != nil {
		a.promptOpened(*p)
	}
}

// wireStarted shows an item that started.
func (a *App) wireStarted(w server.Item) {
	if w.Type == server.ItemNotice {
		a.noticeItem(w)
		return
	}
	it := server.TranscriptItem(w)
	a.kinds[w.ID] = it.Kind
	if it.Kind == transcript.User {
		remote := w.ClientID != "" && a.conn != nil && w.ClientID != a.conn.id
		if g := w.SteerGroup; g != "" && g == a.steerGroup && a.steerBlock != nil {
			// The messages of one steer show as one block.
			a.steerBlock.text += "\n\n" + it.Text
			a.steerBlock.remote = a.steerBlock.remote || remote
			return
		}
		b := &userBlock{text: it.Text, remote: remote}
		a.add(b)
		a.steerGroup, a.steerBlock = w.SteerGroup, nil
		if w.SteerGroup != "" {
			a.steerBlock = b
		}
		return
	}
	a.steerGroup, a.steerBlock = "", nil
	if it.Kind == transcript.Tool && it.Pending && it.Status == transcript.InProgress {
		a.draftChars[w.ID] = len(it.Command) + len(it.Description)
	}
	// Live, streamed items start empty and grow by deltas; in a snapshot
	// they come with what they have, which goes in as one delta.
	streamed := false
	switch it.Kind {
	case transcript.Reasoning, transcript.Assistant, transcript.Tool, transcript.Shell, transcript.Compaction, transcript.BranchSummary:
		streamed = true
	}
	text, output := it.Text, it.Output
	if streamed {
		it.Text, it.Output = "", ""
	}
	a.itemStarted(&it)
	if w.BlockID != "" {
		a.bindBlock(w)
	}
	if !streamed {
		return
	}
	switch {
	case it.Kind == transcript.Tool || it.Kind == transcript.Shell:
		if output != "" {
			it.Output = output
			a.itemDelta(&it, output)
		}
	case text != "":
		it.Text = text
		a.itemDelta(&it, text)
	}
}

func (a *App) wireDelta(id, d string) {
	kind, ok := a.kinds[id]
	if !ok {
		return
	}
	it := transcript.Item{ID: id, Kind: kind, Status: transcript.InProgress}
	switch kind {
	case transcript.Reasoning, transcript.Assistant:
		a.streamChars += len(d)
		if a.activity == "Retrying" {
			a.activity = "Thinking"
		}
	}
	a.itemDelta(&it, d)
}

func (a *App) wireUpdated(w server.Item) {
	it := server.TranscriptItem(w)
	if it.Kind == transcript.Tool && it.Pending {
		a.draftChars[w.ID] = len(it.Command) + len(it.Description)
	}
	if it.Kind == transcript.Tool && !it.Pending {
		delete(a.draftChars, w.ID)
	}
	if w.Shell {
		a.shellPendingContext(w)
	}
	if w.BlockID != "" {
		a.bindBlock(w)
	}
	a.itemUpdated(&it)
}

func (a *App) wireCompleted(w server.Item) {
	if w.Type == server.ItemNotice {
		if _, started := a.kinds[w.ID]; !started && !a.replaying {
			a.noticeItem(w)
		}
		return
	}
	it := server.TranscriptItem(w)
	if _, ok := a.kinds[w.ID]; !ok { // complete from the start
		a.wireStarted(w)
	}
	delete(a.kinds, w.ID)
	if it.Kind == transcript.User {
		return
	}
	if w.BlockID != "" {
		a.bindBlock(w)
	}
	if w.Shell {
		a.shellPendingContext(w)
	}
	a.itemCompleted(&it)
	if w.Display != nil {
		a.wireDisplay(w.BlockID, w.Display)
	}
}

// noticeItem shows a notice of the runtime.
func (a *App) noticeItem(w server.Item) {
	a.kinds[w.ID] = transcript.Notice
	switch w.Level {
	case "error":
		a.add(&noticeBlock{text: w.Text, style: func(s string) string { return tui.FG(1, s) }})
	case "warning":
		a.add(&noticeBlock{text: w.Text, style: func(s string) string { return tui.FG(3, s) }})
	case "info":
		a.add(&infoBlock{title: w.Title, hint: w.Text})
	case "loaded":
		if w.Loaded != nil {
			a.loaded = *w.Loaded
			a.add(&loadedBlock{l: *w.Loaded, reloaded: w.Reloaded, changes: w.Changes, note: w.Note, d: &a.details})
		}
	default:
		a.add(&noticeBlock{text: w.Text, style: tui.Dim})
	}
}

// turnStarted follows a run that started: the activity line.
func (a *App) turnStarted(kind string, startedAt int64, act json.RawMessage) {
	a.busy, a.runKind = true, kind
	a.goalRetryAt = time.Time{}
	a.beginRun(kind, time.UnixMilli(startedAt))
	a.setActivity(act)
	a.statusTrigger()
}

// beginRun starts the activity line's state and its animation.
func (a *App) beginRun(kind string, started time.Time) {
	a.runKind = kind
	a.runStart, a.lastEvent, a.toolsRunning = started, a.clock(), 0
	a.turnOut, a.streamChars, a.turnIn = 0, 0, 0
	clear(a.draftChars)
	a.turnVerb = a.pickVerb()
	if a.stopTicker != nil {
		a.stopTicker()
	}
	stop := make(chan struct{})
	a.stopTicker = func() { close(stop) }
	go func() { // keep the spinner and timers moving
		t := time.NewTicker(a.ui.AnimationInterval())
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				a.ui.RequestRender()
			}
		}
	}()
}

func (a *App) setActivity(raw json.RawMessage) {
	var act server.Activity
	if len(raw) == 0 || json.Unmarshal(raw, &act) != nil {
		return
	}
	if act.Phase != "" {
		a.activity = act.Phase
	}
	a.toolsRunning = act.ToolsRunning
}

// turnCompleted follows the end of a run.
func (a *App) turnCompleted(kind, status string, ctx *int) {
	a.busy = false
	if a.stopTicker != nil {
		a.stopTicker()
		a.stopTicker = nil
	}
	if ctx != nil {
		a.ctxTokens = *ctx
	}
	if kind == "branchSummary" && status == "interrupted" && a.summaryAsked {
		a.cmdTree("") // canceled: the tree opens again, as in pi
	}
	if kind == "branchSummary" {
		a.summaryAsked = false
	}
	a.runKind = ""
	a.statusTrigger()
}

// stepEnded follows a model response: the session's totals and the
// response's own usage.
func (a *App) stepEnded(total, step server.Usage, ctx *int) {
	a.turnOut += step.OutputTokens
	clear(a.draftChars)
	a.turnIn += max(0, step.InputTokens-step.CachedInputTokens-step.CacheWriteTokens)
	a.streamChars = 0
	if ctx != nil {
		a.ctxTokens = *ctx
	}
	a.usage.set(total)
	a.usage.last.PromptTokens, a.usage.last.CachedTokens, a.usage.last.CompletionTokens = step.InputTokens, step.CachedInputTokens, step.OutputTokens
	a.usage.lastCost = a.model().Model.Cost
	a.statusTrigger()
}

// editorEmpty reports whether the editor has nothing typed.
func (a *App) editorEmpty() bool { return len(a.editor.Text()) == 0 || isBlank(a.editor.Text()) }

func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
