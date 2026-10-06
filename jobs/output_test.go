package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeadLongLine(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	path := OutputPath("s", 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 2<<20)
	if err := os.WriteFile(path, []byte(line+"\nlast"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Head("s", 1, 1)
	if err != nil || got != line {
		t.Fatalf("head: %d bytes, %v", len(got), err)
	}
	got, err = Head("s", 1, 3)
	if err != nil || got != line+"\nlast" {
		t.Fatalf("head to EOF: %d bytes, %v", len(got), err)
	}
}
