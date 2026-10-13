//go:build !windows

package jobs

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestMonitorOverallTimeoutBoundsRunningCheck(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	id, dir, err := newDir("s")
	if err != nil {
		t.Fatal(err)
	}
	j := Job{ID: id, Session: "s", Cwd: t.TempDir(), Command: "sleep 10", Monitor: &Monitor{Every: time.Hour, Until: "READY", Timeout: 150 * time.Millisecond}}
	start := time.Now()
	code, detail, err := superviseMonitor(context.Background(), dir, &j, io.Discard)
	if err != nil || code != 1 || !strings.Contains(detail, "timed out") {
		t.Fatalf("monitor: %d, %q, %v", code, detail, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout waited for check: %s", elapsed)
	}
}

func TestMonitorOverallTimeoutBoundsIntervalWait(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	id, dir, err := newDir("s")
	if err != nil {
		t.Fatal(err)
	}
	j := Job{ID: id, Session: "s", Cwd: t.TempDir(), Command: "echo waiting", Monitor: &Monitor{Every: time.Hour, Until: "READY", Timeout: 150 * time.Millisecond}}
	start := time.Now()
	code, detail, err := superviseMonitor(context.Background(), dir, &j, io.Discard)
	if err != nil || code != 1 || !strings.Contains(detail, "timed out") {
		t.Fatalf("monitor: %d, %q, %v", code, detail, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout waited for interval: %s", elapsed)
	}
}
