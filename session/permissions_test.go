package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

func TestSessionPermissionMigration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	root := t.TempDir()
	t.Setenv("ATTO_DIR", root)
	w := New("/work")
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.Path, []byte(`{"type":"session"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(LockPath(w.Path), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	release, err := Lock(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	w.Append(Entry{Type: TypeName, Name: "private"})
	w.Close()
	for dir := filepath.Dir(w.Path); ; dir = filepath.Dir(dir) {
		st, err := os.Stat(dir)
		if err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s: %v, %v", dir, st, err)
		}
		if dir == config.Dir() {
			break
		}
	}
	for _, path := range []string{w.Path, LockPath(w.Path)} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("file %s: %v, %v", path, st, err)
		}
	}
}
