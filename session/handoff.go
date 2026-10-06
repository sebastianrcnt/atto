package session

import (
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	inheritedLockHandle = "ATTO_SESSION_LOCK_HANDLE"
	inheritedLockPath   = "ATTO_SESSION_LOCK_PATH"
	inheritedLockReady  = "ATTO_SESSION_LOCK_READY"
)

// StartBackground starts cmd with an inherited session lease. It can hand
// over this process's TUI lease without ever unlocking the session. Success
// consumes that lease: any older release functions become harmless. Failure
// leaves an existing TUI lease intact, or releases a freshly acquired one.
// The child must call AdoptBackgroundLock before writing the session.
func StartBackground(path string, cmd *exec.Cmd) error {
	if err := fsutil.PrivateDirs(config.Dir(), filepath.Dir(path)); err != nil {
		return err
	}
	key, err := lockKey(path)
	if err != nil {
		return err
	}
	heldLocks.Lock()
	defer heldLocks.Unlock()
	h := heldLocks.locks[key]
	fresh := h == nil
	if fresh {
		f, err := os.OpenFile(key, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		if err := fsutil.PrivateFile(f); err != nil {
			f.Close()
			return err
		}
		if err := tryFileLock(f); err != nil {
			info := readLockInfo(f)
			_ = f.Close()
			if fileLockBusy(err) {
				return LockError(info)
			}
			return err
		}
		h = &heldLock{file: f, info: LockInfo{PID: os.Getpid(), Started: time.Now(), Kind: KindBackground}}
		if err := writeLockInfo(f, h.info); err != nil {
			_ = unlockFile(f)
			_ = f.Close()
			return err
		}
		heldLocks.locks[key] = h
	} else if h.info.PID != os.Getpid() || (h.info.Kind != KindTUI && h.info.Kind != KindBackground) {
		return LockError(h.info)
	}
	succeeded := false
	defer func() {
		if !succeeded {
			stopInheritance(h.file)
		}
		if !succeeded && fresh {
			delete(heldLocks.locks, key)
			_ = unlockFile(h.file)
			_ = h.file.Close()
		}
	}()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyR.Close()
	defer readyW.Close()
	handle, err := inheritFile(cmd, h.file)
	if err != nil {
		return err
	}
	readyHandle, err := inheritFile(cmd, readyW)
	if err != nil {
		return err
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	// Do not propagate a previous handoff's descriptor numbers into descendants.
	cmd.Env = withoutHandoffEnv(env)
	cmd.Env = append(cmd.Env, inheritedLockHandle+"="+handle, inheritedLockPath+"="+key, inheritedLockReady+"="+readyHandle)
	if err := prepareTransfer(h.file); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if restore := cancelTransfer(h.file); restore != nil {
			return fmt.Errorf("start background: %w (restoring lease: %v)", err, restore)
		}
		return err
	}
	_ = readyW.Close()
	result := make(chan error, 1)
	go func() {
		var ack [1]byte
		_, err := io.ReadFull(readyR, ack[:])
		if err == nil && ack[0] != 1 {
			err = errors.New("background child rejected inherited session lease")
		}
		result <- err
	}()
	select {
	case err = <-result:
	case <-time.After(10 * time.Second):
		err = errors.New("background child did not adopt session lease within 10s")
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		// The child is gone, so Windows guard B can safely return to the parent.
		restore := cancelTransfer(h.file)
		if restore != nil {
			return fmt.Errorf("handoff failed: %w (restoring lease: %v)", err, restore)
		}
		_ = writeLockInfo(h.file, h.info)
		return fmt.Errorf("handoff failed: %w", err)
	}
	h.transferred = true
	delete(heldLocks.locks, key)
	// Close only: Unix's shared file description must never be explicitly
	// unlocked by the old owner. On Windows child owns B before parent closes A.
	_ = finishTransfer(h.file)
	succeeded = true
	return nil
}

func withoutHandoffEnv(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if name != inheritedLockHandle && name != inheritedLockPath && name != inheritedLockReady {
			result = append(result, entry)
		}
	}
	return result
}

func inheritedFile(name string) (*os.File, error) {
	n, err := strconv.ParseUint(os.Getenv(name), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid inherited handle %s: %w", name, err)
	}
	f := os.NewFile(uintptr(n), name)
	if f == nil {
		return nil, fmt.Errorf("invalid inherited handle %s", name)
	}
	return f, nil
}

// AdoptBackgroundLock consumes the inherited handoff descriptors, verifies
// that the file names this session and is OS-locked, and records the child's
// PID. Without a handoff it acquires an ordinary background lease.
func AdoptBackgroundLock(path string) (func(), error) {
	if os.Getenv(inheritedLockHandle) == "" {
		return LockKind(path, KindBackground)
	}
	f, err := inheritedFile(inheritedLockHandle)
	if err != nil {
		return nil, err
	}
	ready, err := inheritedFile(inheritedLockReady)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	defer ready.Close()
	stopInheritance(f)
	stopInheritance(ready)
	expected := os.Getenv(inheritedLockPath)
	_ = os.Unsetenv(inheritedLockHandle)
	_ = os.Unsetenv(inheritedLockPath)
	_ = os.Unsetenv(inheritedLockReady)
	owned := false
	defer func() {
		if !owned {
			_, _ = ready.Write([]byte{0})
			_ = f.Close()
		}
	}()
	key, err := lockKey(path)
	if err != nil {
		return nil, err
	}
	if key != expected {
		return nil, errors.New("inherited session lease names another session")
	}
	actual, err := f.Stat()
	if err != nil {
		return nil, err
	}
	named, err := os.Stat(key)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(actual, named) {
		return nil, errors.New("inherited session lease names another file")
	}
	if _, ok := LockedBy(path); !ok {
		return nil, errors.New("inherited session lease is not held")
	}
	if err := claimTransferred(f); err != nil {
		return nil, fmt.Errorf("claim inherited session lease: %w", err)
	}
	// Probe via a separate handle: taking the inherited handle's flock above
	// must not be mistaken for proof that the path has exclusive ownership.
	if _, ok := LockedBy(path); !ok {
		return nil, errors.New("inherited session lease is not exclusive")
	}
	heldLocks.Lock()
	defer heldLocks.Unlock()
	if heldLocks.locks[key] != nil {
		return nil, errors.New("session lease already held in child")
	}
	info := LockInfo{PID: os.Getpid(), Started: time.Now(), Kind: KindBackground}
	if err := writeLockInfo(f, info); err != nil {
		return nil, err
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		return nil, err
	}
	if err := finishClaim(f); err != nil {
		return nil, err
	}
	h := &heldLock{file: f, info: info}
	heldLocks.locks[key] = h
	owned = true
	return releaseLocked(key, h), nil
}
