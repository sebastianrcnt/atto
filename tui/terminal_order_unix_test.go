//go:build !windows

package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestProcessTerminalKeyboardModesInsideAltScreen(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	chunks := make(chan string, 100)
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	term := &ProcessTerminal{in: slave, out: slave}
	ui := New(term)
	ui.Mode = Fullscreen
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			ui.Stop()
		}
	}()
	var output strings.Builder
	until := func(want string) {
		t.Helper()
		deadline := time.After(time.Second)
		for !strings.Contains(output.String(), want) {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatal(io.EOF)
				}
				output.WriteString(chunk)
			case <-deadline:
				t.Fatalf("waiting for %q in %q", want, output.String())
			}
		}
	}
	until(kittyOn)
	initial := output.String()
	if strings.Index(initial, "\x1b[?1049h") < 0 || strings.Index(initial, kittyOn) < strings.Index(initial, "\x1b[?1049h") {
		t.Fatalf("keyboard push before alt screen: %q", initial)
	}
	ui.Stop()
	stopped = true
	until("\x1b[?1049l")
	final := output.String()
	if pop, leave := strings.Index(final, kittyOff), strings.Index(final, "\x1b[?1049l"); pop < 0 || pop > leave {
		t.Fatalf("keyboard pop after alt screen: %q", final)
	}
}
