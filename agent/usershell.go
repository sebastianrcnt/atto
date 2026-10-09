package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// Commands the user runs from the prompt ("!cmd", "!!cmd", as in pi) use
// the shell, environment and console handling of the model's own tool, and
// the same output cut. Their results join the conversation as user
// messages (AddShell). No hooks run for them.

// RunUserShell runs a command the user typed in the agent's working
// directory and returns it as a session record. The output is cut like a
// tool result (the full text in a file under ~/.atto/outputs); canceling ctx kills the
// command. Safe to call while a turn runs: it touches no conversation
// state.
func (a *Agent) RunUserShell(ctx context.Context, command string, exclude bool, onOutput func(string)) session.BashExec {
	return a.RunUserShellWithBackground(ctx, command, exclude, onOutput, nil)
}

// RunUserShellWithBackground also accepts an explicit request to move the
// command to a job. Cancellation still kills it, as for RunUserShell.
func (a *Agent) RunUserShellWithBackground(ctx context.Context, command string, exclude bool, onOutput func(string), bg <-chan struct{}) session.BashExec {
	a.cfgMu.Lock()
	env := a.env
	a.cfgMu.Unlock()
	// No timeout of its own: the user can cancel. (At the longest a command
	// may run, a shell host moves it to the background, noted below.)
	res := runShell(ctx, a.Shell, a.Cwd, env, BashArgs{Command: command, Timeout: int(MaxBashTimeout.Seconds()), userCommand: true}, onOutput, bg)
	v := res.textView().tidy()
	if res.Err != nil {
		v = v.appendString("\n" + res.Err.Error()).trimNL()
	}
	if res.TimedOut {
		v = v.appendString("\n[timed out after " + res.WaitLimit.String() + "]").trimNL()
	}
	if res.Job > 0 && res.Background == BackgroundUser {
		v = v.appendString(fmt.Sprintf("\n[the user moved this command to the background after %s; it is still running as job %d]", res.Duration.Round(time.Second), res.Job)).trimNL()
	} else if res.Job > 0 {
		v = v.appendString(fmt.Sprintf("\n[still running after %s; moved to the background as job %d]", res.WaitLimit, res.Job)).trimNL()
	}
	b := session.BashExec{Command: command, ExitCode: res.ExitCode, Cancelled: res.Canceled, Exclude: exclude, DurationMs: res.Duration.Milliseconds()}
	out, _, cut := v.cut(int(maxOutputBytes.Load()))
	if cut {
		b.Truncated, b.FullOutputPath = true, res.FullOutput
	} else {
		res.dropUnneededFile()
	}
	b.Output = out
	return b
}

// BashExecutionText is what the model reads of a command the user ran, as
// in pi: the command, its output in a code block, and how it ended.
func BashExecutionText(b session.BashExec) string {
	var s strings.Builder
	s.WriteString("Ran `" + b.Command + "`\n")
	if b.Output != "" {
		s.WriteString("```\n" + b.Output + "\n```")
	} else {
		s.WriteString("(no output)")
	}
	switch {
	case b.Cancelled:
		s.WriteString("\n\n(command cancelled)")
	case b.ExitCode != 0:
		fmt.Fprintf(&s, "\n\nCommand exited with code %d", b.ExitCode)
	}
	if b.Truncated && b.FullOutputPath != "" {
		s.WriteString("\n\n[Output truncated. Full output: " + b.FullOutputPath)
		if strings.HasSuffix(b.FullOutputPath, ".zst") {
			s.WriteString(" (zstd; read it with: " + strings.Replace(readHint, "PATH", b.FullOutputPath, 1) + ")")
		}
		s.WriteString("]")
	}
	return s.String()
}

// BashExecutionMessage is the user message a command the user ran becomes
// in the model's context.
func BashExecutionMessage(b session.BashExec) provider.Message {
	return provider.Message{Role: "user", Content: BashExecutionText(b)}
}

// Messages returns a copy of the conversation as the model gets it.
func (a *Agent) Messages() []provider.Message {
	return append([]provider.Message(nil), a.messages...)
}

// AddShell adds a command the user ran to the conversation and records it.
// An excluded command ("!!") is recorded only. It must not be called while
// a turn runs: it would land between a tool call and its result.
func (a *Agent) AddShell(b session.BashExec) {
	if !b.Exclude {
		m := BashExecutionMessage(b)
		a.messages = append(a.messages, m)
		a.sinceUsage += messageChars(m)
	}
	if a.Record != nil {
		a.Record(session.Entry{Type: session.TypeBashExecution, Bash: &b})
	}
}
