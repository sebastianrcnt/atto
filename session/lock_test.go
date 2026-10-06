package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
)

func TestLockAcquireRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if _, ok := LockedBy(path); ok {
		t.Fatal("no lock yet")
	}
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := LockedBy(path)
	if !ok || l.PID != os.Getpid() || l.Started.IsZero() {
		t.Fatalf("lock %+v %v", l, ok)
	}
	release()
	if _, ok := LockedBy(path); ok {
		t.Fatal("released")
	}
	if _, err := os.Stat(LockPath(path)); !os.IsNotExist(err) {
		t.Fatalf("lock file left: %v", err)
	}
}

func TestLockHeldByOtherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if _, err := LockFor(path, 4242); err != nil {
		t.Fatal(err)
	}
	old := processAlive
	t.Cleanup(func() { processAlive = old })
	processAlive = func(pid int) bool { return pid == 4242 }
	if l, ok := LockedBy(path); !ok || l.PID != 4242 {
		t.Fatalf("lock %+v %v", l, ok)
	}
	if _, err := Lock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("a live holder refuses the lock: %v", err)
	}
	// Its process is gone: the lock is stale and replaced.
	processAlive = func(int) bool { return false }
	if _, ok := LockedBy(path); ok {
		t.Fatal("stale lock ignored")
	}
	processAlive = old
	path2 := path
	release, err := LockFor(path2, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := LockedBy(path); !ok || l.PID != os.Getpid() {
		t.Fatalf("lock %+v %v", l, ok)
	}
	release()
}

func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatal("this process is alive")
	}
	if pidAlive(0x7ffffff0) {
		t.Fatal("no such process")
	}
}

func TestReadOnlyWriterDropsEntries(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := New(t.TempDir())
	w.Append(Entry{Type: TypeName, Name: "kept"})
	w.SetReadOnly("locked")
	w.Append(Entry{Type: TypeName, Name: "dropped"})
	w.Branch("")
	w.Close()
	_, entries, err := Load(w.Path)
	if err != nil || len(entries) != 1 || entries[0].Name != "kept" || w.ReadOnly() != "locked" {
		t.Fatalf("%v %+v", err, entries)
	}
}

func TestListMarksRunning(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	cwd := t.TempDir()
	w := New(cwd)
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "hi"}})
	w.Close()
	if l, _ := List(cwd, false); len(l) != 1 || l[0].Running != 0 {
		t.Fatalf("%+v", l)
	}
	release, err := Lock(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if l, _ := List(cwd, false); len(l) != 1 || l[0].Running != os.Getpid() {
		t.Fatalf("%+v", l)
	}
}

// A terminal's lock refuses others with its own wording, still ErrLocked,
// and makes the session's directory when the session has none yet.
func TestLockTUI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2026", "10", "06", "s.jsonl")
	release, err := LockTUI(path)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := LockedBy(path)
	if !ok || l.Kind != KindTUI || l.PID != os.Getpid() {
		t.Fatalf("lock %+v %v", l, ok)
	}
	l.PID = os.Getppid() // as another terminal holds it
	err = LockError(l)
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "open in another atto (pid ") {
		t.Fatalf("error %v", err)
	}
	if _, err := LockTUI(path); err != nil {
		t.Fatalf("our own lock again: %v", err)
	}
	release()
	if _, ok := LockedBy(path); ok {
		t.Fatal("released")
	}
}

func TestWriterKindsDoNotAllowSameProcessTakeover(t *testing.T) {
	for _, kind := range []string{KindRun, KindServer} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			release, err := LockKind(path, kind)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			for _, other := range []string{KindTUI, KindRun, KindServer, KindBackground} {
				if _, err := LockKind(path, other); !errors.Is(err, ErrLocked) {
					t.Fatalf("%s stole %s: %v", other, kind, err)
				}
			}
		})
	}
}
