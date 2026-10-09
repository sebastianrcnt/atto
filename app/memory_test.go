//go:build !race

package app

import (
	"context"
	"fmt"
	"github.com/sebastianrcnt/atto/server"
	"math"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/internal/sessionfixture"
)

func TestTUILongSessionAttachMemory(t *testing.T) {
	operation := os.Getenv("ATTO_TUI_MEMORY")
	if operation == "" {
		for _, mode := range []string{"baseline", "tail"} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestTUILongSessionAttachMemory$", "-test.v")
			cmd.Env = append(os.Environ(), "ATTO_TUI_MEMORY="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("TUI memory: %v\n%s", err, out)
			}
			t.Log(string(out))
		}
		return
	}

	cwd, _ := testEnv(t)
	path, id := sessionfixture.Write(t, cwd)
	debug.FreeOSMemory()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	a := startAppRuntime(t, cwd, nullTerm{}, true, Options{Session: id})
	if operation == "baseline" {
		debug.SetMemoryLimit(math.MaxInt64)
		if err := a.conn.c.Call(context.Background(), "initialize", map[string]any{"protocolVersions": []int{2}}, nil); err != nil {
			t.Fatal(err)
		}
		var full server.ThreadInfo
		if err := a.conn.c.Call(context.Background(), "thread/read", map[string]any{"threadId": id}, &full); err != nil {
			t.Fatal(err)
		}
		a.ui.Do(func() { a.applySnapshot(full) })
	}
	a.ui.Do(func() { _ = a.ui.Body.Render(100) })
	debug.FreeOSMemory()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	var rss uint64
	if out, err := exec.Command("ps", "-o", "rss=", "-p", fmt.Sprint(os.Getpid())).Output(); err == nil {
		fmt.Sscan(strings.TrimSpace(string(out)), &rss)
		rss *= 1024
	}
	if profile := os.Getenv("ATTO_TUI_MEMORY_PROFILE"); profile != "" {
		f, err := os.Create(profile)
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	st, _ := os.Stat(path)
	t.Logf("TUI %s file=%d loadedItems=%d HeapAlloc=%d HeapInuse=%d HeapSys=%d heap delta=%d RSS=%d (in-process runtime included)", operation, st.Size(), len(a.view.Items), after.HeapAlloc, after.HeapInuse, after.HeapSys, int64(after.HeapAlloc)-int64(before.HeapAlloc), rss)
	if operation != "baseline" && after.HeapAlloc > 100<<20 {
		t.Fatal("TUI attach heap exceeds 100 MiB")
	}
	if operation != "baseline" && len(a.view.Items) > 200 {
		t.Fatal("TUI attach loaded full transcript")
	}
	runtime.KeepAlive(a)
}
