//go:build !race

package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider"
)

// A synthetic 65 MB transcript with the measured session's entry count and
// compactions. Generation streams directly to disk so setup is not the peak.
func writeMemorySession(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "large.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(Entry{Type: TypeSession, ID: "memory"}); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("source line and tool output; ", 76)
	for i := range 29151 {
		e := Entry{Type: TypeMessage, ID: fmt.Sprint(i), Parent: fmt.Sprint(i - 1), Message: &provider.Message{Role: "assistant", Content: text}}
		if i%1160 == 0 && i > 0 {
			e.Type, e.Message = TypeCompaction, nil
			e.Replacement = []provider.Message{{Role: "user", Content: "compacted notes"}}
		}
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadContextLargeSessionMemory(t *testing.T) {
	path := writeMemorySession(t)
	debug.FreeOSMemory()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	done, peaks := make(chan struct{}), make(chan runtime.MemStats)
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		var peak runtime.MemStats
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			peak.HeapInuse = max(peak.HeapInuse, m.HeapInuse)
			peak.HeapSys = max(peak.HeapSys, m.HeapSys)
			select {
			case <-done:
				peaks <- peak
				return
			case <-ticker.C:
			}
		}
	}()
	active, err := ReadContext(path)
	close(done)
	peak := <-peaks
	if err != nil {
		t.Fatal(err)
	}
	debug.FreeOSMemory()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(active)
	size, _ := os.Stat(path)
	t.Logf("file=%d entries=%d retained heap delta=%d HeapInuse=%d peak HeapInuse=%d peak HeapSys=%d", size.Size(), len(active.Entries), int64(after.HeapAlloc)-int64(before.HeapAlloc), after.HeapInuse, peak.HeapInuse, peak.HeapSys)
	if after.HeapAlloc > before.HeapAlloc+8<<20 {
		t.Fatalf("context retained pre-compaction messages: heap grew by %d", after.HeapAlloc-before.HeapAlloc)
	}
	if peak.HeapInuse > before.HeapInuse+48<<20 {
		t.Fatalf("opening materialized historical text: peak heap grew by %d", peak.HeapInuse-before.HeapInuse)
	}
}
