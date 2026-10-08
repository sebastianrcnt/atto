//go:build !windows

package shell

import (
	"fmt"
	"os/exec"
	"syscall"
)

// ownConsole is a no-op: only Windows shares a console's code pages.
func ownConsole(*exec.Cmd) {}

func setCmdLine(*exec.Cmd, Kind, string) {}

// Tree kills a command's whole process group on cancel.
type Tree struct{ cmd *exec.Cmd }

func NewTree(cmd *exec.Cmd) *Tree {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return &Tree{cmd}
}

func (*Tree) Started() {}
func (*Tree) Close()   {}

// Kill terminates the whole tree now.
func (t *Tree) Kill() {
	if t.cmd.Process != nil {
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Detach makes cmd outlive its parent (new session, no controlling tty).
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Isolate makes cmd outlive its parent, like Detach. On Unix the two are
// the same: a new session, so the terminal's hangup never reaches it.
func Isolate(cmd *exec.Cmd) { Detach(cmd) }

// KillGroup kills the process group led by pid (a command started with
// NewTree).
func KillGroup(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

// Terminate asks the process (a job supervisor) to stop; it kills its tree
// and records the result. A pid of 0 or less is refused: kill(2) would send
// the signal to the caller's own process group, or to every process.
func Terminate(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("no process %d", pid)
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// Alive reports whether pid is running.
func Alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }
