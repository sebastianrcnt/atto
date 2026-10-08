package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDebug(t *testing.T) {
	dir, err := writeDebug(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"heap.pprof", "goroutines.txt", "memstats.txt"} {
		if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.Size() == 0 {
			t.Fatalf("%s: %v", name, err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "memstats.txt"))
	if !strings.Contains(string(b), "after GC:  heap in use") {
		t.Fatalf("memstats: %s", b)
	}
}

func TestWriteDebugMetadata(t *testing.T) {
	const note = "llama.cpp metadata local/m: /models probe failed; using configured values"
	dir, err := writeDebug(filepath.Join(t.TempDir(), "d"), note)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "model-metadata.txt"))
	if err != nil || string(data) != note+"\n" {
		t.Fatalf("metadata diagnostics: %q, %v", data, err)
	}
}
