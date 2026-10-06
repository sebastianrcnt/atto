package events

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInboxPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	t.Setenv("ATTO_DIR", t.TempDir())
	if err := os.MkdirAll(Dir("s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Push("s", Event{Text: "private"}); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(Dir("s"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		st, err := os.Stat(filepath.Join(Dir("s"), e.Name()))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("event file: %v, %v", st, err)
		}
	}
	st, err := os.Stat(Dir("s"))
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("inbox directory: %v, %v", st, err)
	}
}
