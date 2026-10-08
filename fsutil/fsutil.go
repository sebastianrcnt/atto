// Package fsutil holds small filesystem helpers shared across atto.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteAtomic writes data to path so readers never see a partial file: it
// writes a uniquely named temp file in the same directory (same filesystem,
// so the rename is atomic) and renames it over the target. The unique name
// keeps concurrent writers from clobbering each other's temp file. The
// directory must already exist. The temp file is removed on any failure.
func WriteAtomic(path string, data []byte, perm os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close() // no-op if already closed
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// CreateTemp makes the file 0600; apply the mode the caller asked for.
	if err = os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	// Go's rename replaces an existing target on Windows too.
	return renameAtomic(tmp.Name(), path)
}
