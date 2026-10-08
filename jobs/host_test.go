//go:build !windows

package jobs

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/shell"
)

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func gone(pid int) bool { return errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) }

func startHost(t *testing.T, command string, out *syncBuf) *Host {
	t.Helper()
	h, err := StartHost(shell.Default(), t.TempDir(), nil, command, out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shell.KillGroup(h.PID) })
	return h
}

// A detached command no longer needs atto: closing the host's stdin (as
// atto exiting does) leaves it running, and it finishes as a job.
func TestHostDetachSurvivesAtto(t *testing.T) {
	s := setup(t)
	t.Cleanup(func() { KillAll(s) })
	var out syncBuf
	h := startHost(t, "echo one; sleep 0.5; echo two", &out)
	for deadline := time.Now().Add(3 * time.Second); out.String() == ""; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no output")
		}
	}
	if err := h.Detach(s, "two lines"); err != nil {
		t.Fatal(err)
	}
	st := <-h.Status()
	if st.Job != 1 {
		t.Fatalf("status %+v", st)
	}
	h.WaitCopied()
	_ = h.ctl.Close() // atto goes away
	j, why, err := Wait(s, 1, 10*time.Second)
	if err != nil || why != "done" || j.Status != Exited || *j.ExitCode != 0 {
		t.Fatalf("job %+v %s %v", j, why, err)
	}
	if log, _ := Tail(s, 1, 10); log != "one\ntwo" || out.String() != "one\n" {
		t.Fatalf("log %q, streamed %q", log, out.String())
	}
	var evs []events.Event
	for deadline := time.Now().Add(2 * time.Second); len(evs) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		evs = events.Drain(s)
	}
	if len(evs) != 1 || !strings.Contains(evs[0].Text, `"two lines"`) {
		t.Fatalf("event %+v", evs)
	}
	_ = h.cmd.Wait()
}

// A command still in the foreground dies with atto.
func TestHostForegroundDiesWithAtto(t *testing.T) {
	setup(t)
	var out syncBuf
	h := startHost(t, "sleep 30 & sleep 30", &out)
	_ = h.ctl.Close()
	st := <-h.Status()
	if st.Exit == nil {
		t.Fatalf("status %+v", st)
	}
	_ = h.Wait()
	// Killed descendants keep the group present until the OS reaps them;
	// loaded runners may need longer. Stay below the command's 30s sleep
	// so a surviving command cannot pass by finishing normally.
	for deadline := time.Now().Add(10 * time.Second); !gone(h.PID); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			processes, err := exec.Command("ps", "-axo", "pid,ppid,pgid,state,command").CombinedOutput()
			var group []string
			for line := range strings.SplitSeq(string(processes), "\n") {
				if fields := strings.Fields(line); len(fields) >= 3 && fields[2] == strconv.Itoa(h.PID) {
					group = append(group, line)
				}
			}
			t.Fatalf("the command outlived atto (process group %d, ps error %v):\n%s", h.PID, err, strings.Join(group, "\n"))
		}
	}
}

func TestHostDetachRefusedWithoutSession(t *testing.T) {
	setup(t)
	var out syncBuf
	h := startHost(t, "sleep 30", &out)
	_ = h.Detach("", "x")
	if st := <-h.Status(); st.DetachError == "" {
		t.Fatalf("status %+v", st)
	}
	h.Kill()
	if st := <-h.Status(); st.Exit == nil {
		t.Fatalf("status %+v", st)
	}
	_ = h.Wait()
}

func TestHostStartError(t *testing.T) {
	setup(t)
	_, err := StartHost(shell.Shell{Kind: shell.Bash, Path: "/nonexistent/bash"}, t.TempDir(), nil, "true", &syncBuf{})
	if _, ok := errors.AsType[*StartError](err); !ok {
		t.Fatalf("want StartError, got %v", err)
	}
}

func TestInterruptDetachPostsQuietExit(t *testing.T) {
	for _, code := range []int{0, 3} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			s := setup(t)
			t.Cleanup(func() { KillAll(s) })
			var out syncBuf
			h := startHost(t, "sleep 0.3; echo done; exit "+strconv.Itoa(code), &out)
			if err := h.DetachQuiet(s, "interrupted"); err != nil {
				t.Fatal(err)
			}
			if st := <-h.Status(); st.Job != 1 {
				t.Fatalf("detach status %+v", st)
			}
			h.Release()
			j, why, err := Wait(s, 1, 10*time.Second)
			if err != nil || why != "done" || j.ExitCode == nil || *j.ExitCode != code || !j.QuietExit {
				t.Fatalf("job %+v %s %v", j, why, err)
			}
			for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				if evs := events.Drain(s); len(evs) > 0 {
					if len(evs) != 1 || events.Wakes(evs) || !strings.Contains(evs[0].Text, "done") {
						t.Fatalf("quiet exit event %+v", evs)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("quiet job exit was suppressed rather than delivered")
				}
			}
		})
	}
}
