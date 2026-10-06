package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PrivateDirs creates path and tightens existing directories under root.
// Ancestors outside root and explicitly chosen external paths are left alone.
func PrivateDirs(root, path string) error {
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	for dir := path; ; dir = filepath.Dir(dir) {
		st, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if st.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(dir, st.Mode().Perm()&0o700); err != nil {
				return err
			}
		}
		if dir == filepath.Clean(root) {
			break
		}
	}
	return nil
}

// PrivateFile tightens an existing file without widening its owner permissions.
func PrivateFile(f *os.File) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return f.Chmod(st.Mode().Perm() & 0o600)
}
