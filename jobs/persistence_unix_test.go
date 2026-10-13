//go:build !windows

package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/shell"
)

func readOnlyJobDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if f, err := os.CreateTemp(dir, ".probe"); err == nil {
		f.Close()
		os.Remove(f.Name())
		t.Skip("this user can write read-only directories (e.g. root)")
	}
}

func TestGetReturnsLostStateWriteFailure(t *testing.T) {
	for _, status := range []Status{Starting, Running} {
		t.Run(string(status), func(t *testing.T) {
			j, dir := persistenceJob(t)
			j.Status = status
			j.Started = time.Now().Add(-2 * startGrace)
			if status == Running {
				j.SupervisorPID = 1 << 30
				if shell.Alive(j.SupervisorPID) {
					t.Skip("test PID unexpectedly exists")
				}
			}
			if err := save(dir, j); err != nil {
				t.Fatal(err)
			}
			readOnlyJobDir(t, dir)
			got, err := Get(j.Session, j.ID)
			if err == nil || got.Status != Lost || !strings.Contains(err.Error(), "saving job") {
				t.Fatalf("lost state update: %+v, %v", got, err)
			}
			list, err := ListWithErrors(j.Session)
			if err == nil || len(list) != 1 || list[0].Status != Lost {
				t.Fatalf("lost job disappeared: %+v, %v", list, err)
			}
			if len(List(j.Session)) != 1 {
				t.Fatal("best-effort list discarded a readable job")
			}
		})
	}
}

func TestLaunchReturnsEarlySupervisorFailure(t *testing.T) {
	j, dir := persistenceJob(t)
	if err := os.Mkdir(filepath.Join(dir, "output.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := launch(dir, j, nil)
	if err == nil || !strings.Contains(err.Error(), "supervisor failed") || got.SupervisorPID == 0 {
		t.Fatalf("launch: %+v, %v", got, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("launch ignored the exited supervisor until its startup timeout")
	}
	if pending := events.Drain(j.Session); len(pending) != 1 || !strings.Contains(pending[0].Text, "supervision failed") {
		t.Fatalf("launch failure event: %+v", pending)
	}
}
