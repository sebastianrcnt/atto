package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
)

// Model request failures are kept in ~/.atto/logs/requests.log, one JSON
// object per line: the TUI shows a retry only as a notice, and a session
// keeps no trace of it. Each line says what failed, on which connection
// (so a drop can be matched to a network change), and whether the request
// went out again.

// Request log events.
const (
	requestRetry     = "retry"     // failed; sent again after Wait
	requestFailed    = "failed"    // failed; the turn ends with the error
	requestRecovered = "recovered" // succeeded after earlier failures
)

type requestLogEntry struct {
	Time      time.Time    `json:"time"`
	Event     string       `json:"event"`
	Session   string       `json:"session,omitempty"`
	Provider  string       `json:"provider,omitempty"`
	Model     string       `json:"model,omitempty"`
	Attempt   int          `json:"attempt"`
	Error     string       `json:"error,omitempty"`
	WaitMs    int64        `json:"waitMs,omitempty"`
	Note      string       `json:"note,omitempty"`
	ElapsedMs int64        `json:"elapsedMs"` // since this attempt was sent
	Conn      *ai.ConnInfo `json:"conn,omitempty"`
}

// maxRequestLog is the size at which requests.log moves to requests.log.1.
const maxRequestLog = 2 << 20

var requestLogMu sync.Mutex

func requestLogPath() string { return filepath.Join(config.Dir(), "logs", "requests.log") }

// logRequest appends e to the request log. It never fails the turn.
func logRequest(e requestLogEntry) {
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	requestLogMu.Lock()
	defer requestLogMu.Unlock()
	path := requestLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if st, err := os.Stat(path); err == nil && st.Size() > maxRequestLog {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
