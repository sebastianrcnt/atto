//go:build !windows

package session

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func tryFileLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func unlockFile(f *os.File) error  { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
func fileLockBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}

func openLockFile(path string, create bool) (*os.File, error) {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	return os.OpenFile(path, flags, 0o600)
}
