package session

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

// Keep the locked byte far beyond EOF so diagnostic metadata can be read
// by other processes: Windows byte-range locks also block reads.
func lockRange() *windows.Overlapped { return &windows.Overlapped{OffsetHigh: 1 << 30} }
func tryFileLock(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRange())
}
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
}
func fileLockBusy(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
