//go:build windows

package tui

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// consoleState holds the console modes to restore on exit.
type consoleState struct {
	in, out         windows.Handle
	inMode, outMode uint32
	ok              bool
}

// enableVT turns on VT processing so the console understands the same
// escape sequences as Unix terminals (Windows 10 1511+): ANSI output, and
// VT-encoded keys (arrows, Shift+Tab, bracketed paste, mouse) on input.
func enableVT(in, out *os.File) consoleState {
	s := consoleState{in: windows.Handle(in.Fd()), out: windows.Handle(out.Fd())}
	if windows.GetConsoleMode(s.in, &s.inMode) != nil || windows.GetConsoleMode(s.out, &s.outMode) != nil {
		return s
	}
	s.ok = true
	// The code pages are left alone: Go reads and writes the console in
	// UTF-16, so they don't affect atto, and changing them can leave the
	// console looking different after atto exits.
	_ = windows.SetConsoleMode(s.out, s.outMode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.DISABLE_NEWLINE_AUTO_RETURN|windows.ENABLE_PROCESSED_OUTPUT)
	// term.MakeRaw already ran; add VT input on top of the raw mode.
	var raw uint32
	if windows.GetConsoleMode(s.in, &raw) == nil {
		_ = windows.SetConsoleMode(s.in, raw|windows.ENABLE_VIRTUAL_TERMINAL_INPUT)
	}
	return s
}

func restoreVT(s consoleState) {
	if !s.ok {
		return
	}
	_ = windows.SetConsoleMode(s.out, s.outMode)
}

// releaseInput waits for the input reader to return, which it does within a
// poll interval once done is closed. Nothing then reads the console, so the
// keys typed next go to whatever process uses it after this one.
func (t *ProcessTerminal) releaseInput() {
	if t.reader != nil {
		t.reader.wait(time.Second)
	}
}

// watchResize polls the console size: Windows has no SIGWINCH.
func watchResize(t *ProcessTerminal, done <-chan struct{}, onResize func()) {
	w, h := t.Size()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			if nw, nh := t.Size(); nw != w || nh != h {
				w, h = nw, nh
				onResize()
			}
		}
	}
}
