// Package daemon runs atto's session workers: one daemon per user starts
// "atto _session-server" for a session (the session's runtime, package
// server, behind a Unix socket of its own), finds it again for the next
// client, and forgets it when it exits. Clients (atto -p, the desktop and
// web clients) speak atto's protocol to the worker; closing a client ends
// a view, never the work.
//
// The daemon is started on demand by the first client and exits by itself
// when its last worker ends; it is never installed as a system service.
package daemon

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Proto is the client-daemon protocol version. A client meeting a daemon
// of another version refuses the request and explains how to stop it.
// Version 3 added session workers (and is kept: removing the terminal
// panes of earlier atto2 builds changed no request that remains).
const Proto = 3

// Frame types. A frame is its type byte, the payload's length (uint32, big
// endian) and the payload. A client sends one Hello and reads one answer.
const (
	fHello  = 'H' // client: Hello (JSON), the only frame
	fExit   = 'X' // daemon: done (stop)
	fError  = 'E' // daemon: an error message
	fWorker = 'W' // daemon: a session worker (JSON), or the list of them
)

// maxFrame bounds a frame's payload.
const maxFrame = 16 << 20

// Hello is a client's request.
type Hello struct {
	Proto int    `json:"proto"`
	Op    string `json:"op"` // worker, workers, stop
	// worker: the session (Target, "" for a new one), and the working
	// directory, environment and extra arguments (-model, -effort) of the
	// worker to start.
	Target string   `json:"target,omitempty"`
	Args   []string `json:"args,omitempty"`
	Cwd    string   `json:"cwd,omitempty"`
	Env    []string `json:"env,omitempty"`
	Force  bool     `json:"force,omitempty"` // stop with workers running
}

// Worker is a session worker the daemon runs: the runtime of one
// session, reached at Socket.
type Worker struct {
	Session string    `json:"session"`
	Socket  string    `json:"socket"`
	PID     int       `json:"pid"`
	Cwd     string    `json:"cwd"`
	Started time.Time `json:"started"`
}

func writeFrame(w io.Writer, typ byte, payload []byte) error {
	var h [5]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:], uint32(len(payload)))
	if _, err := w.Write(append(h[:], payload...)); err != nil {
		return err
	}
	return nil
}

func writeJSON(w io.Writer, typ byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(w, typ, b)
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("daemon: frame of %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, nil, err
	}
	return h[0], b, nil
}

// ErrUnavailable means there is no daemon here: not on this platform, or
// turned off.
var ErrUnavailable = errors.New("atto daemon unavailable")
