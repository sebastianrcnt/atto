//go:build windows

package fsutil

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

// retryOpen reports whether an open that failed with err on the given attempt
// should be tried again, after a short wait. While another process replaces
// the file, an open fails with a sharing violation (or access denied) for the
// few milliseconds the replacement takes.
func retryOpen(err error, attempt int) bool {
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return false
	}
	if attempt >= 200 { // about a second, as long as WriteAtomic waits
		return false
	}
	time.Sleep(5 * time.Millisecond)
	return true
}
