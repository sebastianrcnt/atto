package fsutil

import (
	"encoding/json"
	"errors"
	"os"
)

// ReadJSON reads a JSON file. A missing file is an empty value; malformed
// files return an error and never expose a partially decoded value.
func ReadJSON[T any](path string) (T, error) {
	var value T
	data, err := ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}

// EditJSON serializes read-modify-write across processes, creating private
// directories and atomically replacing the file with mode 0600.
func EditJSON[T any](path string, edit func(*T)) error {
	return WithFileLock(path, func() error {
		value, err := ReadJSON[T](path)
		if err != nil {
			return err
		}
		edit(&value)
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		return WriteAtomic(path, append(data, '\n'), 0o600)
	})
}
