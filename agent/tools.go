package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

var shellToolNames = map[string]bool{"bash": true, "powershell": true, "shell": true, "cmd": true}

// runTool runs one call and returns its result for the model: the text,
// the images atto view attached, how it ran, and whether a hook stopped
// the turn.
func (a *Agent) runTool(ctx context.Context, tc provider.ToolCall, index int, drafts *draftTracker, emit func(any)) (string, []provider.Image, *session.ToolMeta, bool) {
	fail := func(msg string) (string, []provider.Image, *session.ToolMeta, bool) {
		drafts.end(index, msg)
		return "error: " + msg, nil, &session.ToolMeta{ExitCode: -1}, false
	}
	// Accept any shell tool name: a session started on another OS, or a
	// model calling "bash" out of habit, still runs in this machine's shell.
	if name := a.Shell.ToolName(); tc.Function.Name != name && !shellToolNames[tc.Function.Name] {
		return fail(fmt.Sprintf("unknown tool %q; the only tool is %s", tc.Function.Name, name))
	}
	var args BashArgs
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return fail("invalid arguments: " + err.Error())
	}
	if strings.TrimSpace(args.Command) == "" {
		return fail("command is empty")
	}
	if args.Description == "" {
		args.Description = FirstLine(args.Command)
	}
	if a.Hooks != nil {
		updated, o := a.Hooks.PreToolUse(ctx, args)
		emitHook(emit, "PreToolUse", o)
		if o.Stop {
			drafts.end(index, "stopped by hook")
			return "[stopped by hook: " + o.StopReason + "]", nil, &session.ToolMeta{Description: args.Description, ExitCode: -1}, true
		}
		if o.Block {
			drafts.end(index, "blocked by hook")
			return "Blocked by a PreToolUse hook: " + o.Reason, nil, &session.ToolMeta{Description: args.Description, ExitCode: -1}, false
		}
		args = updated
	}
	if a.Extensions != nil {
		updated, o := a.Extensions.ToolCall(ctx, args)
		emitHook(emit, ExtensionEvent+"tool_call", o)
		if o.Block {
			drafts.end(index, "blocked by extension")
			return "Blocked by an extension: " + o.Reason, nil, &session.ToolMeta{Description: args.Description, ExitCode: -1}, false
		}
		args = updated
	}
	a.cfgMu.Lock()
	env := a.env
	a.cfgMu.Unlock()
	emit(ToolStart{ID: tc.ID, Index: index, Args: args, Timeout: args.waitLimit(envValue(env, "ATTO_SESSION_ID"))})
	drafts.claim(index)
	// A foreground call gets a directory for atto view, whose images are
	// attached to its result. Other commands get none (the variable is
	// cleared, in case atto's own environment has one).
	viewDir := ""
	if !args.Background {
		if d, err := os.MkdirTemp("", "atto-view-"); err == nil {
			viewDir = d
			defer os.RemoveAll(d)
		}
	}
	env = append(slices.Clip(env), config.EnvView+"="+viewDir, config.EnvToolCallID+"="+tc.ID)
	var bg chan struct{}
	if ShellHost && !args.Background && envValue(env, "ATTO_SESSION_ID") != "" {
		bg = make(chan struct{}, 1)
		a.bgMu.Lock()
		a.bg = bg
		a.bgMu.Unlock()
	}
	args.callID = tc.ID
	res := runShell(ctx, a.Shell, a.Cwd, env, args, func(s string) { emit(ToolOutput{ID: tc.ID, Chunk: s}) }, bg)
	if bg != nil {
		a.bgMu.Lock()
		a.bg = nil
		a.bgMu.Unlock()
	}
	imgs, note := a.viewed(viewDir)
	if note != "" {
		chunk := note + "\n"
		if res.Output != "" && !strings.HasSuffix(res.Output, "\n") {
			chunk = "\n" + chunk
		}
		res.appendOutput(chunk)
		emit(ToolOutput{ID: tc.ID, Chunk: chunk})
	}
	out := res.ForModel(args)
	if a.Extensions != nil {
		var o HookOutcome
		out, o = a.Extensions.ToolResult(ctx, args, res, out)
		emitHook(emit, ExtensionEvent+"tool_result", o)
	}
	emit(ToolEnd{ID: tc.ID, Result: res, Text: out, Images: imgs})
	stop := false
	if a.Hooks != nil {
		o := a.Hooks.PostToolUse(ctx, args, res, out)
		emitHook(emit, "PostToolUse", o)
		if o.Block && o.Reason != "" {
			out += "\n[PostToolUse hook] " + o.Reason
		}
		if o.Context != "" {
			out += "\n[PostToolUse hook] " + o.Context
		}
		stop = o.Stop
	}
	return out, imgs, &session.ToolMeta{
		Description: args.Description,
		ExitCode:    res.ExitCode,
		DurationMs:  res.Duration.Milliseconds(),
		TimedOut:    res.TimedOut,
		Canceled:    res.Canceled,
		Job:         res.Job,
		Background:  res.Background,
	}, stop
}

// ViewUnsupported is added to the result of a command that ran atto view
// when the model takes no image input: nothing is attached.
const ViewUnsupported = "[atto view: this model can't view images, so nothing was attached. Work from text instead, or ask the user to switch to a model with image input.]"

// viewed collects the images atto view left in dir during a call and
// stores them for the session. note, if not empty, is for the model: why
// images were not attached.
func (a *Agent) viewed(dir string) (imgs []provider.Image, note string) {
	if dir == "" {
		return nil, ""
	}
	imgs, err := images.Collect(dir)
	if err != nil {
		note = "[atto view: not attached: " + err.Error() + "]"
	}
	if len(imgs) == 0 {
		return nil, note
	}
	if m, _ := a.Current(); !m.Model.Images() {
		return nil, ViewUnsupported
	}
	for _, im := range imgs {
		// Without its file a resumed session sends a note instead.
		if err := images.Save(im); err != nil {
			if note != "" {
				note += "\n"
			}
			note += fmt.Sprintf("[atto view: %s could not be saved and will not survive resume: %v]", images.ViewLabel(im), err)
		}
	}
	return imgs, note
}

// FirstLine is the first line of a command, the description of a call
// that gave none.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
