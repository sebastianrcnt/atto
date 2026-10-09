//go:build windows

package tui

import (
	"os"
	"testing"
	"time"
)

// With no input pending, the reader ends soon after done closes.
func TestConsoleReaderEndsWhenDone(t *testing.T) {
	done := make(chan struct{})
	r := &consoleReader{f: os.Stdin, done: done,
		ready: func() bool { return false },
		pause: func() { time.Sleep(consoleReadPollPeriod) },
	}
	ended := make(chan error, 1)
	go func() {
		_, err := r.Read(make([]byte, 8))
		ended <- err
	}()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-ended:
		t.Fatal("Read returned before done")
	default:
	}
	closed := time.Now()
	close(done)
	select {
	case err := <-ended:
		if err == nil {
			t.Fatal("Read after done should fail")
		}
		if d := time.Since(closed); d > 200*time.Millisecond {
			t.Fatalf("Read took %v to end", d)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not end after done")
	}
}
