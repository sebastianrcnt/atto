//go:build windows

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteAtomicWaitsForReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := WriteAtomic(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) // Go readers do not share delete access on Windows.
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	done := make(chan error, 1)
	go func() { done <- WriteAtomic(path, []byte("new"), 0o600) }()
	select {
	case err := <-done:
		t.Fatalf("replacement returned while reader open: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("replacement did not finish")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new" {
		t.Fatalf("replacement: %q %v", b, err)
	}
	if ents, err := os.ReadDir(filepath.Dir(path)); err != nil || len(ents) != 1 {
		t.Fatalf("temp leftovers: %v %v", ents, err)
	}
}
