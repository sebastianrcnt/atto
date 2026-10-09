//go:build windows

package agentstate

import (
	"os"

	"golang.org/x/sys/windows"
)

// The locked byte lies far beyond EOF: Windows byte-range locks also block
// reads, and the locked files hold data (a spawn journal).
func lockRange() *windows.Overlapped { return &windows.Overlapped{OffsetHigh: 1 << 30} }

func tryLock(f *os.File) bool {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRange()) == nil
}

func unlock(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
}

// openShared opens path for reading and writing, creating it when create is
// set. Unlike os.OpenFile it allows others to delete the file meanwhile, as a
// spawn journal is removed while its lock is held.
func openShared(path string, create bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if create {
		disposition = windows.OPEN_ALWAYS
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, disposition, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
