package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBackgroundLogPermissionMigration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	path := filepath.Join(t.TempDir(), "s.bg.log")
	if err := os.WriteFile(path, []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openBackgroundLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("log mode: %v, %v", st, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "kept" {
		t.Fatalf("existing log changed: %q, %v", data, err)
	}
}
