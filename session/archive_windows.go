package session

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Share deletion so a transcript reader cannot prevent archival or deletion.
func openSessionFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
func archiveRetry(fn func() error) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := fn()
		if (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func archiveRename(from, to string) error {
	oldName, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	newName, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return archiveRetry(func() error {
		err := windows.MoveFileEx(oldName, newName, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err != nil {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
		}
		return nil
	})
}
func archiveRemove(path string) error { return archiveRetry(func() error { return os.Remove(path) }) }

// Windows does not support fsync on directory handles. Flush the complete
// output file and request a write-through rename instead. Never remove the
// source before publishing the complete destination.
func syncSessionDir(string) error { return nil }
