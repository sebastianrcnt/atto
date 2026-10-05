//go:build !windows

package jobs

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/events"
)

// The test binary doubles as the supervisor and the shell host: Start
// re-executes os.Executable() with "_supervise <dir>", StartHost with
// "_shell".
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "_supervise" {
		if err := Supervise(os.Args[2]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "_shell" {
		if err := ServeHost(os.Stdin, os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func setup(t *testing.T) string {
	t.Setenv("ATTO_DIR", t.TempDir())
	return "s1"
}

func TestJobRunsAndPostsExitEvent(t *testing.T) {
	s := setup(t)
	j, err := Start(s, t.TempDir(), "", "echo one; sleep 0.3; echo two; exit 3", nil, nil)
	if err != nil || j.Status != Running || j.PID == 0 {
		t.Fatalf("start: %+v %v", j, err)
	}
	j, why, err := Wait(s, j.ID, 10*time.Second)
	if err != nil || why != "done" || j.Status != Exited || *j.ExitCode != 3 {
		t.Fatalf("wait: %+v %s %v", j, why, err)
	}
	if out, _ := Tail(s, j.ID, 10); out != "one\ntwo" {
		t.Fatalf("output %q", out)
	}
	var evs []events.Event
	for deadline := time.Now().Add(2 * time.Second); len(evs) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		evs = events.Drain(s)
	}
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "exited with code 3") || !strings.Contains(evs[0].Text, "two") {
		t.Fatalf("event %+v", evs)
	}
}

func TestKillStopsTreeWithoutEvent(t *testing.T) {
	s := setup(t)
	j, err := Start(s, t.TempDir(), "srv", "sleep 30 & sleep 30", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	j, err = Kill(s, j.ID)
	if err != nil || j.Status != Killed {
		t.Fatalf("kill: %+v %v", j, err)
	}
	time.Sleep(200 * time.Millisecond)
	if evs := events.Drain(s); len(evs) != 0 {
		t.Fatalf("killed jobs should not post events: %+v", evs)
	}
	if ActiveCount(s) != 0 {
		t.Fatal("still active")
	}
}

func TestMonitorUntil(t *testing.T) {
	s := setup(t)
	dir := t.TempDir()
	// Becomes READY on the third check.
	cmd := `n=$(cat count 2>/dev/null || echo 0); n=$((n+1)); echo $n > count; [ $n -ge 3 ] && echo READY || echo waiting`
	j, err := Start(s, dir, "health", cmd, &Monitor{Every: time.Second, Until: "READY", Timeout: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	j, _, _ = Wait(s, j.ID, 20*time.Second)
	if j.Status != Exited || *j.ExitCode != 0 {
		t.Fatalf("monitor: %+v", j)
	}
	// The supervisor saves the final state, then posts the event.
	var evs []events.Event
	for deadline := time.Now().Add(2 * time.Second); len(evs) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		evs = events.Drain(s)
	}
	if len(evs) != 1 || evs[0].Source != "monitor" || !strings.Contains(evs[0].Text, "condition met") {
		t.Fatalf("event %+v", evs)
	}
}

func TestNotifyPostsWhileRunningThenExit(t *testing.T) {
	s := setup(t)
	cmd := `echo ERROR a; echo ok; echo ERROR b; sleep 1.6; echo panic c; echo done`
	j, err := Start(s, t.TempDir(), "logs", cmd, nil, &Notify{Pattern: "ERROR|panic"})
	if err != nil {
		t.Fatal(err)
	}
	if j.KindLabel() != "job+notify" {
		t.Fatalf("kind %q", j.KindLabel())
	}
	// The first batch must arrive while the job is still running.
	var first []events.Event
	for deadline := time.Now().Add(1500 * time.Millisecond); len(first) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		first = events.Drain(s)
	}
	cur, _ := Get(s, j.ID)
	if len(first) != 1 || !cur.Active() ||
		!strings.Contains(first[0].Text, "ERROR a") || !strings.Contains(first[0].Text, "ERROR b") || strings.Contains(first[0].Text, "ok") {
		t.Fatalf("first batch %+v (active %v)", first, cur.Active())
	}
	if _, why, _ := Wait(s, j.ID, 10*time.Second); why != "done" {
		t.Fatal(why)
	}
	var rest []events.Event
	for deadline := time.Now().Add(2 * time.Second); len(rest) < 2 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		rest = append(rest, events.Drain(s)...)
	}
	if len(rest) != 2 || !strings.Contains(rest[0].Text, "panic c") || strings.Contains(rest[0].Text, "done") ||
		!strings.Contains(rest[1].Text, "exited with code 0") {
		t.Fatalf("rest %+v", rest)
	}
}

func TestNotifyRejectsBadPattern(t *testing.T) {
	s := setup(t)
	if _, err := Start(s, t.TempDir(), "", "true", nil, &Notify{Pattern: "("}); err == nil {
		t.Fatal("want regexp error")
	}
}

func TestWaitWokenByUserInput(t *testing.T) {
	s := setup(t)
	j, _ := Start(s, t.TempDir(), "", "sleep 5", nil, nil)
	defer Kill(s, j.ID)
	go func() { time.Sleep(200 * time.Millisecond); events.Wake(s) }()
	if _, why, _ := Wait(s, j.ID, 10*time.Second); why != "woken" {
		t.Fatalf("why %s", why)
	}
}

func TestStartArgsQuietExit(t *testing.T) {
	s := setup(t)
	// Run directly, no shell: the argument keeps its spaces and quotes.
	j, err := StartArgs(s, t.TempDir(), "agent x", []string{"/bin/echo", `a "b" c`}, true)
	if err != nil {
		t.Fatal(err)
	}
	if j, _, _ = Wait(s, j.ID, 10*time.Second); j.Status != Exited || *j.ExitCode != 0 || j.Label() != "agent x" {
		t.Fatalf("job %+v", j)
	}
	if out, _ := Tail(s, j.ID, 5); out != `a "b" c` {
		t.Fatalf("output %q", out)
	}
	time.Sleep(200 * time.Millisecond)
	if evs := events.Drain(s); len(evs) != 0 {
		t.Fatalf("a quiet job's clean exit posts nothing: %+v", evs)
	}
	// A failure still does.
	j, _ = StartArgs(s, t.TempDir(), "agent y", []string{"/bin/sh", "-c", "exit 4"}, true)
	Wait(s, j.ID, 10*time.Second)
	var evs []events.Event
	for deadline := time.Now().Add(2 * time.Second); len(evs) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		evs = events.Drain(s)
	}
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "exited with code 4") {
		t.Fatalf("events %+v", evs)
	}
}
