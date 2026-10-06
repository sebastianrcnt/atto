package session

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

// Keep the locked byte far beyond EOF so diagnostic metadata can be read
// by other processes: Windows byte-range locks also block reads.
func lockRange(byte uint32) *windows.Overlapped {
	return &windows.Overlapped{OffsetHigh: 1 << 30, Offset: byte}
}
func lockByte(f *os.File, byte uint32) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRange(byte))
}
func unlockByte(f *os.File, byte uint32) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange(byte))
}
func tryFileLock(f *os.File) error {
	if err := lockByte(f, 0); err != nil {
		return err
	}
	if err := lockByte(f, 1); err != nil {
		_ = unlockByte(f, 0)
		return err
	}
	return nil
}
func unlockFile(f *os.File) error {
	// During handoff a child temporarily owns only B; attempt both independently.
	_ = unlockByte(f, 0)
	b := unlockByte(f, 1)
	if b != nil {
		return b
	}
	return nil
}
func fileLockBusy(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
