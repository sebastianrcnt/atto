package fsutil

import (
	"io"
	"os"
)

// Open opens the file at path for reading. Unlike os.Open it survives, on
// Windows, the moment another process replaces the file (see WriteAtomic),
// when opening it fails with a sharing violation.
func Open(path string) (*os.File, error) {
	for attempt := 0; ; attempt++ {
		f, err := os.Open(path)
		if err == nil || !retryOpen(err, attempt) {
			return f, err
		}
	}
}

// ReadFile reads the whole file at path, as os.ReadFile does, but like Open
// survives the file being replaced.
func ReadFile(path string) ([]byte, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
