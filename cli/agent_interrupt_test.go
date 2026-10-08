//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
)

func TestAgentInterruptKeepsHostedCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	bodies := agentServer(t, func(n int, body string) string {
		if n == 1 {
			return toolAnswer("echo started; touch '" + marker + "'; sleep 30")
		}
		return textAnswer("next turn")
	})
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	if _, err := runAgent(t, "spawn", "a", "work", "-session", "root"); err != nil {
		t.Fatal(err)
	}
	st, err := agentstate.Load("root", "a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.KillAll("root"); jobs.KillAll(st.Session) })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker shell did not start")
		}
	}
	if out, err := runAgent(t, "interrupt", "a", "-session", "root"); err != nil || !strings.Contains(out, "interrupted") {
		t.Fatalf("interrupt: %q %v", out, err)
	}
	if st.Latest().Status != agentstate.Stopped {
		t.Fatalf("turn status %+v", st.Latest())
	}
	j, err := jobs.Get(st.Session, 1)
	if err != nil || j.Status != jobs.Running || !j.QuietExit {
		t.Fatalf("interrupted shell did not survive the worker: %+v %v", j, err)
	}
	saved, err := session.Find(st.Session)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(saved)
	if err != nil || !strings.Contains(string(data), "still running as job 1") || !strings.Contains(string(data), "started") {
		t.Fatalf("detached result missing: %s %v", data, err)
	}
	if len(bodies()) != 1 {
		t.Fatal("interrupted turn continued")
	}
	// The old interrupt request must not cancel the successor.
	if _, err := runAgent(t, "task", "a", "continue", "-session", "root"); err != nil {
		t.Fatal(err)
	}
	if out, err := runAgent(t, "wait", "a", "-timeout", "10s", "-session", "root"); err != nil || !strings.Contains(out, "next turn") {
		t.Fatalf("next turn: %q %v", out, err)
	}
	if j, err := jobs.Get(st.Session, 1); err != nil || j.Status != jobs.Running {
		t.Fatalf("job died with the successor: %+v %v", j, err)
	}
}
