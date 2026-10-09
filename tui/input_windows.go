//go:build windows

package tui

import (
	"io"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// inputRecord is INPUT_RECORD (20 bytes) as its KEY_EVENT_RECORD reads.
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

const keyEventType = 0x0001

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procPeekConsoleInput = kernel32.NewProc("PeekConsoleInputW")
	procReadConsoleInput = kernel32.NewProc("ReadConsoleInputW")
)

const consoleReadPollPeriod = 20 * time.Millisecond

// consoleReader reads the console input without ever blocking in the
// console: a ReadConsole call cannot be cancelled (CancelIoEx only makes it
// return, and the console keeps the request, which then swallows the next
// keys typed, even into another process on the same console). So it reads
// only when a key press with a character is queued, discards the events a
// read would skip (key releases, focus, resize), and otherwise waits on the
// input handle for a short while, checking for done in between.
type consoleReader struct {
	f     *os.File
	done  <-chan struct{}
	ready func() bool // a read would return at once; discards what it would skip
	pause func()      // waits for console input, for a short while
}

func newInputReader(in *os.File, done <-chan struct{}) io.Reader {
	h := windows.Handle(in.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return in // not a console: a pipe or file, whose reads end by themselves
	}
	return &consoleReader{f: in, done: done,
		ready: func() bool { return consoleInputReady(h) },
		pause: func() {
			if _, err := windows.WaitForSingleObject(h, uint32(consoleReadPollPeriod/time.Millisecond)); err != nil {
				time.Sleep(consoleReadPollPeriod)
			}
		},
	}
}

func (r *consoleReader) Read(b []byte) (int, error) {
	for {
		select {
		case <-r.done:
			return 0, io.EOF
		default:
		}
		if r.ready() {
			return r.f.Read(b)
		}
		r.pause()
	}
}

// consoleInputReady reports whether ReadConsole would return without
// waiting. Queued events a read would skip are removed; keys are never.
func consoleInputReady(h windows.Handle) bool {
	var recs [64]inputRecord
	var n uint32
	r, _, _ := procPeekConsoleInput.Call(uintptr(h), uintptr(unsafe.Pointer(&recs[0])), uintptr(len(recs)), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return true // cannot look: read, as before
	}
	if n == 0 {
		return false
	}
	for _, rec := range recs[:n] {
		if rec.eventType == keyEventType && rec.keyDown != 0 && rec.unicodeChar != 0 {
			return true
		}
	}
	// Only the events peeked are discarded, and this is the only reader.
	var got uint32
	procReadConsoleInput.Call(uintptr(h), uintptr(unsafe.Pointer(&recs[0])), uintptr(n), uintptr(unsafe.Pointer(&got)))
	return false
}
