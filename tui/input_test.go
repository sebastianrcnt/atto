package tui

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestPendingReaderWait(t *testing.T) {
	r, w := io.Pipe()
	p := &pendingReader{r: r}
	if !p.wait(0) {
		t.Fatal("an unused reader should be idle")
	}
	got := make(chan struct{})
	go func() {
		p.Read(make([]byte, 8))
		close(got)
	}()
	for i := 0; i < 1000 && p.wait(0); i++ {
		time.Sleep(time.Millisecond)
	}
	if p.wait(10 * time.Millisecond) {
		t.Fatal("a blocked Read should not be idle")
	}
	w.Close()
	<-got
	if !p.wait(time.Second) {
		t.Fatal("a returned Read should be idle")
	}
}

func TestReadInputDisambiguatesEscapeWithoutAnotherRead(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	done := make(chan struct{})
	defer close(done)
	events := make(chan string, 8)
	go readInput(r, done, false, func(s string) { events <- s })
	started := time.Now()
	if _, err := w.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event != "\x1b" || time.Since(started) < 20*time.Millisecond {
			t.Fatalf("Escape arrived early or incorrectly: %q after %s", event, time.Since(started))
		}
	case <-time.After(time.Second):
		t.Fatal("lone Escape was never dispatched")
	}
	if _, err := w.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("[200~hello\rworld\x1b[201~")); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event != PastePrefix+"hello\rworld" {
			t.Fatalf("fragmented paste: %q", event)
		}
	case <-time.After(time.Second):
		t.Fatal("paste was not dispatched")
	}
}

func TestReadInputKeepsEscapeDelayAtEOF(t *testing.T) {
	started := time.Now()
	var got string
	readInput(strings.NewReader("\x1b"), make(chan struct{}), false, func(s string) { got = s })
	if got != "\x1b" || time.Since(started) < 20*time.Millisecond {
		t.Fatalf("EOF Escape %q after %s", got, time.Since(started))
	}
}
