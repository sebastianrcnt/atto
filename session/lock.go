package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A session being written by a process that other atto processes must not
// write to holds a lock file next to the session file: a run left in the
// background (experimental, see app/background_exit.go), or a terminal
// session that has it open. It names the process (pid and start time); a
// lock whose process is gone is stale and ignored.

// LockInfo is the content of a lock file.
type LockInfo struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Kind    string    `json:"kind,omitempty"` // empty for legacy background runs
}

// Writer kinds distinguish a detached continuation from other frontends.
const (
	KindTUI        = "tui"
	KindRun        = "run"
	KindServer     = "server"
	KindBackground = "background"
)

// ErrLocked is wrapped by Lock when a live process holds the session.
var ErrLocked = errors.New("session is running in the background")

// LockPath is the lock file of the session at path.
func LockPath(path string) string { return strings.TrimSuffix(path, ".jsonl") + ".lock" }

// LogPath is where a background run of the session at path logs.
func LogPath(path string) string { return strings.TrimSuffix(path, ".jsonl") + ".bg.log" }

// processAlive is a seam for tests; the real check is per platform.
var processAlive = pidAlive
var processStartTime = pidStartTime

// moveLock is a seam for takeover races in tests.
var moveLock = os.Rename

// LockedBy reads the session's lock. ok is false when there is none or it
// is stale (its process is gone or its pid was reused).
func LockedBy(path string) (LockInfo, bool) {
	b, err := os.ReadFile(LockPath(path))
	if err != nil {
		return LockInfo{}, false
	}
	return liveLock(b)
}

func liveLock(b []byte) (LockInfo, bool) {
	var l LockInfo
	if json.Unmarshal(b, &l) != nil || l.PID <= 0 || !processAlive(l.PID) {
		return LockInfo{}, false
	}
	if started, ok := processStartTime(l.PID); ok && started.After(l.Started.Add(2*time.Second)) {
		return LockInfo{}, false
	}
	return l, true
}

// LockError describes a locked session for the user.
func LockError(l LockInfo) error {
	if l.Kind == KindTUI {
		return openError(fmt.Sprintf("session is open in another atto (pid %d): continue it there (atto attach, if it runs in the daemon), or close it there first", l.PID))
	}
	if l.Kind == KindRun || l.Kind == KindServer {
		return openError(fmt.Sprintf("session is being written by another atto (%s, pid %d): close it there first", l.Kind, l.PID))
	}
	return fmt.Errorf("%w (pid %d): wait for it to finish, then try again", ErrLocked, l.PID)
}

// openError is ErrLocked for a session open in another terminal, worded
// for that.
type openError string

func (e openError) Error() string        { return string(e) }
func (e openError) Is(target error) bool { return target == ErrLocked }

// ReadOnlyMessage is what the TUI shows for a locked session.
func ReadOnlyMessage(l LockInfo) string {
	return fmt.Sprintf("Running in background (pid %d) — read-only until it finishes", l.PID)
}

// Lock takes the session's lock for the calling process. It fails with
// ErrLocked when another live process holds it; a stale lock is replaced.
// The returned function releases it.
func Lock(path string) (release func(), err error) { return LockKind(path, KindRun) }

// LockKind takes a writer lease for this frontend.
func LockKind(path, kind string) (release func(), err error) { return lockAs(path, os.Getpid(), kind) }

// LockFor takes the lock on behalf of the process with the given pid, so a
// parent can hold a session for the background run it just started. That
// process taking the lock itself then finds it its own.
func LockFor(path string, pid int) (release func(), err error) {
	return lockAs(path, pid, KindBackground)
}

// LockTUI takes the lock for a terminal session: other terminals, runs and
// clients are refused while it is held.
func LockTUI(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // a new session's day may have no directory yet
		return nil, err
	}
	return lockAs(path, os.Getpid(), KindTUI)
}

func lockAs(path string, pid int, kind string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	lp := LockPath(path)
	body, _ := json.Marshal(LockInfo{PID: pid, Started: time.Now(), Kind: kind})
	for range 3 {
		f, err := os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := f.Write(body)
			f.Close()
			if werr != nil {
				os.Remove(lp)
				return nil, werr
			}
			return func() { unlock(lp, body) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		stale, err := os.ReadFile(lp)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if l, ok := liveLock(stale); ok {
			owned := l.PID == pid && (kind == KindTUI && l.Kind == KindTUI || kind == KindBackground && (l.Kind == KindBackground || l.Kind == ""))
			if !owned {
				return nil, LockError(l)
			}
		}
		if err := retireLock(lp, stale); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("could not lock %s", path)
}

// retireLock moves the checked lock aside before deciding whether to remove it.
func retireLock(lp string, checked []byte) error {
	f, err := os.CreateTemp(filepath.Dir(lp), ".lock-takeover-*")
	if err != nil {
		return err
	}
	moved := f.Name()
	f.Close()
	os.Remove(moved)
	if err := moveLock(lp, moved); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := os.ReadFile(moved)
	if err != nil {
		return err
	}
	if !bytes.Equal(b, checked) {
		// Another contender replaced the lock. Restore it without replacing a winner.
		if err := os.Link(moved, lp); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return os.Remove(moved)
}

// unlock removes only the exact acquisition that returned this release function.
func unlock(lp string, body []byte) {
	b, err := os.ReadFile(lp)
	if err != nil || !bytes.Equal(b, body) {
		return
	}
	_ = retireLock(lp, body)
}
