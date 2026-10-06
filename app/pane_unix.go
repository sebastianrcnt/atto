//go:build !windows

package app

import (
	"os"
	"os/signal"
	"syscall"
)

// watchRedraw calls redraw on SIGUSR1, which the daemon sends when a
// terminal attaches.
func watchRedraw(redraw func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1)
	go func() {
		for range sigs {
			redraw()
		}
	}()
}
