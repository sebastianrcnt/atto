package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/prompts"
	"github.com/sebastianrcnt/atto/shell"
)

const (
	DefaultBashTimeout = 60 * time.Second
	MaxBashTimeout     = 30 * time.Minute

	// DefaultToolOutputTokens is how much output goes back to the model
	// unless settings.json says otherwise (SetToolOutputTokenLimit).
	DefaultToolOutputTokens = 10_000
	// Hard cap on what is buffered in memory per command.
	maxCaptureBytes = 8 * 1024 * 1024
)

// maxOutputBytes is what goes back to the model, as in codex: about
// DefaultToolOutputTokens tokens (4 bytes per token), cut from the middle so
// both the start (the first error) and the end (the summary) survive. The
// full output is saved to a file.
var maxOutputBytes atomic.Int64

func init() { SetToolOutputTokenLimit(0) }

// SetToolOutputTokenLimit sets the output budget in tokens; n <= 0 is the
// default.
func SetToolOutputTokenLimit(n int) {
	if n <= 0 {
		n = DefaultToolOutputTokens
	}
	maxOutputBytes.Store(int64(n) * 4)
}

var bashSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "description": {
      "type": "string",
      "description": "What this command does, in a few words, shown to the user. E.g. \"JIT compile atto.py\", \"Run unit tests\", \"Read main.go\"."
    },
    "command": {
      "type": "string",
      "description": "The bash command to run."
    },
    "timeout": {
      "type": "integer",
      "description": "Timeout in seconds. Default 60. Raise it for long builds or test suites."
    },
    "run_in_background": {
      "type": "boolean",
      "description": "Start the command as a background job and return at once with its id. For dev servers, watchers, long builds."
    }
  },
  "required": ["description", "command"]
}`)

// toolDescription describes the shell tool for the model.
func toolDescription(sh shell.Shell) string {
	return prompts.Render("bash_tool", map[string]any{"Kind": string(sh.Kind)})
}

type BashArgs struct {
	Description string `json:"description"`
	Command     string `json:"command"`
	Timeout     int    `json:"timeout,omitempty"`
	// Background starts the command as a job (atto job start). Named as
	// in Claude Code's Bash tool, like the other parameters, so models
	// trained on it use it without being told.
	Background bool `json:"run_in_background,omitempty"`
}

// TimeLimit is how long the command may run.
func (a BashArgs) TimeLimit() time.Duration { return a.timeout() }

func (a BashArgs) timeout() time.Duration {
	if a.Timeout <= 0 {
		return DefaultBashTimeout
	}
	return min(time.Duration(a.Timeout)*time.Second, MaxBashTimeout)
}

type BashResult struct {
	Output   string // raw combined output (possibly capped)
	ExitCode int
	TimedOut bool
	Canceled bool
	Duration time.Duration
	Err      error // failure to start, etc.

	// Job is the background job the command became (Background says
	// why); it is still running. Output is what it wrote until then.
	Job        int
	Background string
	// Note says why a command could not move to the background.
	Note string
}

// Why a command runs in the background.
const (
	BackgroundRequested = "requested" // run_in_background
	BackgroundTimeout   = "timeout"   // still running at its timeout
	BackgroundUser      = "user"      // Ctrl+B (Agent.Background)
)

// ShellHost makes commands run under an atto shell host (jobs.StartHost),
// which can turn a running command into a background job. It re-executes
// os.Executable, so it is on only in the atto binary (and in tests whose
// binary serves `_shell`); otherwise commands run directly and are killed
// at their timeout.
var ShellHost bool

// tailLines is how much of the output so far a command that moved to the
// background reports; the rest is in its job log.
const tailLines = 40

// streamWriter collects output and forwards chunks to a callback.
type streamWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	dropped  int
	onOutput func(string)
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if room := maxCaptureBytes - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(len(p), room)])
		w.dropped += max(0, len(p)-room)
	} else {
		w.dropped += len(p)
	}
	w.mu.Unlock()
	if w.onOutput != nil {
		w.onOutput(string(p))
	}
	return len(p), nil
}

// RunBash runs args.Command with the default shell (bash on Unix,
// PowerShell on Windows).
func RunBash(ctx context.Context, cwd string, env []string, args BashArgs, onOutput func(string)) BashResult {
	return RunShell(ctx, shell.Default(), cwd, env, args, onOutput)
}

// RunShell executes args.Command with sh in cwd. The whole process tree
// (process group on Unix, job object on Windows) is killed when ctx is
// canceled. At the timeout the command moves to the background as a job
// of the session in env (ATTO_SESSION_ID) when it runs under a shell
// host; otherwise it is killed.
func RunShell(ctx context.Context, sh shell.Shell, cwd string, env []string, args BashArgs, onOutput func(string)) BashResult {
	return runShell(ctx, sh, cwd, env, args, onOutput, nil)
}

// runShell is RunShell with a channel that moves the command to the
// background on request.
func runShell(ctx context.Context, sh shell.Shell, cwd string, env []string, args BashArgs, onOutput func(string), bg <-chan struct{}) BashResult {
	full := append(append(os.Environ(), "TERM=dumb", "PAGER=cat", "GIT_PAGER=cat", "NO_COLOR=1"), env...)
	session := envValue(env, "ATTO_SESSION_ID")
	if args.Background {
		return startJob(session, cwd, full, args)
	}
	if ShellHost {
		if res, ok := runHosted(ctx, sh, cwd, full, session, args, onOutput, bg); ok {
			return res
		}
	}
	return runDirect(ctx, sh, cwd, full, args, onOutput)
}

// envValue is the last value of key in env.
func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

// startJob runs a command with run_in_background, as atto job start does.
func startJob(session, cwd string, env []string, args BashArgs) BashResult {
	start := time.Now()
	if session == "" {
		return BashResult{Err: errors.New("run_in_background needs a session (ATTO_SESSION_ID)"), ExitCode: -1}
	}
	j, err := jobs.StartEnv(session, cwd, args.Description, args.Command, env)
	if err != nil {
		return BashResult{Err: err, ExitCode: -1, Duration: time.Since(start)}
	}
	return BashResult{Job: j.ID, Background: BackgroundRequested, Duration: time.Since(start)}
}

// runHosted runs the command under a shell host. ok is false when the
// host could not start (and so neither did the command).
func runHosted(ctx context.Context, sh shell.Shell, cwd string, env []string, session string, args BashArgs, onOutput func(string), bg <-chan struct{}) (BashResult, bool) {
	start := time.Now()
	w := &streamWriter{onOutput: onOutput}
	h, err := jobs.StartHost(sh, cwd, env, args.Command, w)
	if err != nil {
		var se *jobs.StartError
		if errors.As(err, &se) {
			return BashResult{Err: se, ExitCode: -1, Duration: time.Since(start)}, true
		}
		return BashResult{}, false
	}
	timer := time.NewTimer(args.timeout())
	defer timer.Stop()

	var res BashResult
	done := ctx.Done()
	detaching := "" // why a detach is pending
	exited := false
	// timedOut kills a command that could not move to the background.
	timedOut := func(why string) {
		res.TimedOut, res.Note = true, why
		h.Kill()
	}
	detach := func(why string) {
		switch {
		case detaching != "" || res.Canceled || res.TimedOut:
		case session == "" && why == BackgroundTimeout:
			timedOut("")
		case session == "":
		case h.Detach(session, args.Description) != nil:
			if why == BackgroundTimeout {
				timedOut("")
			}
		default:
			detaching = why
		}
	}
wait:
	for {
		select {
		case st, ok := <-h.Status():
			switch {
			case !ok:
				if !exited && res.Job == 0 && !res.Canceled && !res.TimedOut {
					res.Err = errors.New("the shell host exited unexpectedly")
				}
				break wait
			case st.Exit != nil:
				res.ExitCode, exited = *st.Exit, true
			case st.Job > 0:
				res.Job, res.Background = st.Job, detaching
				if res.Canceled {
					h.Kill() // canceled while detaching: the job goes too
				}
			case st.DetachError != "":
				why := detaching
				detaching = ""
				if why == BackgroundTimeout {
					timedOut(st.DetachError)
				} else if onOutput != nil {
					// For the user, who asked; the model need not know.
					onOutput("\n[atto: could not move to the background: " + st.DetachError + "]\n")
				}
			}
		case <-done:
			done = nil
			res.Canceled = true
			h.Kill()
		case <-timer.C:
			detach(BackgroundTimeout)
		case <-bg:
			detach(BackgroundUser)
		}
	}
	if res.Job > 0 && !res.Canceled {
		h.Release()
	} else {
		_ = h.Wait()
	}
	res.Duration = time.Since(start)
	w.mu.Lock()
	res.Output = w.buf.String()
	if w.dropped > 0 {
		res.Output += fmt.Sprintf("\n[%d bytes of output dropped]", w.dropped)
	}
	w.mu.Unlock()
	if res.Canceled || res.TimedOut || res.Err != nil {
		res.ExitCode, res.Job, res.Background = -1, 0, ""
	}
	return res, true
}

// runDirect runs the command as a child of atto, killed at its timeout.
func runDirect(ctx context.Context, sh shell.Shell, cwd string, env []string, args BashArgs, onOutput func(string)) BashResult {
	start := time.Now()
	tctx, cancel := context.WithTimeout(ctx, args.timeout())
	defer cancel()

	cmd := sh.Command(tctx, args.Command)
	cmd.Dir = cwd
	cmd.Env = env
	tree := shell.NewTree(cmd)
	defer tree.Close()
	cmd.WaitDelay = 2 * time.Second // don't hang on pipes held by orphaned children
	w := &streamWriter{onOutput: onOutput}
	cmd.Stdout, cmd.Stderr = w, w

	err := cmd.Start()
	if err == nil {
		tree.Started()
		err = cmd.Wait()
	}
	res := BashResult{Duration: time.Since(start)}
	w.mu.Lock()
	res.Output = w.buf.String()
	if w.dropped > 0 {
		res.Output += fmt.Sprintf("\n[%d bytes of output dropped]", w.dropped)
	}
	w.mu.Unlock()

	switch {
	case ctx.Err() != nil:
		res.Canceled = true
		res.ExitCode = -1
	case errors.Is(tctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.ExitCode = -1
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else if !errors.Is(err, exec.ErrWaitDelay) {
			res.Err = err
			res.ExitCode = -1
		}
	}
	return res
}

// ForModel formats the result as the tool message content.
func (r BashResult) ForModel(args BashArgs) string {
	if r.Err != nil {
		return "error: " + r.Err.Error()
	}
	if r.Job > 0 {
		return r.backgroundForModel(args)
	}
	out := truncateMiddle(tidy(r.Output))
	var b strings.Builder
	b.WriteString(out)
	if out != "" && !strings.HasSuffix(out, "\n") {
		b.WriteString("\n")
	}
	switch {
	case r.Canceled:
		b.WriteString("[canceled by user]")
	case r.TimedOut && r.Note != "":
		fmt.Fprintf(&b, "[timed out after %s and killed: it could not move to the background (%s)]", args.timeout(), r.Note)
	case r.TimedOut:
		fmt.Fprintf(&b, "[timed out after %s; pass a larger timeout if the command needs more time]", args.timeout())
	case r.ExitCode != 0:
		fmt.Fprintf(&b, "[exit code %d]", r.ExitCode)
	case out == "":
		b.WriteString("[no output]")
	}
	return strings.TrimRight(b.String(), "\n")
}

// backgroundForModel reports a command that runs on as a job: the tail
// of its output so far, then one status line (which replays recognize,
// see BackgroundStatus).
func (r BashResult) backgroundForModel(args BashArgs) string {
	var b strings.Builder
	lines := strings.Split(tidy(r.Output), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	shown := ""
	if len(lines) > tailLines {
		shown = fmt.Sprintf(" (above: the last %d of %d lines so far)", tailLines, len(lines))
		lines = lines[len(lines)-tailLines:]
	}
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	switch r.Background {
	case BackgroundRequested:
		fmt.Fprintf(&b, "[started in the background as job %d.", r.Job)
	case BackgroundUser:
		fmt.Fprintf(&b, "[the user moved this command to the background after %s; it is still running as job %d%s.", r.Duration.Round(time.Second), r.Job, shown)
	default:
		fmt.Fprintf(&b, "[still running after %s; moved to the background as job %d%s.", args.timeout(), r.Job, shown)
	}
	fmt.Fprintf(&b, " You will get an [atto event] when it exits. Output: atto job output %d · stop: atto job kill %d]", r.Job, r.Job)
	return b.String()
}

// BackgroundStatus reports whether line is the status line that ends
// the result of a command that became job.
func BackgroundStatus(line string, job int) bool {
	return strings.HasPrefix(line, "[") && strings.Contains(line, fmt.Sprintf(" as job %d", job)) &&
		strings.HasSuffix(line, fmt.Sprintf("stop: atto job kill %d]", job))
}

// tidy normalizes output for the model: CRLF to LF, and no trailing
// whitespace-only lines (PowerShell pads tables with them).
func tidy(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// truncateMiddle keeps the first and last maxOutputBytes/2 bytes (on line
// boundaries) and saves the full text to a temp file when it had to cut.
func truncateMiddle(s string) string {
	body, note, path, cut := cutMiddle(s)
	if !cut {
		return s
	}
	if path != "" {
		note += "; full output: " + path
	}
	return note + "]\n" + body
}

// cutMiddle is truncateMiddle in parts: the text with its middle
// replaced by a marker, the start of the note saying so (no closing
// bracket) and the file holding the full text ("" if it could not be
// written). cut is false when s was short enough to keep whole.
func cutMiddle(s string) (body, note, path string, cut bool) {
	limit := int(maxOutputBytes.Load())
	if len(s) <= limit {
		return s, "", "", false
	}
	half := limit / 2
	head := s[:half]
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i]
	}
	tail := s[len(s)-half:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	total := strings.Count(s, "\n") + 1
	omitted := total - (strings.Count(head, "\n") + 1) - (strings.Count(tail, "\n") + 1)
	note = fmt.Sprintf("[output truncated: %d lines, ~%d tokens; showing the start and the end", total, len(s)/4)
	if f, err := os.CreateTemp("", "atto-bash-*.log"); err == nil {
		_, _ = f.WriteString(s)
		f.Close()
		path = f.Name()
	}
	return fmt.Sprintf("%s\n[… %d lines omitted …]\n%s", head, max(omitted, 0), tail), note, path, true
}
