package maint

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

func TestUninstallKeepDataNoBackupFakeBinary(t *testing.T) {
	idleTemps(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := filepath.Join(home, ".atto")
	t.Setenv(config.EnvDir, root)
	binary := filepath.Join(home, "bin/atto-fake")
	put(t, binary, []byte("fake executable"))
	data := filepath.Join(root, "settings.json")
	put(t, data, []byte("keep me"))
	var out bytes.Buffer
	if e := Uninstall(UninstallOptions{Root: root, Temp: t.TempDir(), Binary: binary, Yes: true, KeepData: true, NoBackup: true, Out: &out}); e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(data); e != nil || string(b) != "keep me" {
		t.Fatal("data not retained", e)
	}
	if _, e := os.Stat(binary); !os.IsNotExist(e) {
		t.Fatal("fake binary retained")
	}
	if !strings.Contains(out.String(), "Atto data retained") || !strings.Contains(out.String(), "Uninstall complete") {
		t.Fatal(out.String())
	}
}
func TestUninstallBackupAndDataRemoval(t *testing.T) {
	idleTemps(t)
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	put(t, filepath.Join(root, "settings.json"), []byte("precious"))
	binary := filepath.Join(t.TempDir(), "atto-fake")
	put(t, binary, []byte("fake"))
	backup := filepath.Join(t.TempDir(), "backup.tar.zst")
	if e := Uninstall(UninstallOptions{Root: root, Temp: t.TempDir(), Binary: binary, Yes: true, BackupFile: backup}); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(root); !os.IsNotExist(e) {
		t.Fatal("data retained", e)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if _, e := Restore(backup, RestoreOptions{Root: dest}); e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(filepath.Join(dest, "settings.json")); e != nil || string(b) != "precious" {
		t.Fatal("backup lost settings")
	}
}
func TestUninstallDeclinedDoesNothing(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	binary := filepath.Join(t.TempDir(), "atto-fake")
	put(t, binary, []byte("fake"))
	if e := Uninstall(UninstallOptions{Root: root, Temp: t.TempDir(), Binary: binary, Confirm: func(string, bool) bool { return false }}); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(binary); e != nil {
		t.Fatal("cancelled uninstall removed binary")
	}
}
func TestUninstallKeepsDirtyWorktree(t *testing.T) {
	idleTemps(t)
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	repo := repoFixture(t)
	w := filepath.Join(root, "worktrees/parent/dirty")
	os.MkdirAll(filepath.Dir(w), 0o700)
	if _, e := git(repo, "worktree", "add", "-b", "atto/parent/dirty", w); e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(w, "precious"), []byte("uncommitted"))
	put(t, filepath.Join(root, "settings.json"), []byte("remove"))
	binary := filepath.Join(t.TempDir(), "atto-fake")
	put(t, binary, []byte("fake"))
	if e := Uninstall(UninstallOptions{Root: root, Temp: t.TempDir(), Binary: binary, Yes: true, NoBackup: true}); e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(filepath.Join(w, "precious")); e != nil || string(b) != "uncommitted" {
		t.Fatal("dirty work lost", e)
	}
	if _, e := os.Stat(filepath.Join(root, "settings.json")); !os.IsNotExist(e) {
		t.Fatal("other data retained")
	}
	git(repo, "worktree", "remove", "--force", w)
}
