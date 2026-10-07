//go:build windows

package daemon

import "net"

// The daemon needs pseudo-terminals and Unix job control; on Windows atto
// runs its TUI directly.

func Serve(string) error             { return ErrUnavailable }
func List() ([]Pane, error)          { return nil, nil }
func Stop(bool) error                { return ErrUnavailable }
func Kill(string) error              { return ErrUnavailable }
func Run(Hello) (int, string, error) { return 1, "", ErrUnavailable }

// Session workers need the daemon: on Windows the runtime runs in the
// terminal's own process.

func StartWorker(string, string, []string) (Worker, string, error) {
	return Worker{}, "", ErrUnavailable
}
func Workers() ([]Worker, error)          { return nil, nil }
func DialWorker(Worker) (net.Conn, error) { return nil, ErrUnavailable }
func RunWorker(string, []string) error    { return ErrUnavailable }
