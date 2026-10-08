//go:build windows

package shell

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Tree puts a command in a job object so cancel kills everything it
// spawned (Windows has no process groups to signal). The job is created
// with KILL_ON_JOB_CLOSE, so closing it also cleans up stragglers.
type Tree struct {
	cmd *exec.Cmd
	job windows.Handle
}

func NewTree(cmd *exec.Cmd) *Tree {
	t := &Tree{cmd: cmd}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | consoleFlags()}
	if job, err := windows.CreateJobObject(nil, nil); err == nil {
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err == nil {
			t.job = job
		} else {
			windows.CloseHandle(job)
		}
	}
	cmd.Cancel = func() error {
		if t.job != 0 {
			return windows.TerminateJobObject(t.job, 1)
		}
		return cmd.Process.Kill()
	}
	return t
}

// Started assigns the running process to the job. Children spawned in the
// instant before assignment may escape; the process itself never does.
func (t *Tree) Started() {
	if t.job == 0 || t.cmd.Process == nil {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(t.cmd.Process.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(t.job, h)
}

func (t *Tree) Close() {
	if t.job != 0 {
		windows.CloseHandle(t.job)
		t.job = 0
	}
}

// Kill terminates the whole tree now.
func (t *Tree) Kill() {
	if t.job != 0 {
		_ = windows.TerminateJobObject(t.job, 1)
	} else if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
}

// ownConsole gives cmd a hidden console of its own unless this process's
// console is already private.
func ownConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= consoleFlags()
}

func consoleFlags() uint32 {
	if PrivateConsole {
		return 0
	}
	return windows.CREATE_NO_WINDOW
}

// Detach makes cmd outlive its parent console.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | breakawayFlags()}
}

// Escape our kill-on-close tree when explicitly detaching. An enclosing job
// (for example a CI runner's) may forbid breakaway, so don't request it there.
func breakawayFlags() uint32 {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err == nil &&
		info.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0 {
		return windows.CREATE_BREAKAWAY_FROM_JOB
	}
	return 0
}

// Isolate makes cmd outlive its parent on a console of its own, hidden.
// What it runs gets a console as usual, but changes to it (PowerShell
// setting the code page, say) never reach the user's terminal, during or
// after atto.
func Isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | breakawayFlags()}
}

// KillGroup terminates pid. A Windows process tree is held together by
// the job object of whoever started it, not by pid, so this kills only
// the process itself.
func KillGroup(pid int) {
	if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
		_ = windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
}

// Terminate stops a job supervisor. Windows has no SIGTERM, so the
// supervisor is terminated outright; its kill-on-close job object takes
// the command's whole tree down with it.
func Terminate(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("no process %d", pid)
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// Alive reports whether pid is running.
func Alive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}
