//go:build !windows

package app

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/provider/providertest"
)

// Updating atto replaces the binary under a running TUI, its session's
// worker and the daemon. Opening the session from the new build replaces
// the idle worker; the old TUI follows it to the new worker with its
// draft, and the old daemon hands over to the new build's.
func TestTerminalFollowsWorkerUpgrade(t *testing.T) {
	dir := t.TempDir()
	build := func(name, version string) string {
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-ldflags", "-X github.com/sebastianrcnt/atto/update.Version="+version, "-o", out, "../cmd/atto")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %v\n%s", err, b)
		}
		return out
	}
	oldBuild := build("atto-old", "v0.0.1-upgrade")
	newBuild := build("atto-new", "v0.0.2-upgrade")
	installed := filepath.Join(dir, "bin", "atto")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldBuild, installed); err != nil {
		t.Fatal(err)
	}

	cwd, m := testEnv(t, providertest.Reply{Text: "answer from the old build"}, providertest.Reply{Text: "answer from the new build"})
	writeTestFile(t, config.SettingsPath(), `{"updateCheck":false,"mouse":false,"defaultProvider":"fake","defaultModel":"m"}`)
	t.Setenv("ATTO_NO_DAEMON", "")
	env := append(os.Environ(), config.EnvAgent+"=", "ATTO_SESSION_ID=", "ATTO_TOOL_CALL_ID=", "TMUX=", "TERM=xterm-256color")
	t.Cleanup(func() {
		cmd := exec.Command(installed, "daemon", "stop", "-force")
		cmd.Env = env
		_ = cmd.Run()
	})
	type run struct {
		terminal *os.File
		output   *terminalOutput
		exited   chan error
	}
	start := func(args ...string) *run {
		t.Helper()
		cmd := exec.Command(installed, args...)
		cmd.Dir, cmd.Env = cwd, env
		terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
		if err != nil {
			t.Fatal(err)
		}
		r := &run{terminal: terminal, output: &terminalOutput{}, exited: make(chan error, 1)}
		go func() { _, _ = io.Copy(r.output, terminal) }()
		go func() { r.exited <- cmd.Wait() }()
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = terminal.Close() })
		return r
	}
	wait := func(r *run, what string, cond func() bool) {
		t.Helper()
		for until := time.Now().Add(20 * time.Second); time.Now().Before(until); time.Sleep(20 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatalf("waiting for %s:\n%s", what, r.output.text())
	}
	keys := func(r *run, s string) {
		t.Helper()
		if _, err := io.WriteString(r.terminal, s); err != nil {
			t.Fatal(err)
		}
	}
	status := func() string {
		cmd := exec.Command(installed, "daemon", "status")
		cmd.Env = env
		out, _ := cmd.CombinedOutput()
		return string(out)
	}

	first := start("--inline")
	wait(first, "the old build's TUI", func() bool { return strings.Contains(first.output.text(), "enter steer") })
	keys(first, "hello old build\r")
	wait(first, "the old build's answer", func() bool { return strings.Contains(first.output.text(), "answer from the old build") })
	ws, err := daemon.Workers()
	if err != nil || len(ws) != 1 {
		t.Fatalf("workers %+v %v", ws, err)
	}
	old := ws[0]
	if !strings.Contains(status(), "v0.0.1-upgrade") {
		t.Fatalf("status before the update:\n%s", status())
	}
	keys(first, "draft kept across the upgrade")
	time.Sleep(200 * time.Millisecond)

	// The update: the binary on disk is replaced.
	if err := os.Rename(newBuild, installed); err != nil {
		t.Fatal(err)
	}
	second := start("resume", old.Session, "--inline")
	wait(second, "the new build's TUI", func() bool { return strings.Contains(second.output.text(), "hello old build") })
	wait(first, "the old TUI following the upgrade", func() bool {
		return strings.Contains(first.output.text(), "The session now runs atto v0.0.2-upgrade")
	})
	if strings.Contains(first.output.text(), "This session was closed") {
		t.Fatalf("the old TUI took the upgrade for a close:\n%s", first.output.text())
	}
	ws, err = daemon.Workers()
	if err != nil || len(ws) != 1 || ws[0].PID == old.PID || ws[0].Version != "v0.0.2-upgrade" || ws[0].Clients != 2 {
		t.Fatalf("workers after the upgrade: %+v %v", ws, err)
	}
	wait(second, "the daemon handover", func() bool { return strings.Contains(status(), "daemon v0.0.2-upgrade") })

	// The draft survived; sending it runs in the new worker.
	keys(first, "\r")
	wait(first, "the new build's answer", func() bool { return strings.Contains(first.output.text(), "answer from the new build") })
	if reqs := m.Requests(); len(reqs) != 2 || !strings.Contains(reqs[1], "draft kept across the upgrade") {
		t.Fatalf("requests %v", reqs)
	}
	log, _ := os.ReadFile(daemon.LogPath())
	if !strings.Contains(string(log), "replaced the worker of session "+old.Session) {
		t.Fatalf("daemon log:\n%s", log)
	}
}
