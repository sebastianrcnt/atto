//go:build windows

package maint

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func busyLock(p string) bool {
	f, e := os.OpenFile(p, os.O_RDWR, 0)
	if os.IsNotExist(e) {
		return false
	}
	if e != nil {
		return true
	}
	defer f.Close()
	// Agent turn/slot locks use byte zero; session and fsutil locks use
	// the high offset so transcript data remains readable. Probe both.
	low := &windows.Overlapped{}
	if e = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, low); e != nil {
		return true
	}
	defer windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, low)
	o := &windows.Overlapped{OffsetHigh: 1 << 30}
	if e = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, o); e != nil {
		return true
	}
	windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, o)
	return false
}
func connectionRefused(e error) bool { return errors.Is(e, windows.WSAECONNREFUSED) }
func otherAttoProcesses() bool {
	snap, e := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if e != nil {
		return true
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafeSizeProcessEntry())
	if e = windows.Process32First(snap, &entry); e != nil {
		return true
	}
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if entry.ProcessID != uint32(os.Getpid()) && (strings.EqualFold(name, "atto.exe") || strings.EqualFold(name, "atto.exe.old")) {
			return true
		}
		if e = windows.Process32Next(snap, &entry); e != nil {
			break
		}
	}
	return false
}
func owned(p string) bool {
	sd, e := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if e != nil || sd == nil {
		return false
	}
	owner, _, e := sd.Owner()
	if e != nil || owner == nil {
		return false
	}
	token, e := windows.OpenCurrentProcessToken()
	if e != nil {
		return false
	}
	defer token.Close()
	user, e := token.GetTokenUser()
	return e == nil && owner.Equals(user.User.Sid)
}
func removeExecutable(p string) error {
	if e := os.Remove(p); e == nil || os.IsNotExist(e) {
		return e
	}
	self, e := os.Executable()
	if e != nil || !strings.EqualFold(filepath.Clean(self), filepath.Clean(p)) {
		return fmt.Errorf("cannot delete %s", p)
	}
	if strings.ContainsAny(p, "\r\n\"%&|<>^!") {
		return fmt.Errorf("cannot safely schedule deletion of %s; delete it manually", p)
	}
	cmd := exec.Command("cmd.exe", "/d", "/c", `ping 127.0.0.1 -n 4 >nul & del /f /q "`+p+`"`)
	cmd.SysProcAttr = &windowsSysProcAttr
	if e := cmd.Start(); e != nil {
		return e
	}
	return cmd.Process.Release()
}
func undoInstallerPath(binary string) error {
	dir := os.Getenv("ATTO_INSTALL_DIR")
	if dir == "" {
		dir = filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "atto")
	}
	if !strings.EqualFold(filepath.Dir(binary), dir) {
		return nil
	}
	escaped := strings.ReplaceAll(dir, "'", "''")
	script := `$d='` + escaped + `'; $p=[Environment]::GetEnvironmentVariable('Path','User'); $parts=@($p -split ';' | Where-Object { $_ -ne $d }); [Environment]::SetEnvironmentVariable('Path',($parts -join ';'),'User')`
	b, e := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if e != nil {
		return fmt.Errorf("undo user PATH: %s: %w", b, e)
	}
	return nil
}
func privateSocketDirName() string { return "" }
func fileInUse(p string) bool {
	st, e := os.Lstat(p)
	if e != nil {
		return !os.IsNotExist(e)
	}
	if st.IsDir() {
		busy := false
		_ = filepath.WalkDir(p, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				busy = true
			} else if !d.IsDir() && fileInUse(p) {
				busy = true
			}
			return nil
		})
		return busy
	}
	name, e := windows.UTF16PtrFromString(p)
	if e != nil {
		return true
	}
	h, e := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if e != nil {
		return true
	}
	windows.CloseHandle(h)
	return false
}
func isAttoProcess(pid int) bool {
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if e != nil {
		return false
	}
	defer windows.CloseHandle(h)
	b := make([]uint16, 32768)
	n := uint32(len(b))
	if e = windows.QueryFullProcessImageName(h, 0, &b[0], &n); e != nil {
		return false
	}
	return strings.EqualFold(filepath.Base(windows.UTF16ToString(b[:n])), "atto.exe")
}
func fileProbe() func(string) bool { return fileInUse }
