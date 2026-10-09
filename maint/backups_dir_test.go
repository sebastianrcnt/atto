package maint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A backup atto takes itself (before migrating agent data) lives in
// ATTO_DIR/backups, and backups never contain backups.
func TestBackupMayLiveInTheBackupsDirectoryAndIsNotArchived(t *testing.T) {
	root := fixture(t)
	if err := os.MkdirAll(filepath.Join(root, BackupsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, BackupsDir, "first.tar.zst")
	if _, e := Backup(BackupOptions{Root: root, Output: first, Version: "t"}); e != nil {
		t.Fatal(e)
	}
	second := filepath.Join(root, BackupsDir, "second.tar.zst")
	m, e := Backup(BackupOptions{Root: root, Output: second, Version: "t"})
	if e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if _, e := Restore(second, RestoreOptions{Root: dest}); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(dest, BackupsDir)); !os.IsNotExist(e) {
		t.Fatalf("a backup holds backups: %v", e)
	}
	if !strings.Contains(strings.Join(m.Excluded, " "), "backups/") {
		t.Fatalf("excluded %v", m.Excluded)
	}
	// Elsewhere inside ATTO_DIR is still refused.
	if _, e := Backup(BackupOptions{Root: root, Output: filepath.Join(root, "inside.tar.zst")}); e == nil {
		t.Fatal("a backup inside ATTO_DIR was written")
	}
}

// The manifest says which layout of agent data the backup holds.
func TestBackupRecordsTheAgentLayout(t *testing.T) {
	root := fixture(t)
	m, e := Backup(BackupOptions{Root: root, Output: filepath.Join(t.TempDir(), "old.tar.zst")})
	if e != nil || m.Formats.AgentState != 1 {
		t.Fatalf("no marker: %+v %v", m.Formats, e)
	}
	if e := os.MkdirAll(filepath.Join(root, "agent-state"), 0o755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "agent-state", ".format"), []byte(`{"version":2}`), 0o644); e != nil {
		t.Fatal(e)
	}
	m, e = Backup(BackupOptions{Root: root, Output: filepath.Join(t.TempDir(), "new.tar.zst")})
	if e != nil || m.Formats.AgentState != 2 {
		t.Fatalf("marker: %+v %v", m.Formats, e)
	}
}
