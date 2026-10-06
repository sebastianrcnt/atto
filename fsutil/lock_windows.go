//go:build windows

package fsutil

import (
	"golang.org/x/sys/windows"
	"os"
)

// Lock beyond EOF so the data remains readable on Windows too.
func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{OffsetHigh: 1 << 30})
}
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{OffsetHigh: 1 << 30})
}
