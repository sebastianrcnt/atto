// Package hooks runs user-configured hooks in Claude Code's format.
//
// Each hook gets the event as JSON on stdin (command hooks) or as a POST
// body (http hooks). Exit code 0 succeeds; stdout may carry a JSON object.
// Exit code 2 blocks, with stderr as the reason. Other exit codes are
// non-blocking errors shown to the user. Recognized JSON output:
//
//	{"continue": false, "stopReason": "..."}            end the turn
//	{"decision": "block", "reason": "..."}              block (event-specific)
//	{"hookSpecificOutput": {
//	    "permissionDecision": "allow" | "deny" | "ask",  PreToolUse
//	    "permissionDecisionReason": "...",
//	    "updatedInput": {"command": "..."},              PreToolUse
//	    "additionalContext": "..."}}                     UserPromptSubmit, PostToolUse
//
// Stop blocks with {"decision":"block"} or exit code 2: its reason goes
// to the model and the turn continues. SessionEnd and Notification cannot
// block. The matcher of SessionEnd is tested against the reason and the
// one of Notification against the notification type.
//
// atto has no approval prompt, so "ask" is treated as "deny".
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/shell"
)

const (
	defaultTimeout = 60 * time.Second
	// sessionEndTimeout is the default for SessionEnd hooks, which run while
	// atto is exiting; a hook's own "timeout" overrides it.
	sessionEndTimeout = 5 * time.Second
	// toolName is what hooks see for the bash tool; Claude Code calls its
	// shell tool "Bash", so existing matchers and scripts keep working.
	toolName = "Bash"
)

// Runner implements agent.Hooks for one session.
type Runner struct {
	cfg map[string][]config.HookMatcher

	mu         sync.Mutex
	sessionID  string
	transcript string
	cwd        string
}

// New returns a runner, or nil if no hooks are configured (so callers can
// leave agent.Hooks unset).
func New(cfg map[string][]config.HookMatcher, cwd string) *Runner {
	if len(cfg) == 0 {
		return nil
	}
	return &Runner{cfg: cfg, cwd: cwd}
}

// SetSession updates the session fields sent to hooks.
func (r *Runner) SetSession(id, transcriptPath string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.sessionID, r.transcript = id, transcriptPath
	r.mu.Unlock()
}

// output is the JSON a hook may print.
type output struct {
	Continue           *bool  `json:"continue"`
	StopReason         string `json:"stopReason"`
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	SystemMessage      string `json:"systemMessage"`
	HookSpecificOutput struct {
		PermissionDecision       string         `json:"permissionDecision"`
		PermissionDecisionReason string         `json:"permissionDecisionReason"`
		UpdatedInput             map[string]any `json:"updatedInput"`
		AdditionalContext        string         `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// result of one hook invocation.
type result struct {
	out        output
	parsed     bool // stdout was a JSON object
	stdout     string
	blocked    bool   // exit code 2
	stderr     string // reason when blocked
	errorMsg   string // failure reported as a notice
	incomplete bool   // response cannot be trusted for a blocking decision
}

func matches(pattern, tool string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	re, err := regexp.Compile("(?i)^(?:" + pattern + ")$")
	if err != nil {
		return strings.EqualFold(pattern, tool)
	}
	return re.MatchString(tool)
}

// run executes every hook registered for event whose matcher fits tool.
func (r *Runner) run(ctx context.Context, event, tool string, payload map[string]any) []result {
	return r.runTimeout(ctx, event, tool, payload, defaultTimeout)
}

func (r *Runner) runTimeout(ctx context.Context, event, tool string, payload map[string]any, def time.Duration) []result {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	payload["session_id"] = r.sessionID
	payload["transcript_path"] = r.transcript
	payload["cwd"] = r.cwd
	r.mu.Unlock()
	payload["hook_event_name"] = event
	input, _ := json.Marshal(payload)

	var out []result
	for _, m := range r.cfg[event] {
		if tool != "" && !matches(m.Matcher, tool) {
			continue
		}
		for _, h := range m.Hooks {
			out = append(out, r.execTimeout(ctx, h, input, def))
		}
	}
	return out
}

// execTimeout runs one hook; def applies when the hook sets no timeout.
func (r *Runner) execTimeout(ctx context.Context, h config.HookSpec, input []byte, def time.Duration) result {
	timeout := def
	if h.Timeout > 0 {
		timeout = time.Duration(h.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var res result
	switch h.Type {
	case "http":
		req, err := http.NewRequestWithContext(ctx, "POST", h.URL, bytes.NewReader(input))
		if err != nil {
			res.errorMsg = err.Error()
			return res
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range h.Headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			res.errorMsg = err.Error()
			return res
		}
		defer resp.Body.Close()
		const limit = 1 << 20
		body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			res.errorMsg, res.incomplete = fmt.Sprintf("%s: reading response: %v", h.URL, err), true
			return res
		}
		if len(body) > limit {
			res.errorMsg, res.incomplete = fmt.Sprintf("%s: response exceeds 1 MiB", h.URL), true
			return res
		}
		if resp.StatusCode/100 != 2 {
			res.errorMsg = fmt.Sprintf("%s: %s", h.URL, resp.Status)
			return res
		}
		res.stdout = string(body)
	default: // "command"
		cmd := shell.Command(ctx, h.Command) // same shell as the agent: bash, or PowerShell on Windows
		cmd.Dir = r.cwd
		killTreeOnCancel(cmd)
		cmd.WaitDelay = 2 * time.Second // a child that escaped must not hold the output pipes open
		cmd.Stdin = bytes.NewReader(input)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		res.stdout = stdout.String()
		if err != nil {
			var ee *exec.ExitError
			switch {
			case ctx.Err() != nil:
				res.errorMsg = fmt.Sprintf("%q timed out after %s", h.Command, timeout)
			case errors.As(err, &ee) && ee.ExitCode() == 2:
				res.blocked = true
				res.stderr = strings.TrimSpace(stderr.String())
			default:
				msg := strings.TrimSpace(stderr.String())
				if msg == "" {
					msg = err.Error()
				}
				res.errorMsg = fmt.Sprintf("%q failed: %s", h.Command, msg)
			}
			return res
		}
	}
	if s := strings.TrimSpace(res.stdout); strings.HasPrefix(s, "{") {
		res.parsed = json.Unmarshal([]byte(s), &res.out) == nil
	}
	return res
}

// fold combines hook results into one outcome. blockOn lists the JSON
// "decision" values that block for this event.
func fold(results []result, event string) agent.HookOutcome {
	var o agent.HookOutcome
	var ctxParts []string
	for _, r := range results {
		if r.errorMsg != "" {
			o.Notices = append(o.Notices, event+" hook: "+r.errorMsg)
			if r.incomplete && (event == "PreToolUse" || event == "PostToolUse" || event == "UserPromptSubmit" || event == "Stop") {
				o.Block = true
				o.Reason = joinReason(o.Reason, event+" hook: "+r.errorMsg)
			}
			continue
		}
		if r.blocked {
			o.Block = true
			o.Reason = joinReason(o.Reason, orDefault(r.stderr, "blocked by "+event+" hook"))
			continue
		}
		if r.out.SystemMessage != "" {
			o.Notices = append(o.Notices, r.out.SystemMessage)
		}
		if r.out.Continue != nil && !*r.out.Continue {
			o.Stop = true
			o.StopReason = orDefault(r.out.StopReason, event+" hook")
		}
		if r.out.Decision == "block" {
			o.Block = true
			o.Reason = joinReason(o.Reason, orDefault(r.out.Reason, "blocked by "+event+" hook"))
		}
		hs := r.out.HookSpecificOutput
		if d := hs.PermissionDecision; d == "deny" || d == "ask" {
			o.Block = true
			reason := orDefault(hs.PermissionDecisionReason, "denied by "+event+" hook")
			if d == "ask" {
				reason += " (approval prompts are not supported; treated as deny)"
			}
			o.Reason = joinReason(o.Reason, reason)
		}
		if hs.AdditionalContext != "" {
			ctxParts = append(ctxParts, hs.AdditionalContext)
		} else if event == "UserPromptSubmit" && !r.parsed && strings.TrimSpace(r.stdout) != "" {
			// Plain stdout from a UserPromptSubmit hook is added as context.
			ctxParts = append(ctxParts, strings.TrimSpace(r.stdout))
		}
	}
	o.Context = strings.Join(ctxParts, "\n")
	return o
}

func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func toolInput(args agent.BashArgs) map[string]any {
	in := map[string]any{"command": args.Command, "description": args.Description}
	if args.Timeout > 0 {
		in["timeout"] = args.Timeout
	}
	if args.Background {
		in["run_in_background"] = true
	}
	return in
}

func (r *Runner) UserPromptSubmit(ctx context.Context, prompt string) agent.HookOutcome {
	return fold(r.run(ctx, "UserPromptSubmit", "", map[string]any{"prompt": prompt}), "UserPromptSubmit")
}

func (r *Runner) PreToolUse(ctx context.Context, args agent.BashArgs) (agent.BashArgs, agent.HookOutcome) {
	results := r.run(ctx, "PreToolUse", toolName, map[string]any{"tool_name": toolName, "tool_input": toolInput(args)})
	for _, res := range results {
		if u := res.out.HookSpecificOutput.UpdatedInput; u != nil {
			if c, ok := u["command"].(string); ok && c != "" {
				args.Command = c
			}
			if d, ok := u["description"].(string); ok && d != "" {
				args.Description = d
			}
			if t, ok := u["timeout"].(float64); ok && t > 0 {
				args.Timeout = int(t)
			}
			if b, ok := u["run_in_background"].(bool); ok {
				args.Background = b
			}
		}
	}
	return args, fold(results, "PreToolUse")
}

func (r *Runner) PostToolUse(ctx context.Context, args agent.BashArgs, res agent.BashResult, output string) agent.HookOutcome {
	resp := map[string]any{
		"stdout": res.Output, "exit_code": res.ExitCode, "interrupted": res.Canceled || res.TimedOut,
		"output": output,
	}
	if res.Job > 0 {
		resp["background_job"] = res.Job
	}
	return fold(r.run(ctx, "PostToolUse", toolName, map[string]any{
		"tool_name": toolName, "tool_input": toolInput(args), "tool_response": resp,
	}), "PostToolUse")
}

func (r *Runner) Stop(ctx context.Context, active bool) agent.HookOutcome {
	return fold(r.run(ctx, "Stop", "", map[string]any{"stop_hook_active": active}), "Stop")
}

func (r *Runner) PreCompact(ctx context.Context, auto bool) agent.HookOutcome {
	trigger := "manual"
	if auto {
		trigger = "auto"
	}
	o := fold(r.run(ctx, "PreCompact", "", map[string]any{"trigger": trigger}), "PreCompact")
	o.Block = false // PreCompact cannot block, as in Claude Code
	return o
}

// SessionStart runs when a session starts ("startup"), is resumed
// ("resume") or cleared ("clear"). Returned notices are for display.
func (r *Runner) SessionStart(ctx context.Context, source string) []string {
	if r == nil {
		return nil
	}
	return fold(r.run(ctx, "SessionStart", "", map[string]any{"source": source}), "SessionStart").Notices
}

// SessionEnd runs when a session ends. reason is "exit" (atto quits),
// "clear" (/new, /clear), "resume" (switching to another session) or
// "other". It cannot block and runs synchronously with a short default
// timeout, so exiting never waits long. Returned notices are for display.
func (r *Runner) SessionEnd(ctx context.Context, reason string) []string {
	if r == nil {
		return nil
	}
	res := r.runTimeout(ctx, "SessionEnd", reason, map[string]any{"reason": reason}, sessionEndTimeout)
	return fold(res, "SessionEnd").Notices
}

// Notification runs when atto wants the user's attention. kind is the
// notification type ("idle_prompt", "background_event", "goal_blocked"),
// message what to tell the user. It cannot block.
func (r *Runner) Notification(ctx context.Context, kind, message string) []string {
	if r == nil {
		return nil
	}
	res := r.run(ctx, "Notification", kind, map[string]any{"message": message, "notification_type": kind})
	return fold(res, "Notification").Notices
}
