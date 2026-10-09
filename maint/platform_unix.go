//go:build !windows

package maint

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return true
	}
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return false
}
func connectionRefused(e error) bool { return errors.Is(e, syscall.ECONNREFUSED) }

// Unknown process state keeps temp files; ps is available on supported Unix hosts.
func otherAttoProcesses() bool {
	b, e := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if e != nil {
		return true
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		name := filepath.Base(strings.Join(fields[1:], " "))
		if pid != os.Getpid() && (name == "atto" || name == "atto.old" || name == "atto.new") {
			return true
		}
	}
	return false
}
func owned(p string) bool {
	st, e := os.Lstat(p)
	if e != nil {
		return false
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Getuid())
}
func removeExecutable(p string) error       { return os.Remove(p) }
func undoInstallerPath(binary string) error { return nil } // install.sh only prints advice; it does not edit any shell file.
func privateSocketDirName() string          { return "atto-" + strconv.Itoa(os.Getuid()) }

// lsof checks open file descriptors as well as a process cwd inside a directory.
// On Linux /proc is the fallback when lsof is not installed.
func fileInUse(p string) bool {
	st, e := os.Lstat(p)
	if e != nil {
		return !os.IsNotExist(e)
	}
	if _, e = exec.LookPath("lsof"); e == nil {
		args := []string{"-nP", "-t", "--", p}
		if st.IsDir() {
			args = []string{"-nP", "-t", "+D", p}
		}
		b, e := exec.Command("lsof", args...).Output()
		if len(strings.TrimSpace(string(b))) > 0 {
			return true
		}
		if e == nil {
			return false
		}
		var exit *exec.ExitError
		return !errors.As(e, &exit) || exit.ExitCode() != 1
	}
	dirs, e := os.ReadDir("/proc")
	if e != nil {
		return true
	}
	for _, d := range dirs {
		pid, e := strconv.Atoi(d.Name())
		if e != nil || pid == os.Getpid() {
			continue
		}
		base := filepath.Join("/proc", d.Name())
		entries, e := os.ReadDir(filepath.Join(base, "fd"))
		if e != nil {
			if errors.Is(e, os.ErrPermission) {
				return true
			}
			continue
		}
		paths := []string{filepath.Join(base, "cwd")}
		for _, fd := range entries {
			paths = append(paths, filepath.Join(base, "fd", fd.Name()))
		}
		for _, f := range paths {
			target, e := os.Readlink(f)
			if e == nil && (target == p || st.IsDir() && within(p, target)) {
				return true
			}
		}
	}
	return false
}
func isAttoProcess(pid int) bool {
	b, e := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	return e == nil && filepath.Base(strings.TrimSpace(string(b))) == "atto"
}

// Snapshot all open paths once per inventory rather than invoking lsof per file.
func fileProbe() func(string) bool {
	if _, e := exec.LookPath("lsof"); e != nil {
		return fileInUse
	}
	b, e := exec.Command("lsof", "-nP", "-Fn").Output()
	if e != nil && len(b) == 0 {
		return func(string) bool { return true }
	}
	var paths []string
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.HasPrefix(line, "n/") {
			name := strings.TrimSuffix(line[1:], " (deleted)")
			if i := strings.Index(name, " type="); i >= 0 {
				name = name[:i]
			}
			paths = append(paths, name)
		}
	}
	return func(p string) bool {
		abs, e := filepath.Abs(canonical(p))
		if e != nil {
			return true
		}
		st, e := os.Lstat(abs)
		if e != nil {
			return !os.IsNotExist(e)
		}
		for _, open := range paths {
			if open == abs || st.IsDir() && rawWithin(abs, open) {
				return true
			}
		}
		return false
	}
}
