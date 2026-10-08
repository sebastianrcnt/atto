package agent

import (
	"context"
	"fmt"
	"slices"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// Run sends input and loops through tool calls until the model stops.
// Compaction runs automatically before the turn and between tool calls
// when the context passes AutoCompactLimit.
func (a *Agent) Run(ctx context.Context, input string, emit func(any)) error {
	return a.RunWithImages(ctx, input, nil, emit)
}

// modelChangeNote tells the model that the conversation's last reply came
// from another model, so it doesn't take that reply's words or habits for
// its own. It goes with the next user message only: once this model has
// replied, the last reply is its own.
func (a *Agent) modelChangeNote() string {
	a.cfgMu.Lock()
	cur := a.model.ProviderName + "/" + a.model.Model.ID
	a.cfgMu.Unlock()
	for _, m := range slices.Backward(a.messages) {
		if m.Role != "assistant" {
			continue
		}
		if m.Model == "" { // written before atto recorded models
			return ""
		}
		if prev := m.Provider + "/" + m.Model; prev != cur {
			return fmt.Sprintf("[atto] The model changed from %s to %s. Earlier assistant messages were written by %s.", prev, cur, prev)
		}
		return ""
	}
	return ""
}

// sentInput remembers a user message added by RunWithImages: what the user
// typed (before hooks and notes were added), and the message as stored.
type sentInput struct {
	input string
	imgs  []provider.Image
	msg   provider.Message
	index int // position of msg in the conversation
}

// unanswered reports whether the conversation still ends with the message
// sent as input and imgs, which no reply followed: the turn failed before
// the model answered, and the user is sending the same message again.
func (a *Agent) unanswered(input string, imgs []provider.Image) bool {
	s := a.sent
	if s.msg.Role == "" || s.index != len(a.messages)-1 || a.messages[s.index].Content != s.msg.Content {
		return false
	}
	return s.input == input && slices.EqualFunc(s.imgs, imgs, func(x, y provider.Image) bool { return x.File == y.File })
}

// RunWithImages is Run with images attached to the user message. Their
// bytes must be loaded, and saved with images.Save for the session to
// resume with them.
func (a *Agent) RunWithImages(ctx context.Context, input string, imgs []provider.Image, emit func(any)) (err error) {
	note := a.takeInputNote()
	raw := input
	if a.Extensions != nil {
		a.Extensions.TurnStart(input)
		defer func() { a.Extensions.TurnEnd(err) }()
	}
	if a.Hooks != nil {
		o := a.Hooks.UserPromptSubmit(ctx, input)
		emitHook(emit, "UserPromptSubmit", o)
		switch {
		case o.Stop:
			return ErrStoppedByHook
		case o.Block:
			return ErrPromptBlocked
		case o.Context != "":
			input += "\n\n" + o.Context
		}
	}
	if a.Extensions != nil {
		o := a.Extensions.UserPrompt(ctx, input)
		emitHook(emit, ExtensionEvent+"user_prompt", o)
		switch {
		case o.Block:
			return ErrPromptBlocked
		case o.Context != "":
			input += "\n\n" + o.Context
		}
	}
	// The last attempt at this message failed before the model answered it:
	// it is still in the conversation, so run it again as Continue does
	// instead of adding a copy. (Compaction would rewrite it, so it waits;
	// nothing has been added since the first attempt was checked.)
	if a.unanswered(raw, imgs) {
		return a.loop(ctx, emit, true)
	}
	if n := a.modelChangeNote(); n != "" {
		input += "\n\n" + n
	}
	if note != "" {
		input += "\n\n" + note
	}
	// The message counts too: a large paste can take the request past the
	// limit by itself, and compacting after it was added would cut it.
	if a.needsCompactWith(estimateChars(input) + len(imgs)*imageChars) {
		if err := a.compact(ctx, emit, true); err != nil {
			return err
		}
	}
	m := provider.Message{Role: "user", Content: input, Images: imgs}
	a.appendMessage(m, session.Entry{})
	a.sent = sentInput{input: raw, imgs: imgs, msg: m, index: len(a.messages) - 1}
	return a.loop(ctx, emit, true)
}

// Continue runs the rest of a turn that was stopped, without a new user
// message: the conversation ends with the user's message or tool results
// the model has not answered yet (experimental, for runs left in the
// background).
func (a *Agent) Continue(ctx context.Context, emit func(any)) error {
	return a.loop(ctx, emit, false)
}

// loop is the turn: model calls and tool calls until the model stops.
// checked says the conversation was already measured against the
// compaction limit, as RunWithImages does before it adds the message.
func (a *Agent) loop(ctx context.Context, emit func(any), checked bool) error {
	stopBlocks := 0    // Stop hook continuations in this turn
	compacted := false // context-pressure recovery, once per turn
	a.stopReq.Store(false)

	for step := 1; ; step++ {
		if a.MaxSteps > 0 && step > a.MaxSteps {
			return ErrMaxSteps
		}
		// Every request is measured, whatever sent the turn round again: tool
		// results, a steer, a Stop hook's reason. The last reply may have
		// taken the context past the limit.
		if step > 1 || !checked {
			if a.needsCompact() {
				if err := a.compact(ctx, emit, true); err != nil {
					return err
				}
			}
		}
		res, drafts, thinkMs, err := a.streamStep(ctx, emit, &compacted)

		if err != nil {
			// Keep partial text so the transcript matches what the user saw,
			// but drop half-formed tool calls.
			drafts.endAll()
			if res.Message.Content != "" && !a.DiscardPartial.Load() {
				res.Message.ToolCalls = nil
				a.appendMessage(res.Message, session.Entry{ThinkingMs: thinkMs})
				a.messageSaved(res.Message, emit)
			}
			return err
		}
		usage := res.Usage
		a.appendMessage(res.Message, session.Entry{Usage: &usage, ThinkingMs: thinkMs})
		a.messageSaved(res.Message, emit)
		if usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
			a.LastUsage, a.sinceUsage = usage, 0
		}
		emit(StepEnd{Usage: usage, Context: a.ContextTokens()})

		if len(res.Message.ToolCalls) == 0 {
			if a.stopReq.Swap(false) {
				return nil
			}
			a.atStop.Store(true)
			a.runBoundary()
			a.atStop.Store(false)
			if a.commitSteers(emit) {
				continue
			}
			if a.Hooks != nil {
				// An interrupted turn does not run Stop hooks, as in Claude Code.
				if err := ctx.Err(); err != nil {
					return err
				}
				// A Stop hook may block stopping and give the model a reason
				// to keep working. stop_hook_active tells the hook it already
				// did; after MaxStopBlocks the turn ends anyway.
				o := a.Hooks.Stop(ctx, stopBlocks > 0)
				if o.Block && !o.Stop && stopBlocks >= MaxStopBlocks {
					o.Block = false
					o.Notices = append(o.Notices, fmt.Sprintf("Stop hooks blocked %d times in a row; stopping anyway", MaxStopBlocks))
				}
				emitHook(emit, "Stop", o)
				if o.Block && !o.Stop {
					stopBlocks++
					a.appendMessage(provider.Message{Role: "user", Content: StopHookPrefix + o.Reason}, session.Entry{})
					continue
				}
			}
			return nil
		}
		stopTurn := false
		truncatedTools := res.FinishReason == "length"
		for i, tc := range res.Message.ToolCalls {
			var content string
			var imgs []provider.Image
			var meta session.Entry
			if ctx.Err() != nil {
				content = "[canceled by user]"
				meta.Tool = &session.ToolMeta{Canceled: true, ExitCode: -1}
				drafts.end(i, "")
			} else if truncatedTools {
				// A length stop may cut a tool call at any byte. Its arguments can
				// still happen to be valid JSON, so never execute a call from the
				// truncated assistant message; let the model issue it again.
				msg := "tool call was not executed: the response hit the output token limit; re-issue the tool call with complete arguments, and keep it shorter (split a long command or file write into several calls)"
				content = "error: " + msg
				meta.Tool = &session.ToolMeta{ExitCode: -1}
				drafts.end(i, msg)
			} else {
				var stop bool
				content, imgs, meta.Tool, stop = a.runTool(ctx, tc, i, drafts, emit)
				stopTurn = stopTurn || stop
			}
			a.appendMessage(provider.Message{Role: "tool", ToolCallID: tc.ID, Content: content, Images: imgs}, meta)
		}
		drafts.endAll() // drafts beyond the calls the response ended up with
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if stopTurn {
			return ErrStoppedByHook
		}
		if a.stopReq.Swap(false) {
			return nil
		}
		a.runBoundary()
		a.commitSteers(emit)
	}
}
