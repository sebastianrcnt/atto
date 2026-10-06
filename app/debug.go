package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
)

// cmdDebug saves a heap profile, the goroutines and the memory statistics
// of this atto to ~/.atto/debug/<time>/, for `go tool pprof` (or an agent)
// to read, and the latest model requests under requests/. Nothing leaves
// the machine.
func (a *App) cmdDebug(string) {
	dir, err := writeDebug(filepath.Join(config.Dir(), "debug", time.Now().Format("20060102-150405")))
	if err != nil {
		a.notice("debug: %v", err)
		return
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	a.notice("Saved a heap profile to %s\n%s", shortPath(dir), memSummary(&m))
}

// writeDebug writes heap.pprof, goroutines.txt and memstats.txt to dir.
func writeDebug(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	runtime.GC() // the profile shows what is live, not garbage not yet collected
	write := func(name string, fn func(*os.File) error) error {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		defer f.Close()
		return fn(f)
	}
	if err := write("heap.pprof", func(f *os.File) error { return pprof.Lookup("heap").WriteTo(f, 0) }); err != nil {
		return "", err
	}
	if err := writeRequests(filepath.Join(dir, "requests")); err != nil {
		return "", err
	}
	if err := write("goroutines.txt", func(f *os.File) error { return pprof.Lookup("goroutine").WriteTo(f, 1) }); err != nil {
		return "", err
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	err := write("memstats.txt", func(f *os.File) error {
		_, err := fmt.Fprintf(f, "before GC: %s\nafter GC:  %s\n", memSummary(&before), memSummary(&after))
		return err
	})
	return dir, err
}

// memSummary is the memory figures that matter: what the program holds
// (heap in use), what the heap has from the OS, and everything the Go
// runtime has from the OS (close to the process's size).
func memSummary(m *runtime.MemStats) string {
	mb := func(b uint64) string { return fmt.Sprintf("%.1f MB", float64(b)/(1<<20)) }
	return fmt.Sprintf("heap in use %s · heap from the OS %s · total from the OS %s · %d goroutines · %d GCs",
		mb(m.HeapInuse), mb(m.HeapSys), mb(m.Sys), runtime.NumGoroutine(), m.NumGC)
}

// writeRequests saves the latest model requests (recent-N.json) and the
// pinned ones (compaction-N.json: the requests around the last
// compaction), and index.txt, which says for each request whether its
// messages continue the previous request's: where they first differ is
// where a server's prefix cache stops helping.
func writeRequests(dir string) error {
	sets := map[string][]ai.SentRequest{"recent": ai.RecentRequests()}
	maps.Copy(sets, ai.PinnedRequests())
	names := make([]string, 0, len(sets))
	for k := range sets {
		names = append(names, k)
	}
	sort.Strings(names)
	var index strings.Builder
	for _, name := range names {
		reqs := sets[name]
		if len(reqs) == 0 {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		var prev []json.RawMessage
		var prevTools json.RawMessage
		for i, r := range reqs {
			file := fmt.Sprintf("%s-%d.json", name, i+1)
			if err := os.WriteFile(filepath.Join(dir, file), r.Body, 0o600); err != nil {
				return err
			}
			msgs := requestMessages(r.Body)
			fmt.Fprintf(&index, "%s  %s  %s  %d bytes, %d messages", file, r.Time.Format("15:04:05"), r.URL, len(r.Body), len(msgs))
			tools := requestTools(r.Body)
			if prev != nil {
				fmt.Fprintf(&index, "; %s", prefixNote(prev, msgs))
				if !sameJSON(prevTools, tools) {
					index.WriteString("; tools differ")
				}
			}
			index.WriteString("\n")
			prev, prevTools = msgs, tools
		}
	}
	if index.Len() == 0 {
		return nil
	}
	return os.WriteFile(filepath.Join(dir, "index.txt"), []byte(index.String()), 0o600)
}

// requestMessages is a request body's messages (chat completions) or input
// (responses), each as its JSON.
func requestMessages(body []byte) []json.RawMessage {
	var b struct {
		Messages []json.RawMessage `json:"messages"`
		Input    []json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(body, &b)
	if b.Messages != nil {
		return b.Messages
	}
	return b.Input
}

// requestTools is a request body's tools, which servers render before the
// messages.
func requestTools(body []byte) json.RawMessage {
	var b struct {
		Tools json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Tools
}

// prefixNote says whether cur continues prev: every earlier message the
// same, then more.
func prefixNote(prev, cur []json.RawMessage) string {
	for i := range prev {
		if i >= len(cur) {
			return fmt.Sprintf("shorter than the previous request (%d of its %d messages)", len(cur), len(prev))
		}
		if !sameJSON(prev[i], cur[i]) {
			return fmt.Sprintf("differs from the previous request at message %d", i)
		}
	}
	return fmt.Sprintf("continues the previous request (+%d messages)", len(cur)-len(prev))
}

// sameJSON compares two JSON values ignoring key order and spacing.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(x, y)
}
