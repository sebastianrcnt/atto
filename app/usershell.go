package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// Shell commands typed in the prompt, as in pi: "!cmd" runs cmd in the
// working directory and adds the command and its output to the model's
// context; "!!cmd" runs it but keeps it from the model. They run at once,
// even during a turn, but a result that arrives during a run joins the
// context only when the run ends (flushShell), so it never lands between
// a tool call and its result. No hooks run for them, as in Claude Code.

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

// shellMode reports what the editor text starts: "" (not a command),
// "!" or "!!". Unlike parseShell it holds from the first "!" on.
func shellMode(text string) string {
	t := strings.TrimLeft(text, " \t")
	switch {
	case strings.HasPrefix(t, "!!"):
		return "!!"
	case strings.HasPrefix(t, "!"):
		return "!"
	}
	return ""
}

// shellRun is the command the user is running.
type shellRun struct{ cancel context.CancelFunc }

// pendingShell is a finished command waiting for the run to end.
type pendingShell struct {
	exec session.BashExec
	blk  *shellBlock
}

// submitShell runs a "!" command. While one runs, another is refused and
// its text stays in the editor.
func (a *App) submitShell(text, cmd string, exclude bool) {
	if a.shell != nil {
		a.notice("A shell command is already running. Wait for it, or press esc to cancel it.")
		a.restoreToEditor([]string{text})
		return
	}
	if a.memory != nil {
		a.memory.Begin()
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &shellRun{cancel: cancel}
	a.shell = run
	a.tr().Event(transcript.ShellStart{Command: cmd, Exclude: exclude})
	go func() { // keep the timer moving
		t := time.NewTicker(a.ui.GlyphInterval())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.ui.RequestRender()
			}
		}
	}()
	go func() {
		x := a.agent.RunUserShell(ctx, cmd, exclude, func(s string) {
			a.ui.Do(func() {
				if a.shell == run {
					a.tr().Event(transcript.ShellOutput{Chunk: s})
				}
			})
		})
		a.ui.Do(func() {
			if a.memory != nil {
				defer a.memory.End()
			}
			cancel()
			if a.shell != run { // dropped with its session
				return
			}
			a.shell = nil
			a.tr().Event(transcript.ShellEnd{Exec: x})
			a.finishShell(x)
		})
	}()
}

// finishShell adds a finished command to the conversation, or holds it
// until the run in progress ends.
func (a *App) finishShell(x session.BashExec) {
	if a.busy {
		if !x.Exclude && a.shellBlk != nil {
			a.shellBlk.queued = true
		}
		a.pendingShell = append(a.pendingShell, pendingShell{x, a.shellBlk})
		return
	}
	a.agent.AddShell(x)
	a.ctxTokens = a.agent.ContextTokens()
	a.statusTrigger()
}

// flushShell adds the commands that finished during a run, now that it is
// over.
func (a *App) flushShell() {
	if len(a.pendingShell) == 0 {
		return
	}
	for _, p := range a.pendingShell {
		a.agent.AddShell(p.exec)
		if p.blk != nil {
			p.blk.queued = false
		}
	}
	a.pendingShell = nil
	a.ctxTokens = a.agent.ContextTokens()
	a.statusTrigger()
}

// cancelShell stops the command being run, if any.
func (a *App) cancelShell() bool {
	if a.shell == nil {
		return false
	}
	a.shell.cancel()
	return true
}

// dropShell stops the command and forgets those waiting: their session is
// being left.
func (a *App) dropShell() {
	if a.shell != nil {
		a.shell.cancel()
		a.shell = nil
	}
	a.pendingShell = nil
}

// shellBlock shows a command the user ran: a "!" header in place of a
// tool's description, the command, the last output lines and how it ended.
type shellBlock struct {
	expander
	clickable
	cmd       string
	exclude   bool
	start     time.Time
	output    strings.Builder
	dropped   int // bytes of output not kept
	done      bool
	canceled  bool
	exit      int
	dur       time.Duration
	truncated bool
	full      string
	queued    bool // finished during a turn: the model gets it when it ends
	cache     tui.RenderCache[shellKey]
}

// shellKey is what a shell block's lines depend on, but for the clock.
type shellKey struct {
	cmd, output, full                                string
	dropped, exit                                    int
	dur                                              time.Duration
	exclude, done, canceled, truncated, queued, open bool
}

func (b *shellBlock) Click(line int) bool {
	if !b.hit(line) {
		return false
	}
	b.toggle()
	return true
}

func (b *shellBlock) status() string {
	switch {
	case !b.done:
		return tui.FG(3, "running") + tui.Dim(" · "+tui.FormatDuration(time.Since(b.start).Truncate(100*time.Millisecond))+" · esc to cancel")
	case b.canceled:
		return tui.FG(1, "✗ canceled")
	case b.exit != 0:
		return tui.FG(1, fmt.Sprintf("✗ exit %d", b.exit)) + tui.Dim(" · "+tui.FormatDuration(b.dur))
	}
	return tui.FG(2, "✓") + " " + tui.FormatDuration(b.dur)
}

// Render caches the block; while the command runs, the header line, which
// shows the time, is made again on every call.
func (b *shellBlock) Render(width int) []string {
	key := shellKey{cmd: b.cmd, output: b.output.String(), full: b.full, dropped: b.dropped, exit: b.exit, dur: b.dur,
		exclude: b.exclude, done: b.done, canceled: b.canceled, truncated: b.truncated, queued: b.queued, open: b.expanded()}
	out := b.cache.Render(width, key, func() []string { return b.render(width) })
	if !b.done {
		out = append([]string{b.head(width)}, out[1:]...)
	}
	return out
}

// first is the command's first line.
func (b *shellBlock) first() string {
	first := strings.TrimSpace(tui.StripControls(b.cmd))
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i] + " …"
	}
	return first
}

// head is the block's first line: the command and its status.
func (b *shellBlock) head(width int) string {
	head := tui.FG(5, tui.Bold("!")) + " " + tui.Bold(b.first())
	note := ""
	switch {
	case b.exclude:
		note = " · not sent to the model"
	case b.queued:
		note = " · sent to the model after this turn"
	}
	line := head + tui.Dim(" · ") + b.status() + tui.Dim(note)
	return tui.Truncate(line, width, tui.Dim("…"))
}

func (b *shellBlock) render(width int) []string {
	cmd := strings.TrimSpace(b.cmd)
	first := b.first()
	out := []string{b.head(width)}

	lines := displayLines(b.output.String())
	long := commandRows(first, width, 2) > 1
	multiLine := strings.Contains(cmd, "\n")
	collapsible := len(lines) > toolPreviewLines || multiLine || long
	expanded := collapsible && b.expanded()
	if expanded {
		for i, l := range strings.Split(cmd, "\n") {
			out = append(out, wrapCommand(l, width, 0, i == 0)...)
		}
	}
	hidden := 0
	if !expanded && len(lines) > toolPreviewLines {
		hidden = len(lines) - toolPreviewLines
		lines = lines[hidden:]
	}
	if expanded && b.dropped > 0 {
		out = append(out, tui.Dim(fmt.Sprintf("    (earlier output not kept: %d bytes)", b.dropped)))
	}
	if len(lines) == 0 && b.done {
		lines = []string{"(no output)"}
	}
	for i, l := range lines {
		prefix := "    "
		if i == 0 {
			prefix = "  └ "
		}
		out = append(out, tui.Truncate(tui.Dim(prefix+l), width, tui.Dim("…")))
	}
	if b.truncated && b.done {
		msg := "    output truncated"
		if b.full != "" {
			msg += "; full output: " + b.full
		}
		out = append(out, tui.Truncate(tui.Dim(msg), width, tui.Dim("…")))
	}
	switch {
	case expanded:
		out = append(out, disclosure(true, 0, ""))
	case hidden > 0:
		out = append(out, disclosure(false, hidden, "lines"))
	case collapsible:
		out = append(out, tui.Dim("    + Show details"))
	}
	return b.clicks(collapsible, out, collapsible)
}

// shellItemStarted, shellItemDelta and shellItemCompleted follow the
// builder's items for commands the user runs (see items.go).
func (a *App) shellItemStarted(it *transcript.Item) {
	b := &shellBlock{cmd: it.Command, exclude: it.Excluded, start: time.Now(), d: &a.details}
	a.shellBlk = b
	a.add(b)
}

func (a *App) shellItemDelta(d string) {
	if b := a.shellBlk; b != nil {
		b.output.WriteString(d)
	}
}

func (a *App) shellItemCompleted(it *transcript.Item) {
	b := a.shellBlk
	if b == nil {
		return
	}
	b.output.Reset()
	b.output.WriteString(it.Output) // as saved
	b.dropped = it.Dropped
	b.done = true
	b.dur = it.Duration
	b.truncated, b.full = it.Truncated, it.FullOutput
	if r := it.Result; r != nil {
		b.canceled, b.exit = r.Canceled, r.ExitCode
	}
}
