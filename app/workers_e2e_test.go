//go:build !windows

package app

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
	"golang.org/x/sys/unix"
)

type workerTerminal struct {
	cmd    *exec.Cmd
	tty    *os.File
	output *terminalOutput
	exited chan error
}

type workerTerminals struct {
	t           *testing.T
	binary, cwd string
}

func workerTerminalFixture(t *testing.T, replies ...providertest.Reply) (*workerTerminals, *providertest.Model) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "atto")
	if out, err := exec.Command("go", "build", "-o", binary, "../cmd/atto").CombinedOutput(); err != nil {
		t.Fatalf("build %v: %s", err, out)
	}
	cwd, model := testEnv(t, replies...)
	writeTestFile(t, config.SettingsPath(), `{"updateCheck":false,"mouse":false,"defaultProvider":"fake","defaultModel":"m"}`)
	t.Setenv("ATTO_NO_DAEMON", "")
	t.Setenv("ATTO_WORKER_RETENTION", "1s")
	f := &workerTerminals{t: t, binary: binary, cwd: cwd}
	t.Cleanup(func() { _ = f.command("daemon", "stop", "-force").Run() })
	return f, model
}

func (f *workerTerminals) command(args ...string) *exec.Cmd {
	cmd := exec.Command(f.binary, args...)
	cmd.Dir = f.cwd
	cmd.Env = append(os.Environ(), config.EnvAgent+"=", "ATTO_SESSION_ID=", "TMUX=", "TERM=xterm-256color")
	return cmd
}

func (f *workerTerminals) start(args ...string) *workerTerminal {
	f.t.Helper()
	cmd := f.command(args...)
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		f.t.Fatal(err)
	}
	r := &workerTerminal{cmd: cmd, tty: tty, output: &terminalOutput{}, exited: make(chan error, 1)}
	go func() { _, _ = io.Copy(r.output, tty) }()
	go func() { r.exited <- cmd.Wait() }()
	f.t.Cleanup(func() { _ = cmd.Process.Kill(); _ = tty.Close() })
	return r
}

func (f *workerTerminals) wait(r *workerTerminal, what string, cond func() bool) {
	f.t.Helper()
	for until := time.Now().Add(15 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	f.t.Fatalf("waiting for %s:\n%s", what, r.output.text())
}

func (f *workerTerminals) keys(r *workerTerminal, s string) {
	f.t.Helper()
	if _, err := io.WriteString(r.tty, s); err != nil {
		f.t.Fatal(err)
	}
}
func (f *workerTerminals) exit(r *workerTerminal) {
	f.t.Helper()
	select {
	case err := <-r.exited:
		if err != nil {
			f.t.Fatalf("exit %v: %s", err, r.output.text())
		}
	case <-time.After(10 * time.Second):
		f.t.Fatalf("exit hung: %s", r.output.text())
	}
}
func saveTerminalSession(t *testing.T, cwd, name string) string {
	t.Helper()
	w := session.New(cwd)
	w.Append(session.Entry{Type: session.TypeName, Name: name})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: name}})
	w.Close()
	return w.ID
}

func TestTerminalResumePickerStartsNothing(t *testing.T) {
	f, _ := workerTerminalFixture(t)
	saveTerminalSession(t, f.cwd, "older picker session")
	time.Sleep(20 * time.Millisecond)
	recent := saveTerminalSession(t, f.cwd, "latest picker session")
	files := func() []string {
		var paths []string
		_ = filepath.WalkDir(config.SessionsDir(), func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
				paths = append(paths, path)
			}
			return nil
		})
		return paths
	}
	before := files()
	r := f.start("resume")
	f.wait(r, "picker", func() bool { return strings.Contains(r.output.text(), "latest picker session") })
	if ws, err := daemon.Workers(); err != nil || len(ws) != 0 {
		t.Fatalf("picker started workers: %+v %v", ws, err)
	}
	after := files()
	if len(after) != len(before) {
		t.Fatalf("picker created a session: before %d after %d", len(before), len(after))
	}
	f.keys(r, "\x03")
	f.exit(r)
	r = f.start("resume")
	f.wait(r, "picker default", func() bool { return strings.Contains(r.output.text(), "latest picker session") })
	f.keys(r, "\r")
	f.wait(r, "selected recent worker", func() bool {
		ws, _ := daemon.Workers()
		return len(ws) == 1 && ws[0].Session == recent && ws[0].Clients == 1
	})
	f.keys(r, "/quit\r")
	f.exit(r)
	f.wait(r, "retirement", func() bool { ws, _ := daemon.Workers(); return len(ws) == 0 })
	// Esc leaves an empty picker without creating a conversation.
	r = f.start("resume")
	f.wait(r, "picker escape", func() bool { return strings.Contains(r.output.text(), "Agent command center") })
	f.keys(r, "\x1b")
	f.exit(r)
	if ws, _ := daemon.Workers(); len(ws) != 0 {
		t.Fatalf("escape left workers: %+v", ws)
	}
}

func TestTerminalClientsSwitchAndDaemonKill(t *testing.T) {
	gate := make(chan struct{})
	busyGate := make(chan struct{})
	f, m := workerTerminalFixture(t, providertest.Reply{Text: "answer shared by both terminals", Gate: gate}, providertest.Reply{Text: "answer from second terminal"}, providertest.Reply{Text: "finished after switching away", Gate: busyGate})
	b := saveTerminalSession(t, f.cwd, "target B")
	first := f.start()
	f.wait(first, "first TUI", func() bool { return strings.Contains(first.output.text(), "enter steer") })
	ws, _ := daemon.Workers()
	if len(ws) != 1 {
		t.Fatalf("workers %+v", ws)
	}
	a := ws[0].Session
	second := f.start("resume", a)
	if err := pty.Setsize(second.tty, &pty.Winsize{Rows: 24, Cols: 60}); err != nil {
		t.Fatal(err)
	}
	firstSize, err := pty.GetsizeFull(first.tty)
	if err != nil || firstSize.Cols != 120 {
		t.Fatalf("second client resized first: %+v %v", firstSize, err)
	}
	f.wait(second, "two clients", func() bool { ws, _ := daemon.Workers(); return len(ws) == 1 && ws[0].Clients == 2 })
	f.keys(first, "first client prompt\r")
	if m.Started(10*time.Second) == 0 {
		t.Fatal("model did not start")
	}
	f.wait(second, "shared input", func() bool { return strings.Contains(second.output.text(), "first client prompt") })
	close(gate)
	for _, r := range []*workerTerminal{first, second} {
		f.wait(r, "shared answer", func() bool { return strings.Contains(r.output.text(), "answer shared by both terminals") })
	}
	f.keys(second, "second client prompt\r")
	for _, r := range []*workerTerminal{first, second} {
		f.wait(r, "second client answer", func() bool { return strings.Contains(r.output.text(), "answer from second terminal") })
	}
	f.keys(first, "finish after I switch away\r")
	if m.Started(10*time.Second) == 0 {
		t.Fatal("busy switch model did not start")
	}
	pid := first.cmd.Process.Pid
	f.keys(first, "/agents\r")
	f.wait(first, "command center", func() bool { return strings.Contains(first.output.text(), "target B") })
	f.keys(first, "/target B\r\r")
	f.wait(first, "switch to B", func() bool {
		ws, _ := daemon.Workers()
		for _, w := range ws {
			if w.Session == b && w.Clients == 1 {
				return true
			}
		}
		return false
	})
	if first.cmd.Process.Pid != pid {
		t.Fatal("switch replaced TUI process")
	}
	f.wait(first, "A detached", func() bool {
		ws, _ := daemon.Workers()
		for _, w := range ws {
			if w.Session == a {
				return w.Clients == 1
			}
		}
		return false
	})
	f.keys(second, "/quit\r")
	f.exit(second)
	f.wait(first, "unattended busy A", func() bool {
		ws, _ := daemon.Workers()
		for _, w := range ws {
			if w.Session == a {
				return w.Clients == 0 && w.Busy
			}
		}
		return false
	})
	time.Sleep(1100 * time.Millisecond)
	if ws, _ := daemon.Workers(); len(ws) != 2 {
		t.Fatalf("busy worker retired: %+v", ws)
	}
	close(busyGate)
	f.wait(first, "idle A retirement", func() bool { ws, _ := daemon.Workers(); return len(ws) == 1 && ws[0].Session == b })
	cmd := f.command("job", "start", "-session", b, "--", "sleep 100")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("job start %v: %s", err, out)
	}
	f.wait(first, "running job", func() bool { js := jobs.List(b); return len(js) == 1 && js[0].Status == jobs.Running })
	if out, err := f.command("daemon", "kill", b).CombinedOutput(); err != nil {
		t.Fatalf("daemon kill %v: %s", err, out)
	}
	f.wait(first, "closed worker", func() bool { ws, _ := daemon.Workers(); return len(ws) == 0 })
	js := jobs.List(b)
	if len(js) != 1 || js[0].Status != jobs.Killed {
		t.Fatalf("worker kill left job: %+v", js)
	}
}

func TestRemovedCommandsPointToResume(t *testing.T) {
	f, _ := workerTerminalFixture(t)
	for _, args := range [][]string{{"attach"}, {"attach", "-l"}, {"connect"}, {"-c"}, {"-resume"}, {"--c=true"}, {"--resume=true"}, {"-p", "-c", "prompt"}} {
		out, err := f.command(args...).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "use atto resume") || strings.Count(strings.TrimSpace(string(out)), "\n") != 0 {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
}

func TestTerminalSIGTERMDetaches(t *testing.T) {
	gate := make(chan struct{})
	f, m := workerTerminalFixture(t, providertest.Reply{Text: "finished after SIGTERM", Gate: gate})
	r := f.start()
	f.wait(r, "TUI startup", func() bool { return strings.Contains(r.output.text(), "enter steer") })
	ws, _ := daemon.Workers()
	id := ws[0].Session
	f.keys(r, "survive SIGTERM\r")
	if m.Started(10*time.Second) == 0 {
		t.Fatal("model did not start")
	}
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	f.exit(r)
	f.wait(r, "signal detach", func() bool { ws, _ := daemon.Workers(); return len(ws) == 1 && ws[0].Busy && ws[0].Clients == 0 })
	close(gate)
	f.wait(r, "signal worker retirement", func() bool { ws, _ := daemon.Workers(); return len(ws) == 0 })
	path, err := session.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	_, entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Message != nil && strings.Contains(e.Message.Content, "finished after SIGTERM") {
			found = true
		}
	}
	if !found {
		t.Fatalf("worker lost signal-detached turn: %+v", entries)
	}
}

func TestTerminalOldDaemonFallsBackAndStops(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			f, _ := workerTerminalFixture(t, providertest.Reply{Text: "old daemon fallback answer"})
			if err := os.MkdirAll(daemon.RunDir(), 0700); err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("unix", daemon.SocketPath())
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			stopped := make(chan struct{})
			errors := make(chan error, 1)
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					var h [5]byte
					_, err = io.ReadFull(c, h[:])
					if err != nil {
						c.Close()
						continue
					}
					b := make([]byte, binary.BigEndian.Uint32(h[1:]))
					_, err = io.ReadFull(c, b)
					var hello daemon.Hello
					if err == nil {
						err = json.Unmarshal(b, &hello)
					}
					typ, payload := byte('E'), []byte(fmt.Sprintf("the running atto daemon speaks protocol %d, this atto %d: run atto daemon stop", version, daemon.Proto))
					if err != nil {
						errors <- err
						c.Close()
						return
					}
					if hello.Proto == version {
						switch hello.Op {
						case "list", "workers":
							typ = 'W'
							if hello.Op == "list" {
								typ = 'L'
							}
							payload = []byte(`[]`)
						case "stop":
							typ = 'X'
							payload = []byte(`{"code":0}`)
						default:
							errors <- fmt.Errorf("execution downgraded: %+v", hello)
							c.Close()
							return
						}
					}
					h[0] = typ
					binary.BigEndian.PutUint32(h[1:], uint32(len(payload)))
					_, _ = c.Write(append(h[:], payload...))
					c.Close()
					if hello.Proto == version && hello.Op == "stop" {
						close(stopped)
						return
					}
				}
			}()
			r := f.start("explain fallback")
			f.wait(r, "fallback answer", func() bool { return strings.Contains(r.output.text(), "old daemon fallback answer") })
			if !strings.Contains(r.output.text(), "running without the daemon") {
				t.Fatalf("no fallback hint: %s", r.output.text())
			}
			f.keys(r, "/quit\r")
			f.exit(r)
			if out, err := f.command("daemon", "status").CombinedOutput(); err != nil {
				t.Fatalf("old status %v: %s", err, out)
			}
			if out, err := f.command("daemon", "stop", "-force").CombinedOutput(); err != nil {
				t.Fatalf("old stop %v: %s", err, out)
			}
			select {
			case <-stopped:
			case err := <-errors:
				t.Fatal(err)
			case <-time.After(5 * time.Second):
				t.Fatal("old stop never reached daemon")
			}
		})
	}
}

// A signal must unblock a render even when no terminal is draining its
// output. In particular cleanup must not wait forever to reset screen modes.
func TestTerminalSIGTERMWithUndrainedOutput(t *testing.T) {
	gate := make(chan struct{})
	f, m := workerTerminalFixture(t, providertest.Reply{Text: strings.Repeat("large terminal answer ", 20000), Gate: gate})
	cmd := f.command("--inline", "produce a large answer")
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	defer cmd.Process.Kill()
	// Drain initialization only. Once the model is waiting, deliberately
	// stop consuming output before releasing its large answer.
	stopRead, readDone := make(chan struct{}), make(chan struct{})
	fd := int(tty.Fd())
	var readOnce sync.Once
	stopReader := func() { readOnce.Do(func() { close(stopRead) }); <-readDone }
	defer stopReader()
	startup := &terminalOutput{}
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			select {
			case <-stopRead:
				return
			default:
			}
			ps := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(ps, 50)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return
			}
			if n > 0 {
				nr, err := unix.Read(fd, buf)
				if nr > 0 {
					_, _ = startup.Write(buf[:nr])
				}
				if err == unix.EINTR {
					continue
				}
				if err != nil || nr == 0 {
					return
				}
			}
		}
	}()
	if m.Started(30*time.Second) == 0 {
		t.Fatalf("model never started: %s", startup.text())
	}
	stopReader()
	close(gate)
	time.Sleep(500 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signal hung on terminal output")
	}
}
