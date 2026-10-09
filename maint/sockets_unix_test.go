//go:build !windows

package maint

import (
	"github.com/sebastianrcnt/atto/config"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanStaleAndLiveSockets(t *testing.T) {
	idleTemps(t)
	root, e := os.MkdirTemp("", "am-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { removeTree(root) })
	t.Setenv(config.EnvDir, root)
	os.MkdirAll(filepath.Join(root, "run"), 0o700)
	stale := filepath.Join(root, "run/stale.sock")
	live := filepath.Join(root, "run/live.sock")
	l, e := net.Listen("unix", stale)
	if e != nil {
		t.Fatal(e)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	l, e = net.Listen("unix", live)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	plan, e := Clean(CleanOptions{Root: root, Temp: t.TempDir(), Yes: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(stale); !os.IsNotExist(e) {
		t.Fatal("stale socket retained")
	}
	if _, e = os.Stat(live); e != nil {
		t.Fatal("live socket deleted")
	}
	if len(plan.Kept) != 1 {
		t.Fatal(plan.Kept)
	}
}
func TestCleanDoesNotFollowCategorySymlink(t *testing.T) {
	idleTemps(t)
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	outside := t.TempDir()
	file := filepath.Join(outside, "deleted/1/job.json")
	put(t, file, []byte("{\"id\":1,\"session\":\"deleted\",\"status\":\"exited\"}"))
	if e := os.Symlink(outside, filepath.Join(root, "jobs")); e != nil {
		t.Fatal(e)
	}
	p, e := Clean(CleanOptions{Root: root, Temp: t.TempDir(), Yes: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(file); e != nil {
		t.Fatal("followed category symlink")
	}
	if len(p.Kept) == 0 {
		t.Fatal("missing kept explanation")
	}
}
func TestRestorePreservesSafeSymlinkAndModes(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	put(t, filepath.Join(root, "settings.json"), []byte("{}"))
	os.Chmod(filepath.Join(root, "settings.json"), 0o640)
	if e := os.Symlink("settings.json", filepath.Join(root, "alias")); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "backup.tar.zst")
	if _, e := Backup(BackupOptions{Root: root, Output: file}); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if _, e := Restore(file, RestoreOptions{Root: dest}); e != nil {
		t.Fatal(e)
	}
	if link, e := os.Readlink(filepath.Join(dest, "alias")); e != nil || link != "settings.json" {
		t.Fatal(link, e)
	}
	st, _ := os.Stat(filepath.Join(dest, "settings.json"))
	if st.Mode().Perm() != 0o640 {
		t.Fatal(st.Mode())
	}
}
func TestCleanKeepsDeletedSessionWriterLock(t *testing.T) {
	idleTemps(t)
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	file := filepath.Join(root, "sessions/deleted.jsonl")
	put(t, file, []byte("{}\n"))
	f, e := os.OpenFile(file[:len(file)-len(".jsonl")]+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = holdTestLock(f); e != nil {
		t.Fatal(e)
	}
	defer releaseTestLock(f)
	os.Remove(file)
	output := filepath.Join(root, "outputs/deleted/old.zst")
	put(t, output, []byte("keep"))
	age(t, output, 40*24*time.Hour)
	if _, e = Clean(CleanOptions{Root: root, Temp: t.TempDir(), Yes: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(output); e != nil {
		t.Fatal("deleted locked session's output removed")
	}
}
