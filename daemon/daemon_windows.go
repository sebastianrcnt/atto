//go:build windows

package daemon

import "net"

// The daemon needs Unix sockets and job control; on Windows clients run
// the session's runtime in their own process.

func Serve(string) error { return ErrUnavailable }
func Stop(bool) error    { return ErrUnavailable }

func StartWorker(string, string, []string) (Worker, string, error) {
	return Worker{}, "", ErrUnavailable
}
func Workers() ([]Worker, error)          { return nil, nil }
func DialWorker(Worker) (net.Conn, error) { return nil, ErrUnavailable }
func RunWorker(string, []string) error    { return ErrUnavailable }
