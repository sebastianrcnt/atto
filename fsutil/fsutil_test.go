package fsutil

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

func TestWriteAtomicReplaces(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.json")
	for _, s := range []string{"one", "two, longer", "3"} {
		if err := WriteAtomic(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(p); string(got) != s {
			t.Fatalf("got %q want %q", got, s)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
			t.Fatalf("perm %v", fi.Mode().Perm())
		}
		q := filepath.Join(filepath.Dir(p), "secret")
		if err := WriteAtomic(q, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(q); fi.Mode().Perm() != 0o600 {
			t.Fatalf("perm %v", fi.Mode().Perm())
		}
	}
}

func TestWriteAtomicMissingDirLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAtomic(filepath.Join(dir, "no", "f"), []byte("x"), 0o644); err == nil {
		t.Fatal("want error")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestWriteAtomicRenameFailureCleansUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "d")
	if err := os.Mkdir(target, 0o755); err != nil { // a directory can't be replaced by a file
		t.Fatal(err)
	}
	if err := WriteAtomic(target, []byte("x"), 0o644); err == nil {
		t.Fatal("want error")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestWriteAtomicConcurrent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	contents := make([][]byte, 16)
	for i := range contents {
		contents[i] = bytes.Repeat([]byte(strconv.Itoa(i%10)), 4096+i)
	}
	var wg sync.WaitGroup
	for i, c := range contents {
		wg.Go(func() {
			for range 20 {
				// Windows can briefly refuse a rename while another
				// writer holds the target open; retry like a caller would.
				var err error
				for range 50 {
					if err = WriteAtomic(p, c, 0o644); err == nil {
						break
					}
				}
				if err != nil {
					t.Errorf("writer %d: %v", i, err)
					return
				}
			}
		})
	}
	wg.Wait()
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, c := range contents {
		ok = ok || bytes.Equal(got, c)
	}
	if !ok {
		t.Fatalf("final content is not any writer's data (%d bytes)", len(got))
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temp leftovers: %v", ents)
	}
}
