package config

import (
	"os"
	"runtime"
	"testing"
)

func TestEnsureMigratesPrivateRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	root := t.TempDir()
	t.Setenv("ATTO_DIR", root)
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, SessionsDir()} {
		st, err := os.Stat(dir)
		if err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("private directory %s: %v, %v", dir, st, err)
		}
	}
}
