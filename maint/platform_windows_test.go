//go:build windows

package maint

import (
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsDataPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv(config.EnvDir, "")
	if config.Dir() != filepath.Join(home, ".atto") {
		t.Fatal(config.Dir())
	}
	override := filepath.Join(home, "custom-data")
	t.Setenv(config.EnvDir, override)
	if config.Dir() != override {
		t.Fatal(config.Dir())
	}
	if safeName(`C:\escape`) || safeName(`C:/escape`) || safeName(`..\escape`) {
		t.Fatal("accepted Windows path")
	}
}
func TestWindowsMaintenanceLocks(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	p := filepath.Join(root, "sessions", "abc.jsonl")
	put(t, p, []byte("{}\n"))
	release, e := session.Lock(p)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if !busyLock(session.LockPath(p)) {
		t.Fatal("Windows lock not detected")
	}
	a, e := Activity(root)
	if e != nil || len(a) == 0 {
		t.Fatal(a, e)
	}
}
func TestWindowsFakeExeRemoval(t *testing.T) {
	p := filepath.Join(t.TempDir(), "atto-fake.exe")
	put(t, p, []byte("fake"))
	if e := removeExecutable(p); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("not deleted")
	}
}

func TestWindowsAgentCoordinationLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "turn.lock")
	put(t, p, nil)
	f, e := os.OpenFile(p, os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	o := &windows.Overlapped{}
	if e = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, o); e != nil {
		t.Fatal(e)
	}
	defer windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, o)
	if !busyLock(p) {
		t.Fatal("agent-state byte-zero lock was not detected")
	}
}

func TestWindowsTempOwnership(t *testing.T) {
	p := filepath.Join(t.TempDir(), "owned")
	put(t, p, nil)
	if !owned(p) {
		t.Fatal("current user file was not recognized as owned")
	}
}
