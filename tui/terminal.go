package tui

import (
	"encoding/base64"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/term"
)

// Terminal is the minimal surface the renderer needs. It is an interface so
// tests can substitute a virtual terminal.
type Terminal interface {
	// Start enters raw mode and begins delivering input and resize events.
	Start(onInput func(data string), onResize func()) error
	// Stop restores the terminal to its original state.
	Stop()
	Write(s string)
	Size() (cols, rows int)
}

// ProcessTerminal drives the real stdin/stdout.
type ProcessTerminal struct {
	in, out  *os.File
	oldState *term.State
	console  consoleState // platform console modes to restore (Windows)
	done     chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex // serializes writes
}

func NewProcessTerminal() *ProcessTerminal {
	return &ProcessTerminal{in: os.Stdin, out: os.Stdout}
}

func (t *ProcessTerminal) Start(onInput func(string), onResize func()) error {
	st, err := term.MakeRaw(int(t.in.Fd()))
	if err != nil {
		return err
	}
	t.oldState = st
	t.console = enableVT(t.in, t.out)
	t.done = make(chan struct{})

	t.Write("\x1b[?2004h") // bracketed paste

	t.wg.Go(func() {
		watchResize(t, t.done, onResize)
	})

	// The read loop is not joined on Stop: a blocking read on stdin cannot be
	// interrupted portably, and the process exits shortly after anyway.
	go func() {
		parser := &inputParser{bursts: runtime.GOOS == "windows"}
		buf := make([]byte, 64*1024) // large, so a paste arrives in few reads
		for {
			n, err := t.in.Read(buf)
			if n > 0 {
				select {
				case <-t.done:
					return
				default:
				}
				for _, ev := range parser.feed(string(buf[:n])) {
					onInput(ev)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return nil
}

func (t *ProcessTerminal) Stop() {
	if t.oldState == nil {
		return
	}
	close(t.done)
	t.wg.Wait()
	t.Write("\x1b[0m\x1b[?2004l\x1b[?25h") // plain text, no paste mode, cursor on
	_ = term.Restore(int(t.in.Fd()), t.oldState)
	restoreVT(t.console)
	t.oldState = nil
}

func (t *ProcessTerminal) Write(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = io.WriteString(t.out, s)
}

func (t *ProcessTerminal) Size() (int, int) {
	w, h, err := term.GetSize(int(t.out.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

// OSC52 is the escape sequence that asks the terminal to put text on the
// system clipboard. It travels with the output, so over SSH the text lands
// on the local machine's clipboard. Inside tmux it comes twice: wrapped,
// which tmux passes through to the outer terminal with set -g
// allow-passthrough on, and plain, which tmux itself takes with
// set-clipboard on (and forwards when the outer terminal allows it).
func OSC52(text string) string {
	seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
	if os.Getenv("TMUX") != "" {
		seq = "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\" + seq
	}
	return seq
}

// WriteRaw sends s to the terminal as is, for sequences that draw nothing
// (like OSC52). Call it from the UI goroutine so it can't split a frame.
func (t *TUI) WriteRaw(s string) { t.term.Write(s) }
