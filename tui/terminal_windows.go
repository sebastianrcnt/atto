//go:build windows

package tui

import (
	"os"
	"time"
	"unsafe"

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

// releaseInput ends the console read that is still blocked after Stop, so
// that it cannot take the first keys typed into a process started next on
// this console (atto agents runs "atto resume" as a child). The read is
// cancelled; if that does not wake it, a Return key event is queued for it
// to swallow, the way libuv unblocks a console reader.
func (t *ProcessTerminal) releaseInput() {
	if t.reader == nil || t.reader.wait(0) {
		return
	}
	_ = windows.CancelIoEx(t.console.in, nil)
	if t.reader.wait(100 * time.Millisecond) {
		return
	}
	wakeConsoleRead(t.console.in)
	t.reader.wait(time.Second)
}

// inputRecord is INPUT_RECORD holding a KEY_EVENT_RECORD.
type inputRecord struct {
	eventType       uint16
	_               uint16
	keyDown         int32
	repeatCount     uint16
	virtualKeyCode  uint16
	virtualScanCode uint16
	unicodeChar     uint16
	controlKeyState uint32
}

var procWriteConsoleInput = windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleInputW")

// wakeConsoleRead queues a Return key press on the console input.
func wakeConsoleRead(in windows.Handle) {
	const keyEvent = 0x0001
	rec := inputRecord{eventType: keyEvent, keyDown: 1, repeatCount: 1, virtualKeyCode: 0x0D, virtualScanCode: 0x1C, unicodeChar: '\r'}
	var n uint32
	_, _, _ = procWriteConsoleInput.Call(uintptr(in), uintptr(unsafe.Pointer(&rec)), 1, uintptr(unsafe.Pointer(&n)))
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
