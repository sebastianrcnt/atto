package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
)

// A turn run by a worker is a job like another: listed, with the same label
// and kind, active while the worker lives, and ended by the worker.
func TestWorkerTurnJobLifecycle(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	ctl := Control{File: filepath.Join(t.TempDir(), "interrupt"), Content: "3"}
	j, err := StartWorkerTurn("parent1", t.TempDir(), "agent tests", os.Getpid(), ctl, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Get("parent1", j.ID)
	if err != nil || got.Kind() != "agent" || got.Label() != "agent tests" || !got.Active() || !got.InWorker() || got.SupervisorPID != os.Getpid() {
		t.Fatalf("job %+v %v", got, err)
	}
	if list := List("parent1"); len(list) != 1 || list[0].ID != j.ID {
		t.Fatalf("list %+v", list)
	}
	if out, err := Tail("parent1", j.ID, 5); err != nil || !strings.Contains(out, "runs in the worker") {
		t.Fatalf("output %q %v", out, err)
	}
	if err := EndWorkerTurn("parent1", j.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ = Get("parent1", j.ID)
	if got.Active() || got.Status != Exited || got.ExitCode == nil || *got.ExitCode != 0 || got.Ended == nil {
		t.Fatalf("ended %+v", got)
	}
	// Ending twice changes nothing; a stopped turn is killed.
	if err := EndWorkerTurn("parent1", j.ID, true); err != nil {
		t.Fatal(err)
	}
	if again, _ := Get("parent1", j.ID); again.Status != Exited {
		t.Fatalf("ended job changed: %+v", again)
	}
	k, _ := StartWorkerTurn("parent1", t.TempDir(), "agent b", os.Getpid(), ctl, false)
	_ = EndWorkerTurn("parent1", k.ID, true)
	if got, _ := Get("parent1", k.ID); got.Status != Killed {
		t.Fatalf("stopped %+v", got)
	}
}

// A turn whose worker died is lost, as a supervisor that vanished is.
func TestWorkerTurnJobIsLostWithItsWorker(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	j, err := StartWorkerTurn("p", t.TempDir(), "agent x", 2147483000, Control{File: filepath.Join(t.TempDir(), "i"), Content: "1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := Get("p", j.ID); got.Status != Lost {
		t.Fatalf("job of a dead worker: %+v", got)
	}
}

// Killing such a job asks the worker, it never signals the worker's process.
func TestKillAsksTheWorkerToStopTheTurn(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	file := filepath.Join(t.TempDir(), "interrupt")
	j, err := StartWorkerTurn("p", t.TempDir(), "agent x", os.Getpid(), Control{File: file, Content: "7"}, false)
	if err != nil {
		t.Fatal(err)
	}
	// The worker answers the request by ending the turn.
	go func() {
		for range 200 {
			if b, err := os.ReadFile(file); err == nil && string(b) == "7" {
				_ = EndWorkerTurn("p", j.ID, true)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	got, err := Kill("p", j.ID)
	if err != nil || got.Status != Killed {
		t.Fatalf("kill: %+v %v", got, err)
	}
	// One that ignores the request is reported, not forced.
	old := controlWait
	controlWait = 100 * time.Millisecond
	t.Cleanup(func() { controlWait = old })
	k, _ := StartWorkerTurn("p", t.TempDir(), "agent y", os.Getpid(), Control{File: filepath.Join(t.TempDir(), "i2"), Content: "1"}, false)
	if got, err := Kill("p", k.ID); err == nil || !got.Active() || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("an unresponsive worker: %+v %v", got, err)
	}
	// Silent jobs post no event when they end.
	s, _ := StartWorkerTurn("p", t.TempDir(), "agent z", os.Getpid(), Control{File: filepath.Join(t.TempDir(), "i3"), Content: "1"}, true)
	_ = EndWorkerTurn("p", s.ID, false)
	if evs := events.Drain("p"); len(evs) != 0 {
		t.Fatalf("events %+v", evs)
	}
	if got, _ := Get("p", s.ID); !got.Silent {
		t.Fatalf("silent: %+v", got)
	}
}
