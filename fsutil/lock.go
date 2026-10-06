package fsutil

import (
	"os"
	"path/filepath"
)

// WithFileLock serializes mutations of path across processes. The sibling
// lock file is persistent: unlinking it would let writers lock different files.
func WithFileLock(path string, fn func() error) error {
	if err := PrivateDirs(filepath.Dir(path), filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := PrivateFile(f); err != nil {
		return err
	}
	if err := lockFile(f); err != nil {
		return err
	}
	defer unlockFile(f)
	return fn()
}
