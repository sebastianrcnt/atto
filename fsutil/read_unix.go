//go:build !windows

package fsutil

// retryOpen: opening a file never fails because it is being replaced.
func retryOpen(error, int) bool { return false }
