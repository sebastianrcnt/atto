//go:build !windows

package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
)

// bgSession gives a test its own atto dir and session, and stops every
// job the test leaves behind.
func bgSession(t *testing.T) (string, []string) {
	t.Setenv(config.EnvDir, t.TempDir())
	s := "bg-" + strings.ReplaceAll(t.Name(), "/", "-")
	t.Cleanup(func() { jobs.KillAll(s) })
	return s, []string{"ATTO_SESSION_ID=" + s}
}

// waitEvent waits for the session's next inbox events.
func waitEvent(t *testing.T, s string) []events.Event {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if evs := events.Drain(s); len(evs) > 0 {
			return evs
		}
	}
	t.Fatal("no event")
	return nil
}

// groupGone reports whether no process of group pgid is left.
func groupGone(pgid int) bool {
	return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
}

func TestRunInBackgroundStartsJob(t *testing.T) {
	s, env := bgSession(t)
	args := BashArgs{Description: "Count", Command: "echo hi; sleep 0.3; echo bye; exit 2", Background: true}
	start := time.Now()
	res := RunBash(context.Background(), t.TempDir(), env, args, nil)
	if res.Err != nil || res.Job != 1 || res.Background != BackgroundRequested || time.Since(start) > 3*time.Second {
		t.Fatalf("%+v", res)
	}
	out := res.ForModel(args)
	for _, want := range []string{"started in the background as job 1", "[atto event]", "atto job output 1", "atto job kill 1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("model output %q lacks %q", out, want)
		}
	}
	j, why, err := jobs.Wait(s, 1, 10*time.Second)
	if err != nil || why != "done" || j.Status != jobs.Exited || *j.ExitCode != 2 || j.Name != "Count" {
		t.Fatalf("job %+v %s %v", j, why, err)
	}
	evs := waitEvent(t, s)
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "exited with code 2") || !strings.Contains(evs[0].Text, "bye") {
		t.Fatalf("event %+v", evs)
	}
}

func TestRunInBackgroundNeedsSession(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	res := RunBash(context.Background(), t.TempDir(), nil, BashArgs{Command: "true", Background: true}, nil)
	if res.Err == nil || res.Job != 0 {
		t.Fatalf("%+v", res)
	}
}

// A command still running after its foreground wait becomes a job: the same
// process, its output so far reported and kept in the job log, which goes
// on to the end.
func TestTimeoutMovesCommandToBackground(t *testing.T) {
	s, env := bgSession(t)
	args := BashArgs{Description: "Slow", Command: "echo before; sleep 3; echo after; exit 4", Timeout: 1}
	var streamed strings.Builder
	start := time.Now()
	res := RunBash(context.Background(), t.TempDir(), env, args, func(c string) { streamed.WriteString(c) })
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Fatalf("returned after %s, not at the timeout", d)
	}
	if res.Job != 1 || res.Background != BackgroundTimeout || res.WaitLimit != time.Second || res.TimedOut || res.Output != "before\n" || streamed.String() != "before\n" {
		t.Fatalf("%+v (streamed %q)", res, streamed.String())
	}
	out := res.ForModel(args)
	if !strings.HasPrefix(out, "before\n[still running after 1s; moved to the background as job 1.") || !strings.Contains(out, "[atto event]") {
		t.Fatalf("model output %q", out)
	}
	lines := strings.Split(out, "\n")
	if !BackgroundStatus(lines[len(lines)-1], 1) || BackgroundStatus(lines[len(lines)-1], 2) {
		t.Fatalf("status line not recognized: %q", lines[len(lines)-1])
	}
	j, err := jobs.Get(s, 1)
	if err != nil || j.Status != jobs.Running || j.Name != "Slow" || j.PID == 0 || !j.Started.Before(start.Add(100*time.Millisecond)) {
		t.Fatalf("job %+v %v", j, err)
	}
	j, why, err := jobs.Wait(s, 1, 10*time.Second)
	if err != nil || why != "done" || j.Status != jobs.Exited || *j.ExitCode != 4 {
		t.Fatalf("job %+v %s %v", j, why, err)
	}
	if log, _ := jobs.Tail(s, 1, 10); log != "before\nafter" {
		t.Fatalf("job log %q", log)
	}
	evs := waitEvent(t, s)
	if len(evs) != 1 || !events.Wakes(evs) || !strings.Contains(evs[0].Text, "exited with code 4") || !strings.Contains(evs[0].Text, "after") {
		t.Fatalf("event %+v", evs)
	}
}

// Without a session there is nowhere to keep a job: the timeout kills.
func TestTimeoutWithoutSessionKills(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	res := RunBash(context.Background(), t.TempDir(), nil, BashArgs{Command: "sleep 30", Timeout: 1}, nil)
	if !res.TimedOut || res.Job != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestKillBackgroundedCommandStopsTree(t *testing.T) {
	s, env := bgSession(t)
	res := RunBash(context.Background(), t.TempDir(), env, BashArgs{Command: "sleep 30 & sleep 30", Timeout: 1}, nil)
	if res.Job != 1 {
		t.Fatalf("%+v", res)
	}
	j, _ := jobs.Get(s, 1)
	if groupGone(j.PID) {
		t.Fatal("the command should still run")
	}
	j, err := jobs.Kill(s, 1)
	if err != nil || j.Status != jobs.Killed {
		t.Fatalf("kill: %+v %v", j, err)
	}
	for deadline := time.Now().Add(3 * time.Second); !groupGone(j.PID); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the command's group survived the kill")
		}
	}
	time.Sleep(200 * time.Millisecond)
	if evs := events.Drain(s); len(evs) != 0 {
		t.Fatalf("killed jobs post no events: %+v", evs)
	}
}

// Ctrl+B goes through Agent.Background: the model gets the job in the
// tool result, the turn goes on, and the exit arrives as an event.
func TestAgentBackgroundMovesRunningCommand(t *testing.T) {
	s, env := bgSession(t)
	srv, seen := fakeServer(t, toolCall("echo started; sleep 3; echo finished"), text("ok"))
	a := newTestAgent(srv.URL)
	a.SetSession(s, env)
	if a.Background() {
		t.Fatal("nothing runs yet")
	}
	var end ToolEnd
	start := time.Now()
	err := a.Run(context.Background(), "go", func(ev any) {
		switch e := ev.(type) {
		case ToolOutput:
			if strings.Contains(e.Chunk, "started") && !a.Background() {
				t.Error("Background should find the running command")
			}
		case ToolEnd:
			end = e
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Fatalf("the turn waited for the command: %s", d)
	}
	if end.Result.Job != 1 || end.Result.Background != BackgroundUser || !strings.Contains(end.Text, "the user moved this command to the background") {
		t.Fatalf("tool end %+v", end)
	}
	msgs := seen()
	if len(msgs) != 2 || !strings.Contains(msgs[1][len(msgs[1])-1]["content"].(string), "still running as job 1") {
		t.Fatalf("model did not get the job: %+v", msgs)
	}
	if a.Background() {
		t.Fatal("nothing runs after the turn")
	}
	if j, _, _ := jobs.Wait(s, 1, 10*time.Second); j.Status != jobs.Exited || *j.ExitCode != 0 {
		t.Fatalf("job %+v", j)
	}
	if log, _ := jobs.Tail(s, 1, 10); log != "started\nfinished" {
		t.Fatalf("job log %q", log)
	}
	if evs := waitEvent(t, s); !strings.Contains(evs[0].Text, "finished") {
		t.Fatalf("event %+v", evs)
	}
}

// A command that ends in the foreground under a host leaves no job.
func TestHostedCommandLeavesNoJob(t *testing.T) {
	s, env := bgSession(t)
	res := RunBash(context.Background(), t.TempDir(), env, BashArgs{Command: "echo ok"}, nil)
	if res.Output != "ok\n" || res.ExitCode != 0 || res.Job != 0 || res.Err != nil {
		t.Fatalf("%+v", res)
	}
	if l := jobs.List(s); len(l) != 0 {
		t.Fatalf("jobs %+v", l)
	}
}

// Without a shell host (other binaries) commands run as before.
func TestDirectRunKillsAtTimeout(t *testing.T) {
	ShellHost = false
	defer func() { ShellHost = true }()
	_, env := bgSession(t)
	res := RunBash(context.Background(), t.TempDir(), env, BashArgs{Command: "echo x; sleep 30", Timeout: 1}, nil)
	if !res.TimedOut || res.Job != 0 || res.Output != "x\n" {
		t.Fatalf("%+v", res)
	}
}

func TestBackgroundForModelShowsTail(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString("line " + strings.Repeat("x", i%3) + "\n")
	}
	out := BashResult{Output: b.String(), Job: 3, Background: BackgroundTimeout}.ForModel(BashArgs{Timeout: 5})
	lines := strings.Split(out, "\n")
	if len(lines) != tailLines+1 || !strings.Contains(lines[tailLines], "the last 40 of 100 lines so far") {
		t.Fatalf("%q", out)
	}
}

// The point of shell hosts: a command that moved to the background keeps
// running, and still reports its exit, after the atto that ran it exits.
func TestBackgroundedCommandOutlivesAtto(t *testing.T) {
	s, _ := bgSession(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake := exec.Command(exe, "_fakeatto", "echo early; sleep 4; echo late; exit 5")
	fake.Env = append(os.Environ(), "FAKEATTO_SESSION="+s, "FAKEATTO_DIR="+t.TempDir())
	out, err := fake.Output()
	if err != nil || !strings.Contains(string(out), "moved to the background as job 1") {
		t.Fatalf("fake atto: %v %q", err, out)
	}
	// The fake atto has exited; the job runs on.
	if j, _ := jobs.Get(s, 1); j.Status != jobs.Running {
		t.Fatalf("job %+v", j)
	}
	j, _, _ := jobs.Wait(s, 1, 10*time.Second)
	if j.Status != jobs.Exited || *j.ExitCode != 5 {
		log, _ := jobs.Tail(s, 1, 10)
		t.Fatalf("job %+v exit %d log %q", j, *j.ExitCode, log)
	}
	if log, _ := jobs.Tail(s, 1, 10); log != "early\nlate" {
		t.Fatalf("job log %q", log)
	}
	if evs := waitEvent(t, s); !strings.Contains(evs[0].Text, "exited with code 5") {
		t.Fatalf("event %+v", evs)
	}
}
