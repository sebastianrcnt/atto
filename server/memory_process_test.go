//go:build !race

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/internal/sessionfixture"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
)

type memoryResult struct {
	Operation           string `json:"operation"`
	FileBytes           int64  `json:"fileBytes"`
	SnapshotBytes       int64  `json:"snapshotBytes"`
	HeapInuse           uint64 `json:"heapInuse"`
	PeakHeapInuse       uint64 `json:"peakHeapInuse"`
	PeakHeapSys         uint64 `json:"peakHeapSys"`
	RSS                 uint64 `json:"rss"`
	MaxRSS              uint64 `json:"maxRSS"`
	BeforeFreeHeapInuse uint64 `json:"beforeFreeHeapInuse,omitempty"`
}

type byteCount int64

func (w *byteCount) Write(b []byte) (int, error) { *w += byteCount(len(b)); return len(b), nil }

func sampleHeap() func() (uint64, uint64) {
	done := make(chan struct{})
	result := make(chan [2]uint64)
	go func() {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		var peaks [2]uint64
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			peaks[0] = max(peaks[0], m.HeapInuse)
			peaks[1] = max(peaks[1], m.HeapSys)
			select {
			case <-done:
				result <- peaks
				return
			case <-tick.C:
			}
		}
	}()
	return func() (uint64, uint64) { close(done); p := <-result; return p[0], p[1] }
}

func currentRSS() uint64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", fmt.Sprint(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	var kib uint64
	fmt.Sscan(strings.TrimSpace(string(out)), &kib)
	return kib * 1024
}

// Each operation is measured in a fresh process so getrusage's lifetime peak
// cannot hide an earlier operation's cost. These are real worker runtimes with
// the normal agent/hooks/extensions, not just JSON parser microbenchmarks.
func TestLongSessionProcessMemory(t *testing.T) {
	if os.Getenv("ATTO_MEMORY_CLIENT") != "" {
		runMemoryClient(t)
		return
	}
	operation := os.Getenv("ATTO_MEMORY_OPERATION")
	if operation == "" {
		for _, op := range []string{"baseline-open", "baseline-attach", "open", "attach", "page", "compact", "tree", "tree-unlimited"} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLongSessionProcessMemory$", "-test.v")
			cmd.Env = append(os.Environ(), "ATTO_MEMORY_OPERATION="+op)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v\n%s", op, err, out)
			}
			for line := range strings.SplitSeq(string(out), "\n") {
				if strings.Contains(line, "MEMORY ") {
					t.Log(line)
				}
			}
		}
		return
	}
	s, _ := testServer(t, providertest.Reply{Text: "Compacted synthetic memory fixture notes."})
	if strings.HasPrefix(operation, "baseline-") || strings.HasSuffix(operation, "-unlimited") {
		debug.SetMemoryLimit(math.MaxInt64)
	}
	path, id := sessionfixture.Write(t, s.Cwd)
	st, _ := os.Stat(path)
	ctx := context.Background()
	var info ThreadInfo
	open := func() {
		out, err := s.resumeThread("measurement", threadParams{ThreadID: id, DeferStart: true})
		if err != nil {
			t.Fatal(err)
		}
		info = out.(ThreadInfo)
	}
	if operation != "open" && operation != "baseline-open" {
		open()
	}
	baseline := strings.HasPrefix(operation, "baseline-")
	rebuildFull := func() {
		loaded, err := session.ReadActive(path)
		if err != nil {
			t.Fatal(err)
		}
		th, _ := s.thread(id)
		if err := th.call(func() error { th.tr.MaxItems = 0; th.replayKeepNotices(loaded.Entries); th.hasMore = false; return nil }); err != nil {
			t.Fatal(err)
		}
		loaded.Entries = nil
	}
	if operation == "baseline-attach" {
		rebuildFull()
	}
	var listener net.Listener
	if operation != "open" && operation != "baseline-open" {
		var err error
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				if baseline {
					// Reproduce the previous connection encoder's entire-value buffer.
					go func() {
						defer conn.Close()
						dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
						for {
							var raw json.RawMessage
							if dec.Decode(&raw) != nil {
								return
							}
							if reply := s.Handle(ctx, raw); reply != nil {
								if enc.Encode(reply) != nil {
									return
								}
							}
						}
					}()
				} else {
					go s.ServeConn(ctx, conn)
				}
			}
		}()
	}
	debug.FreeOSMemory()
	stop := sampleHeap()
	var payload any
	var bytes byteCount
	var child *exec.Cmd
	var release io.WriteCloser
	if operation == "open" || operation == "baseline-open" {
		open()
		if baseline {
			rebuildFull()
		}
		payload = info
		if err := json.NewEncoder(&bytes).Encode(payload); err != nil {
			t.Fatal(err)
		}
	} else {
		child = exec.Command(os.Args[0], "-test.run=^TestLongSessionProcessMemory$", "-test.v")
		child.Env = append(os.Environ(), "ATTO_MEMORY_CLIENT="+strings.TrimSuffix(operation, "-unlimited"), "ATTO_MEMORY_ADDRESS="+listener.Addr().String(), "ATTO_MEMORY_THREAD="+id)
		stdout, err := child.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		release, err = child.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(stdout)
		found := false
		for scanner.Scan() {
			if line, ok := strings.CutPrefix(scanner.Text(), "CLIENT_BYTES "); ok {
				n, err := strconv.ParseInt(line, 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				bytes = byteCount(n)
				found = true
				break
			}
		}
		if !found {
			_ = release.Close()
			err := child.Wait()
			t.Fatalf("measurement client failed: %v", err)
		}
	}
	inuse, sys := stop()
	var beforeFree runtime.MemStats
	runtime.ReadMemStats(&beforeFree)
	inuse, sys = max(inuse, beforeFree.HeapInuse), max(sys, beforeFree.HeapSys)
	debug.FreeOSMemory()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	result := memoryResult{Operation: operation, FileBytes: st.Size(), SnapshotBytes: int64(bytes), HeapInuse: m.HeapInuse, PeakHeapInuse: inuse, PeakHeapSys: sys, RSS: currentRSS(), MaxRSS: maxRSS(), BeforeFreeHeapInuse: beforeFree.HeapInuse}
	runtime.KeepAlive(payload)
	raw, _ := json.Marshal(result)
	t.Logf("MEMORY %s", raw)
	if release != nil {
		_ = release.Close()
		if err := child.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if !baseline && !strings.HasSuffix(operation, "-unlimited") {
		if result.PeakHeapInuse > 128<<20 {
			t.Fatalf("worker peak heap exceeded 128 MiB: %+v", result)
		}
		if result.MaxRSS > 150<<20 {
			t.Fatalf("worker peak RSS exceeded 150 MiB: %+v", result)
		}
		if result.RSS > 80<<20 {
			t.Fatalf("worker steady RSS exceeded 80 MiB: %+v", result)
		}
	}
}

func runMemoryClient(t *testing.T) {
	operation := os.Getenv("ATTO_MEMORY_CLIENT")
	conn, err := net.Dial("tcp", os.Getenv("ATTO_MEMORY_ADDRESS"))
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(conn)
	defer c.Close()
	ctx := context.Background()
	version := 3
	if strings.HasPrefix(operation, "baseline-") {
		version = 2
	}
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{version}}, nil); err != nil {
		t.Fatal(err)
	}
	id := os.Getenv("ATTO_MEMORY_THREAD")
	var info ThreadInfo
	if err := c.Call(ctx, "thread/attach", map[string]any{"threadId": id}, &info); err != nil {
		t.Fatal(err)
	}
	var payload any = info
	switch operation {
	case "page":
		var page ItemPage
		if err := c.Call(ctx, "thread/items", map[string]any{"threadId": id, "before": info.Before, "limit": 200}, &page); err != nil {
			t.Fatal(err)
		}
		payload = page
	case "compact":
		if err := c.Call(ctx, "thread/compact", map[string]any{"threadId": id}, nil); err != nil {
			t.Fatal(err)
		}
		done := false
		deadline := time.After(10 * time.Second)
		for !done {
			select {
			case n := <-c.Events():
				done = n.Method == "turn/completed"
			case <-deadline:
				t.Fatal("compaction timed out")
			}
		}
		if err := c.Call(ctx, "thread/read", map[string]any{"threadId": id}, &info); err != nil {
			t.Fatal(err)
		}
		payload = info
	case "tree":
		var tree struct {
			Entries []session.Entry `json:"entries"`
		}
		if err := c.Call(ctx, "thread/tree", map[string]any{"threadId": id}, &tree); err != nil {
			t.Fatal(err)
		}
		target := ""
		for _, e := range tree.Entries {
			if e.Type == session.TypeCompaction {
				target = e.ID
				break
			}
		}
		if err := c.Call(ctx, "thread/navigate", map[string]any{"threadId": id, "entryId": target}, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Call(ctx, "thread/read", map[string]any{"threadId": id}, &info); err != nil {
			t.Fatal(err)
		}
		payload = info
	}
	var bytes byteCount
	if err := json.NewEncoder(&bytes).Encode(payload); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("CLIENT_BYTES %d\n", bytes)
	_, _ = io.Copy(io.Discard, os.Stdin) // Keep attached while the worker is sampled.
}
