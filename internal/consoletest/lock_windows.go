//go:build windows

package consoletest

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Lock holds the console for the calling test until it ends. The packages of
// one go test run share a console, so a test that sets its code page and
// checks it again would otherwise see the change of a test in another package.
func Lock(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "atto-test-console.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	h := windows.Handle(f.Fd())
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{}); err != nil {
		f.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = windows.UnlockFileEx(h, 0, 1, 0, &windows.Overlapped{})
		f.Close()
	})
}
