//go:build !windows

package app

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
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

type terminalRPCConn struct {
	io.ReadCloser
	io.WriteCloser
}

func (c terminalRPCConn) Close() error { _ = c.WriteCloser.Close(); return c.ReadCloser.Close() }

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

func TestTerminalDaemonDetachAndResume(t *testing.T) { terminalDaemonRoundtrip(t, false) }

func TestTerminalDaemonHangupAndResume(t *testing.T) { terminalDaemonRoundtrip(t, true) }

func terminalDaemonRoundtrip(t *testing.T, killView bool) {
	binary := filepath.Join(t.TempDir(), "atto")
	if out, err := exec.Command("go", "build", "-o", binary, "../cmd/atto").CombinedOutput(); err != nil {
		t.Fatalf("build %v\n%s", err, out)
	}
	gate := make(chan struct{})
	cwd, m := testEnv(t, providertest.Reply{Text: "daemon continuing answer", Gate: gate})
	writeTestFile(t, config.SettingsPath(), `{"updateCheck":false,"mouse":false,"defaultProvider":"fake","defaultModel":"m"}`)
	t.Setenv("ATTO_NO_DAEMON", "")
	t.Setenv("ATTO_WORKER_RETENTION", "1s")
	stop := func() {
		cmd := exec.Command(binary, "daemon", "stop", "-force")
		cmd.Env = append(os.Environ(), config.EnvAgent+"=", "ATTO_SESSION_ID=")
		_ = cmd.Run()
	}
	t.Cleanup(stop)
	type terminalRun struct {
		cmd      *exec.Cmd
		terminal *os.File
		output   *terminalOutput
		exited   chan error
	}
	start := func(args ...string) *terminalRun {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), config.EnvAgent+"=", "ATTO_SESSION_ID=", "TMUX=", "TERM=xterm-256color")
		terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
		if err != nil {
			t.Fatal(err)
		}
		r := &terminalRun{cmd: cmd, terminal: terminal, output: &terminalOutput{}, exited: make(chan error, 1)}
		go func() { _, _ = io.Copy(r.output, terminal) }()
		go func() { r.exited <- cmd.Wait() }()
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = terminal.Close() })
		return r
	}
	wait := func(r *terminalRun, what string, cond func() bool) {
		t.Helper()
		for until := time.Now().Add(15 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatalf("waiting for %s:\n%s", what, r.output.text())
	}
	typeKeys := func(r *terminalRun, keys string) {
		t.Helper()
		if _, err := io.WriteString(r.terminal, keys); err != nil {
			t.Fatal(err)
		}
	}
	first := start("--inline")
	wait(first, "daemon terminal startup", func() bool { return strings.Contains(first.output.text(), "enter steer") })
	workers, err := daemon.Workers()
	if err != nil || len(workers) != 1 {
		t.Fatalf("workers %+v %v", workers, err)
	}
	w := workers[0]
	nc, err := daemon.DialWorker(w)
	if err != nil {
		t.Fatal(err)
	}
	client := server.NewClient(nc)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{3}, "capabilities": map[string]bool{"interactive": true}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(ctx, "prompt/clientOpen", map[string]any{"threadId": w.Session, "prompt": server.Prompt{Kind: server.PromptSelect, RequestID: "pty-question", Title: "Daemon prompt question", Options: []server.PromptOption{{Label: "PTY chosen option"}}}}, nil); err != nil {
		t.Fatal(err)
	}
	wait(first, "worker prompt", func() bool { return strings.Contains(first.output.text(), "Daemon prompt question") })
	typeKeys(first, "\r")
	for {
		select {
		case n := <-client.Events():
			if n.Method == "prompt/clientAnswered" {
				goto answered
			}
		case <-ctx.Done():
			t.Fatal("PTY did not answer worker prompt")
		}
	}
answered:
	typeKeys(first, "daemon turn prompt\r")
	if m.Started(10*time.Second) == 0 {
		t.Fatalf("no model request:\n%s", first.output.text())
	}
	// app-server is another client of the same worker the terminal shows.
	protocolCmd := exec.Command(binary, "app-server")
	protocolCmd.Dir = cwd
	protocolCmd.Env = first.cmd.Env
	in, err := protocolCmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := protocolCmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := protocolCmd.Start(); err != nil {
		t.Fatal(err)
	}
	protocol := server.NewClient(terminalRPCConn{out, in})
	protocolDone := make(chan error, 1)
	go func() { protocolDone <- protocolCmd.Wait() }()
	t.Cleanup(func() {
		protocol.Close()
		select {
		case <-protocolDone:
		case <-time.After(5 * time.Second):
			_ = protocolCmd.Process.Kill()
			<-protocolDone
		}
	})
	if err := protocol.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{3}}, nil); err != nil {
		t.Fatal(err)
	}
	var shared server.ThreadInfo
	if err := protocol.Call(ctx, "thread/resume", map[string]any{"threadId": w.Session}, &shared); err != nil || !shared.Busy || shared.ID != w.Session {
		t.Fatalf("app-server did not share TUI worker: %+v %v", shared, err)
	}
	client.Close()
	if killView {
		if err := first.terminal.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		typeKeys(first, "\x04")
	}
	select {
	case err := <-first.exited:
		if err != nil && !killView {
			t.Fatalf("detach exit %v:\n%s", err, first.output.text())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Ctrl+D did not detach:\n%s", first.output.text())
	}
	workers, err = daemon.Workers()
	if err != nil || len(workers) != 1 || workers[0].PID != w.PID || !workers[0].Busy {
		t.Fatalf("detach stopped worker turn: %+v %v", workers, err)
	}
	if killView {
		close(gate)
		wait(first, "detached turn completion", func() bool { ws, _ := daemon.Workers(); return len(ws) == 1 && !ws[0].Busy })
	}
	second := start("resume", w.Session)
	wait(second, "reattached prompt", func() bool { return strings.Contains(second.output.text(), "daemon turn prompt") })
	if !killView {
		close(gate)
	}
	wait(second, "continued answer", func() bool { return strings.Contains(second.output.text(), "daemon continuing answer") })
	if len(m.Requests()) != 1 {
		t.Fatalf("turn was rerun: %v", m.Requests())
	}
	seen := 0
	for {
		select {
		case n := <-protocol.Events():
			if n.Method == "item/completed" && strings.Contains(string(n.Params), "daemon continuing answer") {
				seen++
			}
			if n.Method == "turn/completed" {
				if seen != 1 {
					t.Fatalf("app-server received answer %d times", seen)
				}
				goto protocolCompleted
			}
		case <-ctx.Done():
			t.Fatal("app-server did not receive shared turn")
		}
	}
protocolCompleted:
	protocol.Close()
	printCmd := exec.Command(binary, "-p", "-session", w.Session, "-output-format", "json", "print through worker")
	printCmd.Dir = cwd
	printCmd.Env = first.cmd.Env
	printed, err := printCmd.Output()
	if err != nil {
		t.Fatalf("print worker: %v: %s", err, printed)
	}
	var result struct {
		Session string `json:"session_id"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal(printed, &result); err != nil || result.IsError || result.Session != w.Session || result.Result != "daemon continuing answer" {
		t.Fatalf("worker print result %s %v", printed, err)
	}
	workers, err = daemon.Workers()
	if err != nil || len(workers) != 1 || workers[0].PID != w.PID {
		t.Fatalf("print did not use existing worker: %+v %v", workers, err)
	}
	if killView {
		proc, err := os.FindProcess(w.PID)
		if err != nil {
			t.Fatal(err)
		}
		if err := proc.Kill(); err != nil {
			t.Fatal(err)
		}
		wait(second, "transparent worker reconnect", func() bool { return strings.Contains(second.output.text(), "Reconnected.") })
		workers, err = daemon.Workers()
		if err != nil || len(workers) != 1 || workers[0].PID == w.PID || workers[0].Session != w.Session {
			t.Fatalf("crashed worker did not restart: %+v %v", workers, err)
		}
	}
	if killView {
		typeKeys(second, "/quit\r")
	} else {
		typeKeys(second, "/close\r")
	}
	select {
	case err := <-second.exited:
		if err != nil {
			t.Fatalf("close %v:\n%s", err, second.output.text())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("/close did not exit:\n%s", second.output.text())
	}
	wait(second, "worker exit after close or idle retirement", func() bool { workers, _ := daemon.Workers(); return len(workers) == 0 })
}
