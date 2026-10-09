//go:build !noext

package extensions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/session"
)

// The Manager is the agent's Extensions.
var _ agent.Extensions = (*Manager)(nil)

// sessionEndWait bounds how long session_end handlers may delay leaving
// a session (and exiting atto).
const sessionEndWait = 2 * time.Second

// object makes a plain JavaScript object of fields. On the loop.
func (e *ext) object(fields map[string]any) *goja.Object {
	o := e.vm.NewObject()
	for k, v := range fields {
		_ = o.Set(k, v)
	}
	return o
}

// has reports whether e handles event.
func (e *ext) has(event string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Contains(e.events, event)
}

// handle calls event's handlers of e in turn, each with the payload
// fields() makes then (so it sees what earlier ones changed), and passes
// what each returns to each; errors and timeouts become notices.
func (e *ext) handle(ctx context.Context, event string, fields func() map[string]any, each func(v any), notices *[]string) {
	if !e.has(event) {
		return
	}
	for i := 0; ; i++ {
		var more atomic.Bool
		v, err := e.await(ctx, e.m.timeout(), func() (goja.Value, error) {
			hs := e.handlers[event]
			if i >= len(hs) {
				return goja.Undefined(), nil
			}
			more.Store(i+1 < len(hs))
			return hs[i](goja.Undefined(), e.object(fields()), e.ctxObj)
		})
		switch {
		case errors.Is(err, errStopped), errors.Is(err, context.Canceled):
			return
		case err != nil:
			msg := err.Error()
			if _, ok := err.(errTimeout); !ok {
				msg = jsError(err)
			}
			e.m.log(e.spec.Name, event+": "+msg)
			*notices = append(*notices, fmt.Sprintf("%s: %s handler: %s", e.spec.Name, event, msg))
		default:
			each(v)
		}
		if !more.Load() {
			return
		}
	}
}

// fire calls event's handlers of every extension without waiting.
func (m *Manager) fire(event string, fields map[string]any) {
	for _, e := range m.running() {
		if !e.has(event) {
			continue
		}
		e.post(func() {
			for _, h := range e.handlers[event] {
				e.call(event, h, e.object(fields), e.ctxObj)
			}
		})
	}
}

// fireWait is fire that waits, up to d in all, for the handlers (and the
// promises they return).
func (m *Manager) fireWait(event string, fields map[string]any, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var notices []string
	for _, e := range m.running() {
		e.handle(ctx, event, func() map[string]any { return fields }, func(any) {}, &notices)
	}
	for _, n := range notices {
		m.host().Notify("atto", n, "error")
	}
}

// SessionStart fires session_start; reason is "startup", "resume",
// "clear", "reload" or "other".
func (m *Manager) SessionStart(reason string) {
	m.fire("session_start", map[string]any{"reason": reason})
}

// SessionEnd fires session_end and waits briefly for the handlers;
// reason is "exit", "clear", "resume" or "other".
func (m *Manager) SessionEnd(reason string) {
	m.endNative()
	m.fireWait("session_end", map[string]any{"reason": reason}, sessionEndWait)
	for _, e := range m.running() { // model calls for the session that ended
		e.cancelRequests()
	}
}

// BlockEnd fires message_end (an assistant text block) or reasoning_end
// (a reasoning block) for kind session.BlockText or session.BlockReasoning.
// Like turn_start it never waits: the handlers run on the extensions' own
// goroutines, and a slow one (a model call, a sleep) delays nothing.
func (m *Manager) BlockEnd(kind, id, text, model string) {
	event := "message_end"
	if kind == session.BlockReasoning {
		event = "reasoning_end"
	}
	m.fire(event, map[string]any{"blockId": id, "text": text, "model": model})
}

// StepEnd fires step_end after each model response, without waiting:
// usage and timing for a tokens-per-second display.
func (m *Manager) StepEnd(e agent.StepEnd, model string) {
	m.fire("step_end", map[string]any{
		"model": model, "promptTokens": e.Usage.PromptTokens, "cachedTokens": e.Usage.CachedTokens,
		"outputTokens": e.Usage.CompletionTokens, "cost": e.Usage.Cost, "contextTokens": e.Context,
		"ttftMs": e.TTFT.Milliseconds(), "genMs": e.Generation.Milliseconds(),
	})
}

func (m *Manager) TurnStart(prompt string) {
	m.fire("turn_start", map[string]any{"prompt": prompt})
}

func (m *Manager) TurnEnd(err error) {
	fields := map[string]any{"error": nil, "aborted": errors.Is(err, context.Canceled)}
	if err != nil {
		fields["error"] = err.Error()
	}
	m.fire("turn_end", fields)
}

func (m *Manager) UserPrompt(ctx context.Context, prompt string) agent.HookOutcome {
	var o agent.HookOutcome
	var extra []string
	for _, e := range m.running() {
		e.handle(ctx, "user_prompt", func() map[string]any { return map[string]any{"prompt": prompt} }, func(v any) {
			switch r := v.(type) {
			case string:
				if strings.TrimSpace(r) != "" {
					extra = append(extra, r)
				}
			case map[string]any:
				if c, ok := r["context"].(string); ok && strings.TrimSpace(c) != "" {
					extra = append(extra, c)
				}
				if b, _ := r["block"].(bool); b && !o.Block {
					o.Block = true
					o.Reason = e.spec.Name + ": " + orDefault(r["reason"], "blocked")
				}
			}
		}, &o.Notices)
		if o.Block {
			break
		}
	}
	o.Context = strings.Join(extra, "\n")
	return o
}

func (m *Manager) ToolCall(ctx context.Context, args agent.BashArgs) (agent.BashArgs, agent.HookOutcome) {
	var o agent.HookOutcome
	tool := ""
	if m.ag != nil {
		tool = m.ag.Shell.ToolName()
	}
	for _, e := range m.running() {
		e.handle(ctx, "tool_call", func() map[string]any {
			return map[string]any{
				"toolName": tool, "command": args.Command, "description": args.Description,
				"timeout": args.Timeout, "background": args.Background,
			}
		}, func(v any) {
			r, ok := v.(map[string]any)
			if !ok || o.Block {
				return
			}
			if b, _ := r["block"].(bool); b {
				o.Block = true
				o.Reason = e.spec.Name + ": " + orDefault(r["reason"], "blocked")
				return
			}
			if c, ok := r["command"].(string); ok && strings.TrimSpace(c) != "" && c != args.Command {
				args.Command = c
				o.Notices = append(o.Notices, e.spec.Name+": rewrote the command")
			}
		}, &o.Notices)
		if o.Block {
			break
		}
	}
	return args, o
}

func (m *Manager) ToolResult(ctx context.Context, args agent.BashArgs, res agent.BashResult, output string) (string, agent.HookOutcome) {
	var o agent.HookOutcome
	tool := ""
	if m.ag != nil {
		tool = m.ag.Shell.ToolName()
	}
	for _, e := range m.running() {
		e.handle(ctx, "tool_result", func() map[string]any {
			return map[string]any{
				"toolName": tool, "command": args.Command, "description": args.Description,
				"output": output, "exitCode": res.ExitCode, "timedOut": res.TimedOut, "canceled": res.Canceled,
				"durationMs": res.Duration.Milliseconds(), "job": res.Job,
			}
		}, func(v any) {
			switch r := v.(type) {
			case string:
				output = r
			case map[string]any:
				if s, ok := r["output"].(string); ok {
					output = s
				}
			}
		}, &o.Notices)
	}
	return output, o
}
