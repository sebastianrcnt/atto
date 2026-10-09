package app

import (
	"os"
	"os/signal"
	"syscall"
)

// A disappearing terminal is an exit, never a session close request. On
// Unix that is SIGHUP; on Windows, Go reports a console closing (CTRL_CLOSE_EVENT,
// and logoff and shutdown) as SIGTERM, which is delivered here too. Windows
// ends the process a few seconds after the handler returns, so the exit must
// be quick: with workers it only detaches.
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
