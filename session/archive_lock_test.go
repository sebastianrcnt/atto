package session

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestArchiveWriterLeaseAndSidecars(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := New("/work")
	w.Append(Entry{Type: TypeName, Name: "kept"})
	w.Close()
	release, err := Lock(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Archive(w.Path); !errors.Is(err, ErrLocked) {
		t.Fatalf("archive live writer: %v", err)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatal(err)
	}
	release()
	if err := os.WriteFile(LogPath(w.Path), []byte("background"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst, err := Archive(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{w.Path, LockPath(w.Path), LogPath(w.Path)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("old sidecar %s: %v", path, err)
		}
	}
	if data, err := os.ReadFile(LogPath(dst)); err != nil || string(data) != "background" {
		t.Fatalf("archived log: %q, %v", data, err)
	}
	if _, err := os.Stat(LockPath(dst)); err != nil {
		t.Fatal(err)
	}
	if _, locked := LockedBy(dst); locked {
		t.Fatal("archive retained lease")
	}
	restored, err := Unarchive(dst)
	if err != nil || restored != w.Path {
		t.Fatalf("unarchive: %q, %v", restored, err)
	}
	if _, err := os.Stat(LogPath(restored)); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRefusesCrossProcessWriter(t *testing.T) {
	if path := os.Getenv("ATTO_TEST_ARCHIVE_LOCK"); path != "" {
		if _, err := Archive(path); !errors.Is(err, ErrLocked) {
			t.Fatalf("archive external writer: %v", err)
		}
		return
	}
	t.Setenv("ATTO_DIR", t.TempDir())
	w := New("/work")
	w.Append(Entry{Type: TypeName, Name: "kept"})
	w.Close()
	release, err := Lock(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestArchiveRefusesCrossProcessWriter$")
	cmd.Env = append(os.Environ(), "ATTO_TEST_ARCHIVE_LOCK="+w.Path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatal(err)
	}
	if _, locked := LockedBy(w.Path); !locked {
		t.Fatal("writer lease lost")
	}
}
