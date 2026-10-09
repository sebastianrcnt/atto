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
	procAttrs(cmd).CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP | consoleFlags()
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
	cmd.Cancel = t.kill
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
func (t *Tree) Kill() { _ = t.kill() }

// kill terminates the process, the job's other members and the descendants
// that are outside the job. Windows PowerShell 5.1 starts its children
// outside the job object of the process that runs it, so the job alone leaves
// them (and the output pipe they hold) behind. They are looked up before the
// process goes, while its ID cannot belong to anyone else.
func (t *Tree) kill() error {
	var rest []uint32
	if t.cmd.Process != nil {
		rest = descendants(uint32(t.cmd.Process.Pid))
	}
	var err error
	if t.job != 0 {
		err = windows.TerminateJobObject(t.job, 1)
	} else if t.cmd.Process != nil {
		err = t.cmd.Process.Kill()
	}
	for _, pid := range rest {
		if h, e := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); e == nil {
			_ = windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}
	return err
}

// descendants are the running processes that the running process pid
// started, and those they started in turn.
func descendants(pid uint32) []uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	children := map[uint32][]uint32{}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
	}
	root := startTime(pid)
	if root == 0 {
		return nil
	}
	var out []uint32
	started := map[uint32]uint64{pid: root}
	for queue := []uint32{pid}; len(queue) > 0; queue = queue[1:] {
		parent := queue[0]
		for _, child := range children[parent] {
			if _, seen := started[child]; seen {
				continue
			}
			// A process cannot be older than its parent; one that is has
			// the ID of a parent that is long gone.
			at := startTime(child)
			if at == 0 || at < started[parent] {
				continue
			}
			started[child] = at
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}

// startTime is when pid started, as a FILETIME, or 0 if it is not running.
func startTime(pid uint32) uint64 {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil || exited.HighDateTime != 0 || exited.LowDateTime != 0 {
		return 0
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
}

// ownConsole gives cmd a hidden console of its own unless this process's
// console is already private.
func ownConsole(cmd *exec.Cmd) {
	procAttrs(cmd).CreationFlags |= consoleFlags()
}

func procAttrs(cmd *exec.Cmd) *syscall.SysProcAttr {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	return cmd.SysProcAttr
}

// cmd strips the outer pair of quotes after /s /c and runs the script
// verbatim. Go's argv escaping instead leaves backslashes before its quotes.
func setCmdLine(cmd *exec.Cmd, kind Kind, script string) {
	if kind == Cmd {
		procAttrs(cmd).CmdLine = `"` + cmd.Path + `" /d /s /c "` + script + `"`
	}
}

func consoleFlags() uint32 {
	if PrivateConsole {
		return 0
	}
	return windows.CREATE_NO_WINDOW
}

// Detach makes cmd outlive its parent console.
func Detach(cmd *exec.Cmd) {
	procAttrs(cmd).CreationFlags = windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | breakawayFlags()
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
	procAttrs(cmd).CreationFlags = windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | breakawayFlags()
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
