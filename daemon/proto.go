// Package daemon runs atto sessions the way tmux runs shells: one daemon
// per user owns the processes, each atto TUI in a pane (a pseudo-terminal
// it holds), and clients attach a terminal to a pane and leave it again.
// Closing a terminal, a dropped SSH connection or "Detach" leaves the
// session running; "atto attach" brings it back, from any terminal of the
// same user, a phone's SSH app included.
//
// The daemon is started on demand by the first client and exits by itself
// when its last pane ends; it is never installed as a system service. The
// panes run the same atto binary as a plain TUI (ATTO_DAEMON_PANE set), so
// execution and control are separate: the client only relays keys and
// screen output.
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
// Version 3 adds session workers.
const Proto = 3

// EnvPane is set to a pane's ID in the environment of the atto it runs.
const EnvPane = "ATTO_DAEMON_PANE"

// EnvPaneToken authenticates the pane's UI markers and is consumed at startup.
const EnvPaneToken = "ATTO_DAEMON_TOKEN"

// Frame types. A frame is its type byte, the payload's length (uint32, big
// endian) and the payload.
const (
	fHello    = 'H' // client: Hello (JSON), the first frame
	fInput    = 'I' // client: bytes typed
	fResize   = 'R' // client: Size (JSON)
	fOutput   = 'O' // daemon: bytes to show
	fAttached = 'A' // daemon: Pane (JSON), the client is attached
	fExit     = 'X' // daemon: Exit (JSON), the last frame
	fList     = 'L' // daemon: []Pane (JSON)
	fError    = 'E' // daemon: an error message, the last frame
	fWorker   = 'W' // daemon: a session worker (JSON), or the list of them
)

// maxFrame bounds a frame's payload.
const maxFrame = 16 << 20

// Hello is a client's request.
type Hello struct {
	Proto int    `json:"proto"`
	Op    string `json:"op"` // new, attach, list, stop, kill, worker, workers
	// new: the atto arguments, working directory and environment.
	Args []string `json:"args,omitempty"`
	Cwd  string   `json:"cwd,omitempty"`
	Env  []string `json:"env,omitempty"`
	// attach, kill: the pane (its ID, or the session ID its atto has
	// open); attach without one takes the most recent.
	Target string `json:"target,omitempty"`
	Force  bool   `json:"force,omitempty"` // stop with panes running
	Size
}

// Size is a terminal's size.
type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Pane describes a pane.
type Pane struct {
	ID      int       `json:"id"`
	PID     int       `json:"pid"`
	Cwd     string    `json:"cwd"`
	Args    []string  `json:"args,omitempty"`
	Started time.Time `json:"started"`
	Clients int       `json:"clients"`
	// Session and Name are what its atto reports: the open session.
	Session string `json:"session,omitempty"`
	Name    string `json:"name,omitempty"`
	// State is what its atto last said it is doing: working (a turn
	// runs), waiting (it needs the user: a question, a held goal) or idle.
	State  string    `json:"state,omitempty"`
	Active time.Time `json:"active"` // last input or attach
}

// Worker is a session worker the daemon runs: the runtime of one
// session, reached at Socket.
type Worker struct {
	ID      string    `json:"id"`
	Version string    `json:"version"`
	Busy    bool      `json:"busy"`
	Clients int       `json:"clients"`
	Session string    `json:"session"`
	Socket  string    `json:"socket"`
	PID     int       `json:"pid"`
	Cwd     string    `json:"cwd"`
	Started time.Time `json:"started"`
}

// Exit ends an attachment.
type Exit struct {
	Code     int    `json:"code"`
	Detached bool   `json:"detached,omitempty"` // the pane goes on
	Reason   string `json:"reason,omitempty"`
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
