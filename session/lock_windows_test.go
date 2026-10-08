package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockFileAllowsSidecarRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.lock")
	f, err := openLockFile(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := tryFileLock(f); err != nil {
		t.Fatal(err)
	}
	defer unlockFile(f)
	probe, err := openLockFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if err := tryFileLock(probe); !fileLockBusy(err) {
		t.Fatalf("contender lock: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove held sidecar: %v", err)
	}
}
