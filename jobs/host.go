package jobs

// A shell host runs one call of the agent's shell tool. atto starts every
// command under one (atto re-executed as `atto _shell`), isolated from
// atto like a job supervisor, so that a command still running at its
// foreground wait, or when the user interrupts or presses Ctrl+B, can
// become a background job without being restarted. Detaching needs no handover: the host stops
// streaming to atto, writes the output it has captured so far to the new
// job's output.log, keeps appending there, and from then on is the job's
// supervisor: it records the exit and posts the [atto event], and `atto
// job kill` stops it like any job. A command that ends in the foreground
// leaves no job behind.
//
// The alternative, atto pumping the pipes itself after a timeout, would
// tie the job to atto's lifetime (and to its pipes); a separate host
// costs one extra process start per command instead.
//
// The protocol runs over the host's standard streams:
//
//	stdin   atto → host: a hostSpec line, then hostControl lines. EOF
//	        before a detach means atto is gone; the command is killed.
//	stdout  host → atto: the command's combined output, raw.
//	stderr  host → atto: HostStatus lines.
//
// Both output streams are closed when the command ends or detaches.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/shell"
)

// hostSpec is the command a host runs.
type hostSpec struct {
	Shell   string     `json:"shell"`
	Kind    shell.Kind `json:"kind"`
	Command string     `json:"command"`
	Cwd     string     `json:"cwd"`
}

// hostControl is a request from atto: "detach" (into a job of Session,
// named Name) or "kill".
type hostControl struct {
	Op        string `json:"op"`
	Session   string `json:"session,omitempty"`
	Name      string `json:"name,omitempty"`
	QuietExit bool   `json:"quietExit,omitempty"`
}

// HostStatus is a report from the host. Exactly one field is set.
type HostStatus struct {
	PID         int    `json:"pid,omitempty"`         // the command started
	Error       string `json:"error,omitempty"`       // it could not start
	Exit        *int   `json:"exit,omitempty"`        // it ended in the foreground
	Job         int    `json:"job,omitempty"`         // it is now this background job
	DetachError string `json:"detachError,omitempty"` // it could not detach and keeps running
}

// pipeGrace is how long a command that ended in the foreground may leave
// its output pipe held open by processes it started (`server &`) before
// the host stops reading, as exec.Cmd.WaitDelay does for direct runs.
const pipeGrace = 2 * time.Second

// StartError is a command that could not start (a missing shell, a bad
// working directory); the host itself ran.
type StartError struct{ Msg string }

func (e *StartError) Error() string { return e.Msg }

// Host is atto's side of a running shell host.
type Host struct {
	PID int // the command's process (group leader on Unix)

	cmd    *exec.Cmd
	ctlMu  sync.Mutex
	ctl    io.WriteCloser
	status chan HostStatus // closed when the host closes its stderr
	copied chan struct{}   // closed when its stdout is drained
}

// StartHost starts command under a shell host in cwd with env (the
// complete environment) and returns once the command runs. Its output is
// copied to out until it ends or detaches. A *StartError means the
// command could not start; any other error, that the host could not (the
// caller may run the command itself).
func StartHost(sh shell.Shell, cwd string, env []string, command string, out io.Writer) (*Host, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "_shell")
	cmd.Dir, cmd.Env = cwd, env
	shell.Isolate(cmd)
	ctl, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	h := &Host{cmd: cmd, ctl: ctl, status: make(chan HostStatus, 4), copied: make(chan struct{})}
	go func() {
		_, _ = io.Copy(out, stdout)
		close(h.copied)
	}()
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 4096), 1<<20)
		for sc.Scan() {
			var st HostStatus
			if err := json.Unmarshal(sc.Bytes(), &st); err != nil {
				st = HostStatus{Error: "shell host: " + sc.Text()} // e.g. a panic
			}
			h.status <- st
		}
		close(h.status)
	}()
	spec, _ := json.Marshal(hostSpec{Shell: sh.Path, Kind: sh.Kind, Command: command, Cwd: cwd})
	_, werr := ctl.Write(append(spec, '\n'))
	st, ok := <-h.status
	switch {
	case ok && st.PID > 0:
		h.PID = st.PID
		return h, nil
	case ok && st.Error != "" && werr == nil:
		_ = h.Wait()
		return nil, &StartError{st.Error}
	}
	_ = cmd.Process.Kill()
	_ = h.Wait()
	if werr != nil {
		return nil, werr
	}
	return nil, errors.New("shell host did not start")
}

// Status reports what the host says; the channel closes when it has
// nothing more to say (the command ended or detached, or the host died).
func (h *Host) Status() <-chan HostStatus { return h.status }

func (h *Host) send(c hostControl) error {
	data, _ := json.Marshal(c)
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	_, err := h.ctl.Write(append(data, '\n'))
	return err
}

// Detach asks the host to turn the command into a job of session. The
// answer arrives on Status: Job, DetachError, or Exit if it ended first.
// quietExit marks an interrupt-detached job: its exit waits for the next turn
// rather than waking an idle session.
func (h *Host) Detach(session, name string, quietExit ...bool) error {
	quiet := len(quietExit) > 0 && quietExit[0]
	return h.send(hostControl{Op: "detach", Session: session, Name: name, QuietExit: quiet})
}

// Kill stops the command and everything it started, detached or not.
func (h *Host) Kill() {
	if h.send(hostControl{Op: "kill"}) != nil {
		// The host is gone or wedged: kill the command's group ourselves.
		shell.KillGroup(h.PID)
		_ = h.cmd.Process.Kill()
	}
}

// Wait waits for a command that ended in the foreground: its output is
// copied and the host has exited.
func (h *Host) Wait() error {
	<-h.copied
	for range h.status {
	}
	return h.cmd.Wait()
}

// Release lets go of a host whose command detached: it keeps running as
// the job's supervisor. Its exit is still reaped while atto runs.
func (h *Host) Release() {
	<-h.copied
	go func() {
		for range h.status {
		}
		_ = h.cmd.Wait()
	}()
}

// WaitCopied returns once all output before the command ended or
// detached has been copied.
func (h *Host) WaitCopied() { <-h.copied }

// host is the host process's state.
type host struct {
	spec   hostSpec
	start  time.Time
	tree   *shell.Tree
	pid    int
	status *os.File // to atto, until detached
	out    *os.File

	mu       sync.Mutex
	mem      []byte // output so far (up to maxLog), to seed output.log
	memFull  bool
	log      *cappedFile // set once detached
	dir      string
	job      Job
	exited   bool
	killed   bool
	statusMu sync.Mutex
}

// Write takes the command's output: to atto while in the foreground, to
// the job's log once detached.
func (h *host) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.log != nil {
		return h.log.Write(p)
	}
	if room := maxLog - len(h.mem); room > 0 {
		h.mem = append(h.mem, p[:min(len(p), room)]...)
	} else {
		h.memFull = true
	}
	_, _ = h.out.Write(p) // atto may be gone; the command goes on until told
	return len(p), nil
}

func (h *host) report(st HostStatus) {
	data, _ := json.Marshal(st)
	h.statusMu.Lock()
	_, _ = h.status.Write(append(data, '\n'))
	h.statusMu.Unlock()
}

// detach turns the command into a job: the output so far seeds its log,
// and atto's streams close.
func (h *host) detach(c hostControl) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.log != nil && c.QuietExit && !h.exited {
		h.job.QuietExit = true
		_ = save(h.dir, h.job)
	}
	if h.exited || h.log != nil {
		return // atto gets the exit status instead
	}
	id, dir, err := reserve(c.Session)
	if err != nil {
		h.report(HostStatus{DetachError: err.Error()})
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		h.report(HostStatus{DetachError: err.Error()})
		return
	}
	if err := fsutil.PrivateFile(f); err != nil {
		f.Close()
		h.report(HostStatus{DetachError: err.Error()})
		return
	}
	log := &cappedFile{f: f}
	_, _ = log.Write(h.mem)
	if h.memFull {
		_, _ = log.Write([]byte{'\n'}) // past the cap: writes the note that output was dropped
	}
	h.mem = nil
	j := Job{ID: id, Session: c.Session, Name: c.Name, Command: h.spec.Command, Cwd: h.spec.Cwd, Status: Running,
		SupervisorPID: os.Getpid(), PID: h.pid, Started: h.start, QuietExit: c.QuietExit}
	if err := save(dir, j); err != nil {
		f.Close()
		_ = os.RemoveAll(dir)
		h.report(HostStatus{DetachError: err.Error()})
		return
	}
	h.log, h.dir, h.job = log, dir, j
	h.report(HostStatus{Job: id})
	h.status.Close()
	h.out.Close()
}

// kill stops the command's tree. A killed job posts no event, as with
// atto job kill.
func (h *host) kill() {
	h.mu.Lock()
	h.killed = true
	h.mu.Unlock()
	h.tree.Kill()
}

// ServeHost is the shell host process (`atto _shell`), serving the
// protocol described at the top of this file on its standard streams.
func ServeHost(in io.Reader, out, status *os.File) error {
	ignoreSIGPIPE()             // atto may go away; writes to it must fail, not kill us
	shell.PrivateConsole = true // StartHost put us on a hidden console
	br := bufio.NewReader(in)
	line, err := br.ReadBytes('\n')
	var spec hostSpec
	if err == nil {
		err = json.Unmarshal(line, &spec)
	}
	h := &host{spec: spec, start: time.Now(), status: status, out: out}
	if err != nil {
		h.report(HostStatus{Error: "bad spec: " + err.Error()})
		return err
	}

	// The output pipe is ours rather than exec's so that a command that
	// detaches can keep writing after its shell exits (a server started
	// with &), while one in the foreground gets pipeGrace.
	pr, pw, err := os.Pipe()
	if err != nil {
		h.report(HostStatus{Error: err.Error()})
		return err
	}
	// Killing goes through the tree; the context is never canceled.
	cmd := shell.Shell{Kind: spec.Kind, Path: spec.Shell}.Command(context.Background(), spec.Command)
	cmd.Dir = spec.Cwd
	cmd.Stdout, cmd.Stderr = pw, pw
	h.tree = shell.NewTree(cmd)
	defer h.tree.Close()
	err = cmd.Start()
	pw.Close()
	if err != nil {
		pr.Close()
		h.report(HostStatus{Error: err.Error()})
		return nil
	}
	h.tree.Started()
	h.pid = cmd.Process.Pid
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(h, pr)
		close(copied)
	}()
	h.report(HostStatus{PID: h.pid})

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		if _, ok := <-sigs; ok {
			h.kill()
		}
	}()
	go func() {
		dec := json.NewDecoder(br)
		for {
			var c hostControl
			if dec.Decode(&c) != nil {
				break
			}
			switch c.Op {
			case "detach":
				h.detach(c)
			case "kill":
				h.kill()
			}
		}
		// atto closed our stdin: it is gone, or done with a command that
		// ended. One still in the foreground goes with it.
		h.mu.Lock()
		orphaned := !h.exited && h.log == nil
		h.mu.Unlock()
		if orphaned {
			h.kill()
		}
	}()

	werr := cmd.Wait()
	code, detail := 0, ""
	var ee *exec.ExitError
	switch {
	case werr == nil:
	case errors.As(werr, &ee):
		code = ee.ExitCode()
	default:
		code, detail = -1, werr.Error()
	}
	// Let the output drain: for pipeGrace in the foreground, for as long
	// as it takes once detached (as for any job).
	select {
	case <-copied:
	case <-time.After(pipeGrace):
		h.mu.Lock()
		detached := h.log != nil
		h.mu.Unlock()
		if !detached {
			pr.Close()
		}
		<-copied
	}
	pr.Close()

	h.mu.Lock()
	h.exited = true
	detached, killed, j, dir := h.log != nil, h.killed, h.job, h.dir
	h.mu.Unlock()
	if !detached {
		out.Close()
		h.report(HostStatus{Exit: &code})
		status.Close()
		return nil
	}
	h.log.f.Close()
	return finish(dir, j, code, detail, killed)
}
