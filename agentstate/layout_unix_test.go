//go:build !windows

package agentstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationGuardSurvivesDirectoryRename(t *testing.T) {
	root := t.TempDir()
	old, current := filepath.Join(root, "old"), filepath.Join(root, "current")
	write(t, filepath.Join(old, "a.lock"), "")
	f, err := os.OpenFile(filepath.Join(old, "a.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !tryLock(f) {
		t.Fatal("idle migration guard not acquired")
	}
	defer unlock(f)
	if err := os.Rename(old, current); err != nil {
		t.Fatal("guard prevented directory rename:", err)
	}
	other, err := os.OpenFile(filepath.Join(current, "a.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if tryLock(other) {
		unlock(other)
		t.Fatal("migration guard lost its lock after rename")
	}
}
