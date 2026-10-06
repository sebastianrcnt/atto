package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Session writer leases use OS advisory locks. The persistent lock file
// holds diagnostic metadata only; its existence never implies ownership.

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

// heldLocks tracks compatible same-process acquisitions. Every release is
// idempotent and refers to its own lease, so an old release cannot drop a
// newer acquisition (including one acquired after the file was freed).
var heldLocks = struct {
	sync.Mutex
	locks map[string]*heldLock
}{locks: make(map[string]*heldLock)}

type heldLock struct {
	file        *os.File
	info        LockInfo
	refs        int
	transferred bool
}

func lockKey(path string) (string, error) {
	lp, err := filepath.Abs(LockPath(path))
	if err != nil {
		return "", err
	}
	// Resolve the directory, not the file: the file may not exist yet.
	if dir, err := filepath.EvalSymlinks(filepath.Dir(lp)); err == nil {
		lp = filepath.Join(dir, filepath.Base(lp))
	}
	return lp, nil
}

func readLockInfo(f *os.File) LockInfo {
	var l LockInfo
	b, _ := io.ReadAll(io.NewSectionReader(f, 0, 1<<20))
	_ = json.Unmarshal(b, &l)
	return l
}

// LockedBy probes the OS lock, not the metadata. Partial or unreadable
// metadata must not make an actively held lease appear free.
func LockedBy(path string) (LockInfo, bool) {
	f, err := os.OpenFile(LockPath(path), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return LockInfo{}, false
	}
	if err != nil {
		return LockInfo{}, true
	}
	defer f.Close()
	if err := tryFileLock(f); err == nil {
		_ = unlockFile(f)
		return LockInfo{}, false
	}
	return readLockInfo(f), true
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

// Lock takes the session's writer lease for the calling process.
func Lock(path string) (release func(), err error) { return LockKind(path, KindRun) }

// LockKind takes a writer lease for this frontend.
func LockKind(path, kind string) (release func(), err error) { return lockAs(path, os.Getpid(), kind) }

// LockFor takes a lease in the calling process, recording pid for messages.
// The OS lease still belongs to the caller: background children must inherit
// its open file, rather than acquiring a separate lock after spawning.
func LockFor(path string, pid int) (release func(), err error) {
	return lockAs(path, pid, KindBackground)
}

func LockTUI(path string) (release func(), err error) {
	return lockAs(path, os.Getpid(), KindTUI)
}

func writeLockInfo(f *os.File, info LockInfo) error {
	body, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.WriteAt(body, 0); err != nil {
		return err
	}
	return f.Sync()
}

// releaseLocked must be called with heldLocks held.
func releaseLocked(key string, h *heldLock) func() {
	h.refs++
	var once sync.Once
	return func() {
		once.Do(func() {
			heldLocks.Lock()
			defer heldLocks.Unlock()
			h.refs--
			if h.refs == 0 && !h.transferred {
				if heldLocks.locks[key] == h {
					delete(heldLocks.locks, key)
				}
				_ = unlockFile(h.file)
				_ = h.file.Close()
			}
		})
	}
}

func lockAs(path string, pid int, kind string) (release func(), err error) {
	if err := fsutil.PrivateDirs(config.Dir(), filepath.Dir(path)); err != nil {
		return nil, err
	}
	key, err := lockKey(path)
	if err != nil {
		return nil, err
	}
	heldLocks.Lock()
	defer heldLocks.Unlock()
	if h := heldLocks.locks[key]; h != nil {
		owned := h.info.PID == pid && (kind == KindTUI && h.info.Kind == KindTUI || kind == KindBackground && (h.info.Kind == KindBackground || h.info.Kind == ""))
		if !owned {
			return nil, LockError(h.info)
		}
		return releaseLocked(key, h), nil
	}
	f, err := os.OpenFile(key, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := fsutil.PrivateFile(f); err != nil {
		f.Close()
		return nil, err
	}
	if err := tryFileLock(f); err != nil {
		info := readLockInfo(f)
		_ = f.Close()
		if fileLockBusy(err) {
			return nil, LockError(info)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	info := LockInfo{PID: pid, Started: time.Now(), Kind: kind}
	if err := writeLockInfo(f, info); err != nil {
		_ = unlockFile(f)
		_ = f.Close()
		return nil, err
	}
	h := &heldLock{file: f, info: info}
	heldLocks.locks[key] = h
	return releaseLocked(key, h), nil
}
