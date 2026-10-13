package jobs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/shell"
)

func persistenceJob(t *testing.T) (Job, string) {
	t.Helper()
	t.Setenv("ATTO_DIR", t.TempDir())
	id, dir, err := newDir("persist")
	if err != nil {
		t.Fatal(err)
	}
	return Job{ID: id, Session: "persist", Cwd: t.TempDir(), Status: Starting, Started: time.Now()}, dir
}

// A directory in place of job.json deterministically rejects atomic replacement
// on every OS, without relying on chmod (which root and Windows can bypass).
func blockJobState(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, "job.json"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestSaveReportsMarshalFailureWithoutReplacingState(t *testing.T) {
	j, dir := persistenceJob(t)
	if err := save(dir, j); err != nil {
		t.Fatal(err)
	}
	j.Started = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	err := save(dir, j)
	if err == nil || !strings.Contains(err.Error(), "saving job") || !strings.Contains(err.Error(), "job.json") {
		t.Fatalf("marshal failure: %v", err)
	}
	got, err := load(dir)
	if err != nil || got.Started.Year() == 10000 {
		t.Fatalf("last valid state was damaged: %+v, %v", got, err)
	}
}

func TestSaveReportsWriteFailure(t *testing.T) {
	j, dir := persistenceJob(t)
	blockJobState(t, dir)
	err := save(dir, j)
	var linkErr *os.LinkError
	if err == nil || !errors.As(err, &linkErr) || !strings.Contains(err.Error(), "saving job") {
		t.Fatalf("write failure: %v", err)
	}
}

// Re-executed by run; kept portable rather than depending on /bin/sh or sleep.
func TestStateFailureChild(t *testing.T) {
	if os.Getenv("ATTO_TEST_STATE_FAILURE_CHILD") == "1" {
		time.Sleep(time.Minute)
	}
}

func TestRunningStateFailureKillsAndReapsChild(t *testing.T) {
	j, dir := persistenceJob(t)
	blockJobState(t, dir)
	t.Setenv("ATTO_TEST_STATE_FAILURE_CHILD", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	j.Args = []string{exe, "-test.run=^TestStateFailureChild$"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	code, _, err := run(ctx, dir, &j, "", io.Discard, true)
	if err == nil || code != -1 || !strings.Contains(err.Error(), "saving job") {
		t.Fatalf("run: code %d, job %+v, error %v", code, j, err)
	}
	if j.PID <= 0 || shell.Alive(j.PID) {
		t.Fatalf("child was not reaped: pid %d", j.PID)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("state failure waited for child timeout instead of killing it")
	}
}

func TestCommandStartAndStateFailuresAreBothReported(t *testing.T) {
	j, dir := persistenceJob(t)
	blockJobState(t, dir)
	j.Args = []string{filepath.Join(t.TempDir(), "missing-command")}
	code, detail, err := run(context.Background(), dir, &j, "", io.Discard, true)
	if code != -2 || j.Status != Failed || !strings.Contains(detail, "missing-command") || err == nil ||
		!strings.Contains(err.Error(), "missing-command") || !strings.Contains(err.Error(), "saving job") {
		t.Fatalf("run: %d, %q, %v", code, detail, err)
	}
}

func TestMonitorStateFailureDoesNotStartCheck(t *testing.T) {
	j, dir := persistenceJob(t)
	blockJobState(t, dir)
	j.Monitor = &Monitor{Every: time.Hour, Until: "READY"}
	j.Args = []string{filepath.Join(t.TempDir(), "must-not-start")}
	code, _, err := superviseMonitor(context.Background(), dir, &j, io.Discard)
	if code != -1 || err == nil || !strings.Contains(err.Error(), "saving job") {
		t.Fatalf("monitor: %d, %v", code, err)
	}
}

func TestFinishStateFailureDoesNotPostSuccessEvent(t *testing.T) {
	j, dir := persistenceJob(t)
	blockJobState(t, dir)
	if err := finish(dir, j, 0, "", false); err == nil {
		t.Fatal("finish discarded state error")
	}
	if got := events.Drain(j.Session); len(got) != 0 {
		t.Fatalf("false success event: %+v", got)
	}
}

func TestFinishReturnsExitEventFailureButKeepsFinalState(t *testing.T) {
	j, dir := persistenceJob(t)
	if err := os.MkdirAll(filepath.Dir(events.Dir(j.Session)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(events.Dir(j.Session), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finish(dir, j, 7, "", false); err == nil || !strings.Contains(err.Error(), "exit event") {
		t.Fatalf("event failure: %v", err)
	}
	got, err := load(dir)
	if err != nil || got.Status != Exited || got.ExitCode == nil || *got.ExitCode != 7 {
		t.Fatalf("final state: %+v, %v", got, err)
	}
}

func TestSuperviseReportsFailureEvenWhenLogCannotOpen(t *testing.T) {
	j, dir := persistenceJob(t)
	if err := save(dir, j); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "output.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Supervise(dir); err == nil || !strings.Contains(err.Error(), "output.log") {
		t.Fatalf("supervisor error: %v", err)
	}
	got := events.Drain(j.Session)
	if len(got) != 1 || !strings.Contains(got[0].Text, "supervision failed") || !strings.Contains(got[0].Text, "output.log") {
		t.Fatalf("missing independent failure event: %+v", got)
	}
}

func TestSupervisionFailureLogsAndPreservesQuietPolicy(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "silent"}[quiet], func(t *testing.T) {
			j, dir := persistenceJob(t)
			blockJobState(t, dir)
			j.Silent = quiet
			if err := reportSupervisionError(dir, j, errors.New("state unavailable")); err != nil {
				t.Fatal(err)
			}
			log, err := os.ReadFile(filepath.Join(dir, "output.log"))
			if err != nil || !strings.Contains(string(log), "state unavailable") {
				t.Fatalf("failure log: %q, %v", log, err)
			}
			got := events.Drain(j.Session)
			if len(got) != 1 || got[0].Quiet != quiet || !strings.Contains(got[0].Text, "state unavailable") {
				t.Fatalf("failure event: %+v", got)
			}
		})
	}
}
