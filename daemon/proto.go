// Package daemon supervises session workers. Each terminal is an independent client.
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
// Version 4 removes PTY panes; only stop/status may use older revisions.
const Proto = 4

// Control frame types (type byte, big-endian uint32 length, payload).
const (
	fHello  = 'H'
	fExit   = 'X'
	fList   = 'L' // legacy status only
	fError  = 'E'
	fWorker = 'W'
)

// maxFrame bounds a frame's payload.
const maxFrame = 16 << 20

// Hello is a client's request.
type Hello struct {
	Proto int    `json:"proto"`
	Op    string `json:"op"` // stop, status, kill, worker, workers
	// worker: arguments, working directory and environment.
	Args   []string `json:"args,omitempty"`
	Cwd    string   `json:"cwd,omitempty"`
	Env    []string `json:"env,omitempty"`
	Target string   `json:"target,omitempty"` // session ID
	Force  bool     `json:"force,omitempty"`  // stop running workers too
}

// Worker is a session worker the daemon runs: the runtime of one
// session, reached at Socket.
type Worker struct {
	OpenPrompt  bool      `json:"openPrompt"`
	GoalWaiting bool      `json:"goalWaiting"`
	Name        string    `json:"name,omitempty"`
	State       string    `json:"state,omitempty"`
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Busy        bool      `json:"busy"`
	Clients     int       `json:"clients"`
	Session     string    `json:"session"`
	Socket      string    `json:"socket"`
	PID         int       `json:"pid"`
	Cwd         string    `json:"cwd"`
	Started     time.Time `json:"started"`
	// Replaceable: the worker closes for an upgrade on request
	// (worker/retire) and outlives its daemon (a handover).
	Replaceable bool `json:"replaceable,omitempty"`
}

// Exit acknowledges a successful control operation.
type Exit struct {
	Code int `json:"code"`
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

// ErrProtocol means this daemon and client cannot share the control protocol.
var ErrProtocol = errors.New("incompatible atto daemon protocol")
