package agentstate

import (
	"os"

	"golang.org/x/sys/windows"
)

// The migration target does not exist yet. os.Symlink would guess a file
// symlink on Windows, which cannot alias the directory after its rename.
func stateAlias(target, link string) error {
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	const allowUnprivileged = 0x2 // supported since Windows 10 Creators Update
	err = windows.CreateSymbolicLink(name, to, windows.SYMBOLIC_LINK_FLAG_DIRECTORY|allowUnprivileged)
	if err != nil {
		err = windows.CreateSymbolicLink(name, to, windows.SYMBOLIC_LINK_FLAG_DIRECTORY)
	}
	if err != nil {
		return &os.LinkError{Op: "symlink", Old: target, New: link, Err: err}
	}
	return nil
}

// Unlike os.OpenFile's Windows default, allow deleting/renaming the guarded
// directory while we hold its idle lock files open through migration.
func openStateGuard(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
