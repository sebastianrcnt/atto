//go:build !windows

package app

import (
	"os"
	"os/signal"
	"syscall"
)

// A disappearing terminal is an exit, never a session close request.
func watchTerminalExit(quit func()) func() {
	sigs := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM)
	go func() {
		select {
		case <-sigs:
			quit()
		case <-done:
		}
	}()
	return func() { signal.Stop(sigs); close(done) }
}
