package fsutil

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Readers never fail because a writer replaces the file at that moment (on
// Windows an open then fails with a sharing violation).
func TestReadFileWhileReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := WriteAtomic(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	var failed atomic.Value
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for !stop.Load() {
				if b, err := ReadFile(path); err != nil {
					failed.CompareAndSwap(nil, err.Error())
				} else if s := string(b); s != "old" && s != "new" {
					failed.CompareAndSwap(nil, "read "+s)
				}
			}
		})
	}
	for end := time.Now().Add(time.Second); time.Now().Before(end); {
		if err := WriteAtomic(path, []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()
	if msg := failed.Load(); msg != nil {
		t.Fatal(msg)
	}
}
