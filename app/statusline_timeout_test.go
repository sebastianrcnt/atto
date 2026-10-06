//go:build !windows

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/shell"
)

func TestStatusCommandTimeoutKillsPipeHoldingChild(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	start := time.Now()
	_, err := runStatusCommand(fmt.Sprintf("sleep 30 & echo $! > %q; wait", pidFile), nil, dir)
	if err == nil {
		t.Fatal("command did not time out")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	// Reaping may lag the group signal slightly.
	for deadline := time.Now().Add(time.Second); shell.Alive(pid) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if shell.Alive(pid) {
		shell.KillGroup(pid)
		t.Fatalf("status child %d survived", pid)
	}
}
