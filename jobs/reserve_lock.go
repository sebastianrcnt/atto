package jobs

import (
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/fsutil"
	"os"
	"path/filepath"
	"time"
)

const reservationLockTimeout = 10 * time.Second

func lockReservations(session string) (func(), error) {
	if !fsutil.ValidID(session) {
		return nil, fmt.Errorf("invalid session id")
	}
	root := Root(session)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(root, ".reserve.lock")
	deadline := time.Now().Add(2 * reservationLockTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			info, err := f.Stat()
			f.Close()
			if err != nil {
				return nil, err
			}
			return func() { _ = removeReservationLock(path, info) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if time.Since(info.ModTime()) > reservationLockTimeout {
			if err := removeReservationLock(path, info); err != nil {
				return nil, err
			}
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out reserving a job")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Remove the checked file, not a new reservation lock at the same path.
func removeReservationLock(path string, checked os.FileInfo) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".reserve-old-*")
	if err != nil {
		return err
	}
	moved := f.Name()
	f.Close()
	os.Remove(moved)
	if err := os.Rename(path, moved); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	info, err := os.Stat(moved)
	if err != nil {
		return err
	}
	if !os.SameFile(checked, info) {
		if err := os.Link(moved, path); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return os.Remove(moved)
}
