//go:build windows

package fsutil

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func renameAtomic(old, new string) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := os.Rename(old, new)
		// Readers opened without FILE_SHARE_DELETE briefly prevent replacing
		// their file. Keep the complete temp file and retry the rename only.
		if (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
