package server

import (
	"context"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/session"
)

// Shell commands the user types, as in pi: "!cmd" runs cmd in the working
// directory and adds the command and its output to the model's context;
// "!!cmd" runs it but keeps it from the model. They run at once, even
// during a turn, one at a time, but a result that arrives during a run
// joins the context only when the run ends (flushShell), so it never lands
// between a tool call and its result. No hooks run for them. Interrupting
// one (shell/interrupt, Esc) is separate from interrupting the turn.

// parseShell splits a prompt that is a shell command: the command, and
// whether it is excluded from the context ("!!"). An empty command
// ("!", "!!") is not one: it is sent as a message, as in pi.
func parseShell(text string) (cmd string, exclude, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(text), "!")
	if !found {
		return "", false, false
	}
	if r, ex := strings.CutPrefix(rest, "!"); ex {
		rest, exclude = r, true
	}
	cmd = strings.TrimSpace(rest)
	return cmd, exclude, cmd != ""
}

// shellRun is the command the user is running.
type shellRun struct {
	cancel context.CancelFunc
	item   string // its item's ID
}

// pendingShell is a finished command waiting for the run to end.
type pendingShell struct {
	exec session.BashExec
	item string
}

// startShell runs a "!" command for client. While one runs, another is
// refused and its text goes back.
func (t *thread) startShell(client, text, cmd string, exclude bool) {
	if t.shell != nil {
		t.notice("", "A shell command is already running. Wait for it, or press esc to cancel it.")
		t.recover(client, false, []string{text}, nil)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &shellRun{cancel: cancel}
	t.shell = run
	t.feed(transcript.ShellStart{Command: cmd, Exclude: exclude})
	if open := t.tr.Open(); len(open) > 0 {
		run.item = open[len(open)-1].ID
	}
	t.lastActive = time.Now()
	go func() {
		x := t.agent.RunUserShell(ctx, cmd, exclude, func(s string) {
			t.do(func() {
				if t.shell == run {
					t.feed(transcript.ShellOutput{Chunk: s})
				}
			})
		})
		t.do(func() {
			cancel()
			if t.shell != run { // dropped with its session
				return
			}
			t.shell = nil
			if t.busy && !x.Exclude {
				// The model gets it once the run ends: say so on the item.
				t.pendingShell = append(t.pendingShell, pendingShell{x, run.item})
				t.feed(transcript.ShellEnd{Exec: x})
				return
			}
			t.feed(transcript.ShellEnd{Exec: x})
			t.finishShell(x)
		})
	}()
}

// shellPending reports whether the user's command item id finished during
// a run and waits for it to end.
func (t *thread) shellPending(id string) bool {
	for _, p := range t.pendingShell {
		if p.item == id {
			return true
		}
	}
	return false
}

// finishShell adds a finished command to the conversation.
func (t *thread) finishShell(x session.BashExec) {
	if t.busy {
		t.pendingShell = append(t.pendingShell, pendingShell{exec: x})
		return
	}
	t.agent.AddShell(x)
	t.ctx = t.agent.ContextTokens()
	t.updated()
}

// flushShell adds the commands that finished during a run, now that it is
// over, and tells clients their items are in the context now.
func (t *thread) flushShell() {
	if len(t.pendingShell) == 0 {
		return
	}
	for _, p := range t.pendingShell {
		t.agent.AddShell(p.exec)
		for i := range t.items {
			if t.items[i].ID == p.item && p.item != "" {
				t.items[i].ContextPending = false
				t.publish("item/updated", map[string]any{"item": t.items[i]})
			}
		}
	}
	t.pendingShell = nil
	t.ctx = t.agent.ContextTokens()
}

// cancelShell stops the command being run, if any.
func (t *thread) cancelShell() bool {
	if t.shell == nil {
		return false
	}
	t.shell.cancel()
	return true
}

// dropShell stops the command and forgets those waiting: their session is
// being left.
func (t *thread) dropShell() {
	if t.shell != nil {
		t.shell.cancel()
		t.shell = nil
	}
	t.pendingShell = nil
}
