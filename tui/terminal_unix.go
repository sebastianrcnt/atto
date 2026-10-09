//go:build !windows

package tui

import (
	"os"
	"os/signal"
	"syscall"
)

type consoleState struct{}

// enableVT is a no-op: Unix terminals speak VT sequences natively.
func enableVT(in, out *os.File) consoleState { return consoleState{} }
func restoreVT(consoleState)                 {}

// releaseInput does nothing: exec replaces the process, and a blocked read
// cannot be interrupted portably.
func (t *ProcessTerminal) releaseInput() {}

// watchResize delivers SIGWINCH as resize events until done closes.
func watchResize(_ *ProcessTerminal, done <-chan struct{}, onResize func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGWINCH)
	defer signal.Stop(sigs)
	for {
		select {
		case <-sigs:
			onResize()
		case <-done:
			return
		}
	}
}
