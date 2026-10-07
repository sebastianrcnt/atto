package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
)

// Shell commands typed in the prompt, as in pi: "!cmd" runs cmd in the
// working directory and adds the command and its output to the model's
// context; "!!cmd" runs it but keeps it from the model. The runtime runs
// them (server/shell.go); the terminal shows bash mode while one is typed,
// and the command's block.

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

// shellBlock shows a command the user ran: a "!" header in place of a
// tool's description, the command, the last output lines and how it ended.
type shellBlock struct {
	expander
	clickable
	id        string // its item's
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
	b := &shellBlock{id: it.ID, cmd: it.Command, exclude: it.Excluded, start: time.Now(), d: &a.details}
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

// shellPendingContext follows whether a command that finished during a
// run still waits for the run to end before the model gets it.
func (a *App) shellPendingContext(w server.Item) {
	if b := a.shellBlk; b != nil && b.id == w.ID {
		b.queued = w.ContextPending && !w.Excluded
	}
}
