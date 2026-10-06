package transcript

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// keepOutput is how much of a command's output an item keeps: the tail,
// trimmed back to this once it reaches twice as much.
const keepOutput = 64 * 1024

// Input is the message a front end sends to the model, given to the
// Builder as an event right before the run starts. The agent emits no
// event for it, and showing it at once (before hooks and a pre-turn
// compaction run) is what users expect. It becomes a user, event or goal
// item according to its prefix.
type Input struct {
	Text   string
	Images []provider.Image
}

// ShellStart, ShellOutput and ShellEnd are events a front end gives the
// Builder for a command the user runs ("!cmd"): the agent emits none. At
// most one runs at a time. Unlike the agent's events they may arrive in the
// middle of a turn.
type (
	ShellStart struct {
		Command string
		Exclude bool
	}
	ShellOutput struct{ Chunk string }
	ShellEnd    struct{ Exec session.BashExec }
)

// Handler receives items as they change. The *Item is the Builder's own
// and keeps changing: copy what must stay. Any field may be nil.
type Handler struct {
	Started func(it *Item)
	// Delta is text appended to the item: to Text, or for a tool to
	// Output. It is applied to the item before the call.
	Delta func(it *Item, text string)
	// Updated fires when a tool item changes other than by output: its
	// description and command as the model writes the call (Pending), and
	// once more when the call is complete and starts running.
	Updated func(it *Item)
	// Completed fires once per item, with its final state. Items that are
	// complete from the start (messages, hooks) get Started and Completed
	// back to back.
	Completed func(it *Item)
	// Saved fires for the reasoning and assistant items of a model response
	// once it is recorded and they have their EntryID: before Completed
	// live, and on replay too.
	Saved func(it *Item)
	// Display fires for a saved block_display entry (replay only; live,
	// extensions act on the front end directly).
	Display func(d Display)
}

// Display is what an extension showed on a block: see session.Entry.
type Display struct {
	EntryID string // of the assistant message
	Block   string // session.BlockText or session.BlockReasoning
	Ext     string
	Status  string
	Text    string
}

// Builder makes items from agent events (Event) and saved entries
// (Replay). It is not safe for concurrent use: feed it from the goroutine
// that receives the events.
type Builder struct {
	// IDPrefix starts every item ID; a counter follows ("<thread>-i3").
	IDPrefix string
	Handler  Handler

	items []*Item
	seq   int

	// The items receiving the current stream, and text that arrived
	// before them: an item only starts with something visible, so a step
	// that streams only whitespace leaves no item, as on replay.
	reasoning, text, compact *Item
	summary                  *Item // a branch summary being written
	shell                    *Item // a command the user is running
	pendReasoning, pendText  string
	step                     []*Item // reasoning and text items of the response being streamed
	thinkStart               time.Time
	tools                    map[string]*Item // by call ID, while running
	drafts                   map[int]*Item    // by index in the response, while the model writes the call

	// Replay: tool calls of the last assistant message waiting for their
	// results.
	calls []provider.ToolCall
}

// Items returns copies of every item so far, in the order they started.
func (b *Builder) Items() []Item {
	out := make([]Item, len(b.items))
	for i, it := range b.items {
		out[i] = it.clone()
	}
	return out
}

// Open returns copies of the items still in progress (being streamed,
// running, or a command the model is still writing), in the order they
// started.
func (b *Builder) Open() []Item {
	var out []Item
	for _, it := range b.items {
		if it.Status == InProgress || it.Pending {
			out = append(out, it.clone())
		}
	}
	return out
}

// Reset forgets every item and starts the IDs over.
func (b *Builder) Reset() {
	h, p := b.Handler, b.IDPrefix
	*b = Builder{Handler: h, IDPrefix: p}
}

// Event applies an agent event (or an Input).
func (b *Builder) Event(ev any) { b.apply(ev, time.Now()) }

// Add appends a finished item a front end makes itself, such as a notice
// or a goal status change.
func (b *Builder) Add(it Item) {
	if it.Status == "" {
		it.Status = Completed
	}
	b.add(it)
}

// End finishes what a run left open: text and reasoning complete, a
// command, compaction or branch summary still running failed (it was
// interrupted).
func (b *Builder) End() { b.end(time.Now()) }

func (b *Builder) end(at time.Time) {
	b.step = nil
	b.closeText(at)
	for _, it := range b.items { // in order, unlike the maps
		if it.Kind == Tool && it.Pending {
			b.endDraft(it, "")
		} else if it.Kind == Tool && it.Status == InProgress {
			b.endTool(it.CallID, ToolResult{Canceled: true, ExitCode: -1}, 0)
		}
	}
	for _, c := range []**Item{&b.compact, &b.summary, &b.shell} {
		if *c != nil {
			it := *c
			*c = nil
			it.Status = Failed
			b.completed(it)
		}
	}
}

func (b *Builder) apply(ev any, at time.Time) {
	switch e := ev.(type) {
	case Input:
		b.step = nil
		b.input(e.Text, e.Images)
	case agent.MessageSaved:
		for _, it := range b.step {
			it.EntryID = e.EntryID
			if b.Handler.Saved != nil {
				b.Handler.Saved(it)
			}
		}
		b.step = nil
	case ShellStart:
		b.shell = b.start(Item{Kind: Shell, Status: InProgress, Command: e.Command, Excluded: e.Exclude})
	case ShellOutput:
		if it := b.shell; it != nil {
			it.Output += e.Chunk
			if len(it.Output) > 2*keepOutput {
				cut := len(it.Output) - keepOutput
				it.Dropped += cut
				it.Output = it.Output[cut:]
			}
			b.delta(it, e.Chunk)
		}
	case ShellEnd:
		if it := b.shell; it != nil {
			b.shell = nil
			x := e.Exec
			// What was saved, as replays have only that.
			it.Output, it.Dropped = x.Output, 0
			it.Truncated, it.FullOutput = x.Truncated, x.FullOutputPath
			it.Result = &ToolResult{ExitCode: x.ExitCode, Canceled: x.Cancelled}
			it.Duration = time.Duration(x.DurationMs) * time.Millisecond
			it.Status = Completed
			if it.Result.Failed() {
				it.Status = Failed
			}
			b.completed(it)
		}
	case agent.ReasoningDelta:
		if b.thinkStart.IsZero() {
			b.thinkStart = at
		}
		b.stream(&b.reasoning, &b.pendReasoning, Reasoning, e.Text)
	case agent.TextDelta:
		b.finishReasoning(at)
		b.stream(&b.text, &b.pendText, Assistant, e.Text)
	case agent.StepEnd:
		b.closeText(at)
	case agent.ToolDraft:
		b.closeText(at)
		b.draft(e.Index, e.Args)
	case agent.ToolDraftEnd:
		if it := b.drafts[e.Index]; it != nil {
			b.endDraft(it, e.Err)
		}
	case agent.ToolStart:
		b.closeText(at)
		b.startTool(e.ID, e.Index, e.Args, e.Timeout)
	case agent.ToolOutput:
		b.toolOutput(e.ID, e.Chunk)
	case agent.ToolEnd:
		r := e.Result
		res := ToolResult{ExitCode: r.ExitCode, TimedOut: r.TimedOut, Canceled: r.Canceled, Text: e.Text, Job: r.Job, Background: r.Background}
		if r.Err != nil {
			res.Err = r.Err.Error()
		}
		b.endTool(e.ID, res, r.Duration, e.Images...)
	case agent.SteerCommitted:
		// One item per message, as the session holds them.
		b.closeText(at)
		for _, t := range e.Texts {
			b.input(t, nil)
		}
	case agent.HookNotice:
		b.add(Item{Kind: Hook, Status: Completed, HookEvent: e.Event, Text: e.Message, Blocked: e.Blocked})
	case agent.CompactStart:
		b.closeText(at)
		b.compact = b.start(Item{Kind: Compaction, Status: InProgress, Auto: e.Auto})
	case agent.CompactTrimmed:
		b.add(Item{Kind: Notice, Status: Completed, Text: fmt.Sprintf("Compaction left out the %d oldest messages: the conversation no longer fit the context window with room for the notes.", e.Messages)})
	case agent.CompactDelta:
		if c := b.compact; c != nil {
			c.Text += e.Text
			b.delta(c, e.Text)
		}
	case agent.BranchSummaryStart:
		b.closeText(at)
		b.summary = b.start(Item{Kind: BranchSummary, Status: InProgress})
	case agent.BranchSummaryDelta:
		if c := b.summary; c != nil {
			c.Text += e.Text
			b.delta(c, e.Text)
		}
	case agent.BranchSummaryEnd:
		if c := b.summary; c != nil {
			b.summary = nil
			c.Text, c.Duration, c.Status = e.Summary, e.Elapsed.Truncate(time.Millisecond), Completed
			b.completed(c)
		}
	case agent.CompactEnd:
		if c := b.compact; c != nil {
			b.compact = nil
			c.Text, c.TokensBefore, c.TokensAfter = e.Notes, e.Before, e.After
			c.Duration, c.Status = e.Elapsed.Truncate(time.Millisecond), Completed
			b.completed(c)
		}
	}
}

// input adds the message sent to the model. Prefixes say who it is from.
func (b *Builder) input(text string, imgs []provider.Image) {
	it := Item{Kind: User, Status: Completed, Text: text}
	switch {
	case events.IsEvent(text):
		it.Kind = Event
	case goal.IsMessage(text):
		it.Kind = Goal
	case strings.HasPrefix(text, agent.StopHookPrefix):
		// The reason a Stop hook gave for keeping the turn going: live it
		// arrives as the hook's notice, which this matches.
		it.Kind, it.HookEvent, it.Blocked = Hook, "Stop", true
		it.Text = strings.TrimPrefix(text, agent.StopHookPrefix)
	default:
		// A goal's state note rides at the end of the user's message, for
		// the model only.
		it.Text, _ = goal.SplitNote(text)
	}
	for _, im := range imgs {
		im.Data = nil // the bytes stay in the image store
		it.Images = append(it.Images, im)
	}
	b.add(it)
}

// stream appends text to the open item of kind, starting it once the text
// shows something.
func (b *Builder) stream(cur **Item, pend *string, kind Kind, text string) {
	if *cur == nil {
		*pend += text
		if strings.TrimSpace(*pend) == "" {
			return
		}
		text, *pend = *pend, ""
		*cur = b.start(Item{Kind: kind, Status: InProgress})
		b.step = append(b.step, *cur)
	}
	it := *cur
	it.Text += text
	b.delta(it, text)
}

// finishReasoning completes the reasoning when the answer starts (or the
// step ends): the thinking time runs from its first delta to then.
func (b *Builder) finishReasoning(at time.Time) {
	if it := b.reasoning; it != nil {
		b.reasoning = nil
		it.Duration = max(0, at.Sub(b.thinkStart).Truncate(time.Millisecond))
		it.Status = Completed
		b.completed(it)
	}
	b.pendReasoning, b.thinkStart = "", time.Time{}
}

// closeText ends the step's streamed items.
func (b *Builder) closeText(at time.Time) {
	b.finishReasoning(at)
	if it := b.text; it != nil {
		b.text = nil
		it.Status = Completed
		b.completed(it)
	}
	b.pendText = ""
}

// draft shows a tool call the model is still writing: it starts the call's
// item, pending, and updates it as the arguments arrive.
func (b *Builder) draft(index int, args agent.BashArgs) {
	it := b.drafts[index]
	if it == nil {
		if b.drafts == nil {
			b.drafts = map[int]*Item{}
		}
		b.drafts[index] = b.start(Item{Kind: Tool, Status: InProgress, Pending: true,
			Description: args.Description, Command: args.Command})
		return
	}
	if it.Description == args.Description && it.Command == args.Command {
		return
	}
	it.Description, it.Command = args.Description, args.Command
	b.updated(it)
}

// endDraft finishes a pending call that will not run, failed with err (or
// canceled when err is empty).
func (b *Builder) endDraft(it *Item, err string) {
	for i, d := range b.drafts {
		if d == it {
			delete(b.drafts, i)
		}
	}
	res := ToolResult{ExitCode: -1, Err: err, Canceled: err == ""}
	it.Pending = false
	it.Result = &res
	it.Status = Failed
	b.completed(it)
}

// startTool starts a call that runs now: the item its draft made, if any.
func (b *Builder) startTool(id string, index int, args agent.BashArgs, timeout time.Duration) {
	if b.tools == nil {
		b.tools = map[string]*Item{}
	}
	if it := b.drafts[index]; it != nil {
		delete(b.drafts, index)
		it.Pending, it.CallID = false, id
		it.Description, it.Command, it.Timeout = args.Description, args.Command, timeout
		b.tools[id] = it
		b.updated(it)
		return
	}
	b.tools[id] = b.start(Item{Kind: Tool, Status: InProgress, CallID: id,
		Description: args.Description, Command: args.Command, Timeout: timeout})
}

func (b *Builder) toolOutput(id, chunk string) {
	it := b.tools[id]
	if it == nil {
		return
	}
	it.Output += chunk
	if len(it.Output) > 2*keepOutput {
		cut := len(it.Output) - keepOutput
		it.Dropped += cut
		it.Output = it.Output[cut:]
	}
	b.delta(it, chunk)
}

func (b *Builder) endTool(id string, res ToolResult, d time.Duration, imgs ...provider.Image) {
	it := b.tools[id]
	if it == nil {
		return
	}
	delete(b.tools, id)
	it.Output = tidy(it.Output)
	for _, im := range imgs {
		im.Data = nil // the bytes stay in the image store
		it.Images = append(it.Images, im)
	}
	it.Result, it.Duration = &res, d.Truncate(time.Millisecond)
	it.Status = Completed
	if res.Failed() {
		it.Status = Failed
	}
	b.completed(it)
}

func (b *Builder) start(it Item) *Item {
	b.seq++
	it.ID = b.IDPrefix + strconv.Itoa(b.seq)
	p := &it
	b.items = append(b.items, p)
	if b.Handler.Started != nil {
		b.Handler.Started(p)
	}
	return p
}

func (b *Builder) add(it Item) {
	b.completed(b.start(it))
}

func (b *Builder) delta(it *Item, text string) {
	if b.Handler.Delta != nil {
		b.Handler.Delta(it, text)
	}
}

func (b *Builder) updated(it *Item) {
	if b.Handler.Updated != nil {
		b.Handler.Updated(it)
	}
}

func (b *Builder) completed(it *Item) {
	if b.Handler.Completed != nil {
		b.Handler.Completed(it)
	}
}

// tidy is the output as the model gets it, before truncation: CRLF as LF
// and no trailing whitespace-only lines. A replayed result has nothing
// more, so live and replayed commands show the same output.
func tidy(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// --- replay ---

// FromEntries is the transcript of a saved conversation: pass the active
// branch (session.Active), not the whole file.
func FromEntries(idPrefix string, entries []session.Entry) []Item {
	b := Builder{IDPrefix: idPrefix}
	b.Replay(entries)
	return b.Items()
}

// Replay adds the items of saved entries (the active branch) as if their
// events had happened: each entry becomes the events the agent emitted
// when it was written, applied by the same code as live ones.
func (b *Builder) Replay(entries []session.Entry) {
	for _, e := range entries {
		m := e.Message
		if e.Type == session.TypeMessage && m != nil && m.Role == "tool" {
			b.replayResult(e)
			continue
		}
		switch e.Type {
		case session.TypeCompaction:
			b.interruptCalls()
			b.apply(agent.CompactStart{Auto: e.Auto}, e.Time)
			b.apply(agent.CompactEnd{Notes: e.Notes, Before: e.TokensBefore, After: e.TokensAfter,
				Elapsed: time.Duration(e.ElapsedMs) * time.Millisecond}, e.Time)
		case session.TypeBranchSummary:
			b.interruptCalls()
			b.apply(agent.BranchSummaryStart{}, e.Time)
			b.apply(agent.BranchSummaryEnd{Summary: e.Summary, Elapsed: time.Duration(e.ElapsedMs) * time.Millisecond}, e.Time)
		case session.TypeBlockDisplay:
			if b.Handler.Display != nil {
				b.Handler.Display(Display{EntryID: e.TargetID, Block: e.Block, Ext: e.Ext, Status: e.Status, Text: e.Display})
			}
		case session.TypeExtText:
			// Like block_display, it leaves the calls waiting for results alone.
			b.add(Item{Kind: ExtText, Ext: e.Ext, Title: e.Title, Text: e.Display, Lang: e.Lang, Preview: e.Preview})
		case session.TypeBashExecution:
			if x := e.Bash; x != nil {
				b.interruptCalls()
				b.apply(ShellStart{Command: x.Command, Exclude: x.Exclude}, e.Time)
				b.apply(ShellOutput{Chunk: x.Output}, e.Time)
				b.apply(ShellEnd{Exec: *x}, e.Time)
			}
		case session.TypeMessage:
			if m == nil {
				continue
			}
			b.interruptCalls()
			switch m.Role {
			case "user":
				b.apply(Input{Text: m.Content, Images: m.Images}, e.Time)
			case "assistant":
				// Thinking ran from the first reasoning to the first text.
				answer := e.Time.Add(time.Duration(e.ThinkingMs) * time.Millisecond)
				b.apply(agent.ReasoningDelta{Text: m.ReasoningContent}, e.Time)
				b.apply(agent.TextDelta{Text: m.Content}, answer)
				b.apply(agent.MessageSaved{EntryID: e.ID}, answer)
				b.apply(agent.StepEnd{}, answer)
				b.calls = append(b.calls, m.ToolCalls...)
			}
		}
	}
	b.interruptCalls()
}

// replayResult runs a tool call of the last assistant message: start,
// output and end, as the agent emitted them. Results come in call order.
func (b *Builder) replayResult(e session.Entry) {
	m := e.Message
	i := 0
	for i < len(b.calls) && b.calls[i].ID != m.ToolCallID {
		i++
	}
	if i == len(b.calls) {
		return // no such call on this branch
	}
	for _, tc := range b.calls[:i] { // calls with no result
		b.startCall(tc, nil)
		b.endTool(tc.ID, ToolResult{Canceled: true, ExitCode: -1}, 0)
	}
	tc := b.calls[i]
	b.calls = b.calls[i+1:]
	b.startCall(tc, e.Tool)
	res, d := ToolResult{Text: m.Content}, time.Duration(0)
	if t := e.Tool; t != nil {
		res.ExitCode, res.TimedOut, res.Canceled = t.ExitCode, t.TimedOut, t.Canceled
		res.Job, res.Background = t.Job, t.Background
		d = time.Duration(t.DurationMs) * time.Millisecond
	}
	b.toolOutput(tc.ID, shownOutput(m.Content, e.Tool))
	b.endTool(tc.ID, res, d, m.Images...)
}

// interruptCalls ends tool calls that have no result: the turn was
// interrupted (or atto quit) before they ran.
func (b *Builder) interruptCalls() {
	for _, tc := range b.calls {
		b.startCall(tc, nil)
		b.endTool(tc.ID, ToolResult{Canceled: true, ExitCode: -1}, 0)
	}
	b.calls = nil
}

// startCall starts a recorded tool call with the arguments the agent ran
// it with: the description it recorded (a hook may have changed it) or
// the same default.
func (b *Builder) startCall(tc provider.ToolCall, meta *session.ToolMeta) {
	var args agent.BashArgs
	_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	switch {
	case meta != nil && meta.Description != "":
		args.Description = meta.Description
	case args.Description == "":
		args.Description = agent.FirstLine(args.Command)
	}
	if args.Description == "" {
		args.Description = tc.Function.Name
	}
	b.apply(agent.ToolStart{ID: tc.ID, Args: args, Timeout: args.TimeLimit()}, time.Time{})
}

// shownOutput is a recorded tool result without the status line the agent
// appended for the model ("[exit code 1]", "[no output]"...): the item
// shows the status itself, as it does live.
func shownOutput(content string, t *session.ToolMeta) string {
	if t == nil {
		return content
	}
	rest, last := "", content
	if before, after, found := strings.CutLast(content, "\n"); found {
		rest, last = before, after
	}
	var ok bool
	switch {
	case t.Job > 0:
		ok = agent.BackgroundStatus(last, t.Job)
	case t.Canceled:
		ok = last == "[canceled by user]"
	case t.TimedOut:
		ok = strings.HasPrefix(last, "[timed out after ")
	case t.ExitCode != 0:
		ok = last == fmt.Sprintf("[exit code %d]", t.ExitCode)
	default:
		ok = rest == "" && last == "[no output]"
	}
	if ok {
		return rest
	}
	return content
}
