package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/clipboard"
	"github.com/sebastianrcnt/atto/tui"
)

// copyEnv is what copying needs from outside: tests replace every field
// so no real clipboard or tmux is touched.
type copyEnv struct {
	getenv    func(string) string
	clipboard func(text string) error // the OS clipboard tool (package clipboard)
	primary   func(text string) error // the X11/Wayland PRIMARY selection
	// tmux runs tmux with stdin, e.g. load-buffer.
	tmux func(stdin string, args ...string) error
}

func systemCopyEnv() copyEnv {
	cb := clipboard.System()
	return copyEnv{
		getenv:    os.Getenv,
		clipboard: cb.WriteText,
		primary:   cb.WritePrimary,
		tmux: func(stdin string, args ...string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "tmux", args...)
			cmd.Stdin = strings.NewReader(stdin)
			return cmd.Run()
		},
	}
}

// copyResult says which ways the text went. OSC 52 is always sent.
type copyResult struct {
	n                        int // characters
	ssh                      bool
	clipboard, primary, tmux bool
	clipErr                  error
}

// run copies text every way that applies, as Claude Code does: the OS
// clipboard tool when atto runs locally (and PRIMARY on Linux, so a middle
// click pastes it too), and the tmux paste buffer inside tmux. Over SSH the
// OS tool would fill the remote machine's clipboard, so only OSC 52 (sent
// by the caller) reaches the user's. It blocks on the commands.
func (e copyEnv) run(text string) copyResult {
	r := copyResult{n: len([]rune(text)), ssh: e.getenv("SSH_CONNECTION") != "" || e.getenv("SSH_TTY") != ""}
	if !r.ssh {
		r.clipErr = e.clipboard(text)
		r.clipboard = r.clipErr == nil
		r.primary = e.primary != nil && e.primary(text) == nil
	}
	if e.getenv("TMUX") != "" {
		// -w also sets the outer terminal's clipboard (tmux 3.2+); older
		// tmux rejects the flag.
		err := e.tmux(text, "load-buffer", "-w", "-")
		if err != nil {
			err = e.tmux(text, "load-buffer", "-")
		}
		r.tmux = err == nil
	}
	return r
}

// note is the toast for a copy, naming the ways it went.
func (r copyResult) note() string {
	chars := fmt.Sprintf("%d char%s", r.n, plural(r.n))
	var ways []string
	if r.clipboard {
		ways = append(ways, "clipboard")
	}
	if r.primary {
		ways = append(ways, "primary")
	}
	if r.tmux {
		ways = append(ways, "tmux buffer")
	}
	switch {
	case r.clipboard:
		return "Copied " + chars + " (" + strings.Join(ways, " + ") + ")"
	case r.tmux:
		return "Copied " + chars + " (tmux buffer + OSC 52)"
	case r.clipErr != nil && r.clipErr != clipboard.ErrNoCopyTool:
		return "Sent " + chars + " via OSC 52; the system clipboard failed: " + r.clipErr.Error()
	}
	return "Sent " + chars + " via OSC 52; check your terminal's clipboard settings if paste fails"
}

// copyText puts text on the clipboard. The OSC 52 sequence goes out at
// once, from the UI goroutine so it can't split a frame; the clipboard
// commands run in the background, and done gets the note when they have
// finished. Call it from the UI goroutine.
func (a *App) copyText(text string, done func(note string)) {
	a.ui.WriteRaw(tui.OSC52(text))
	env := a.copyEnv
	if env.getenv == nil {
		env = systemCopyEnv()
	}
	go func() {
		note := env.run(text).note()
		a.ui.Do(func() { done(note) })
	}()
}

// copySelection copies text selected with the mouse (tui.TUI.OnCopy).
func (a *App) copySelection(text string) { a.copyText(text, a.showToast) }

// cmdCopy copies the last answer as markdown.
func (a *App) cmdCopy(string) {
	text := lastAnswer(a.ui.Body.Children)
	if text == "" {
		a.notice("Nothing to copy yet.")
		return
	}
	a.copyText(text, func(note string) { a.showToast("Last answer: " + note) })
}

// lastAnswer is the text of the last assistant answer in the transcript.
func lastAnswer(children []tui.Component) string {
	for _, c := range slices.Backward(children) {
		if g, ok := c.(gap); ok { // a.add wraps blocks in a gap
			c = g.Component
		}
		if t, ok := c.(*textBlock); ok {
			if text := strings.TrimSpace(t.text.String()); text != "" {
				return text
			}
		}
	}
	return ""
}

// toastFor is how long a toast stays.
const toastFor = 4 * time.Second

// toast is a short-lived note at the right end of the status line, for
// confirmations that shouldn't pile up in the transcript (a copy). It
// takes no row of its own, so nothing moves when it comes and goes. A new
// one replaces the old.
type toast struct {
	text  string
	until time.Time
}

func (a *App) showToast(text string) {
	a.toast = toast{text, time.Now().Add(toastFor)}
	time.AfterFunc(toastFor+50*time.Millisecond, a.ui.RequestRender)
}

// withToast puts the toast at the right end of line, cutting line short
// when both don't fit.
func (a *App) withToast(line string, width int) string {
	if a.toast.text == "" || time.Now().After(a.toast.until) {
		return line
	}
	t := tui.Truncate(a.toast.text, max(1, width-2), "…")
	room := width - tui.VisibleWidth(t) - 1
	if tui.VisibleWidth(line) > room-1 {
		line = tui.Truncate(line, max(0, room-1), "…")
	}
	return line + strings.Repeat(" ", max(1, room-tui.VisibleWidth(line))) + tui.Dim(t)
}

// tmuxMouseHint is shown at startup inside tmux when its mouse option is
// off: tmux then keeps mouse events from atto, so neither the wheel nor
// selection works.
const tmuxMouseHint = "tmux has mouse off, so scrolling and selecting with the mouse don't reach atto. Add `set -g mouse on` to ~/.tmux.conf (or run `tmux set -g mouse on`)."

// tmuxMouseOff reports whether atto runs inside tmux with mouse off. run
// runs tmux and returns its output.
func tmuxMouseOff(getenv func(string) string, run func(args ...string) (string, error)) bool {
	if getenv("TMUX") == "" {
		return false
	}
	out, err := run("show", "-gv", "mouse")
	return err == nil && strings.TrimSpace(out) == "off"
}

func runTmux(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	return string(out), err
}

// mouseDisabled reports whether the user turned mouse capture off, with
// "mouse": false in settings.json or ATTO_NO_MOUSE=1.
func mouseDisabled(setting *bool, getenv func(string) string) bool {
	if v := strings.TrimSpace(getenv("ATTO_NO_MOUSE")); v != "" && v != "0" {
		return true
	}
	return setting != nil && !*setting
}
