//go:build !windows

package app

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

type terminalOutput struct {
	sync.Mutex
	buf bytes.Buffer
}

func (b *terminalOutput) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.buf.Write(p)
}
func (b *terminalOutput) text() string {
	b.Lock()
	defer b.Unlock()
	return tui.StripEscapes(b.buf.String())
}

// Exercise the actual input parser, renderer, ordered runtime connection,
// hosted shell and shutdown, not a substitute terminal or direct RPC calls.
func TestTerminalCLIEndToEnd(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "atto")
	if out, err := exec.Command("go", "build", "-o", binary, "../cmd/atto").CombinedOutput(); err != nil {
		t.Fatalf("build %v\n%s", err, out)
	}
	gate := make(chan struct{})
	cwd, m := testEnv(t, providertest.Reply{Text: "PTY first answer", Gate: gate}, providertest.Reply{Text: "PTY steered answer"}, providertest.Reply{Command: "sleep 30", Description: "PTY long command"})
	writeTestFile(t, config.SettingsPath(), `{"updateCheck":false,"mouse":false,"defaultProvider":"fake","defaultModel":"m"}`)
	cmd := exec.Command(binary, "--inline")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "ATTO_NO_DAEMON=1", config.EnvAgent+"=", "ATTO_SESSION_ID=", "TMUX=", "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	output := &terminalOutput{}
	go func() { _, _ = io.Copy(output, terminal) }()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = terminal.Close() })
	wait := func(what string, cond func() bool) {
		t.Helper()
		for until := time.Now().Add(15 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatalf("waiting for %s:\n%s", what, output.text())
	}
	typeKeys := func(s string) {
		t.Helper()
		if _, err := io.WriteString(terminal, s); err != nil {
			t.Fatal(err)
		}
	}
	wait("terminal startup", func() bool { return strings.Contains(output.text(), "enter steer") })
	typeKeys("PTY prompt\r")
	if m.Started(10*time.Second) == 0 {
		t.Fatalf("no model request:\n%s", output.text())
	}
	typeKeys("PTY steering\r")
	wait("pending steer", func() bool { return strings.Contains(output.text(), "PTY steering") })
	close(gate)
	wait("answer", func() bool { return strings.Contains(output.text(), "PTY steered answer") })
	if reqs := m.Requests(); len(reqs) != 2 || !strings.Contains(reqs[1], "PTY steering") {
		t.Fatalf("steer requests %v", reqs)
	}
	typeKeys("run a command\r")
	wait("hosted command", func() bool { return strings.Contains(output.text(), "PTY long command") })
	// Wait for the command to actually start rather than interrupting the
	// tool-argument draft before a host exists.
	time.Sleep(150 * time.Millisecond)
	typeKeys("\x1b")
	wait("user-interrupted host detached", func() bool {
		return strings.Contains(output.text(), "background") && strings.Contains(output.text(), "Interrupted.")
	})
	sums, err := session.List(cwd, false)
	if err != nil || len(sums) != 1 {
		t.Fatalf("sessions %v %v", sums, err)
	}
	_, entries, err := session.Load(sums[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	var detached bool
	for _, e := range entries {
		if e.Message != nil && e.Message.Role == "tool" {
			detached = detached || strings.Contains(e.Message.Content, "background")
		}
	}
	if !detached {
		t.Fatal("interrupt-detached command result missing from session")
	}
	typeKeys("/quit\r")
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("exit %v:\n%s", err, output.text())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("terminal did not exit:\n%s", output.text())
	}
	if _, held := session.LockedBy(sums[0].Path); held {
		t.Fatal("TUI runtime retained writer lease after exit")
	}
}
