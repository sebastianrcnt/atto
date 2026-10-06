package session

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if _, err := os.Stat(LockPath(path)); err != nil {
		t.Fatalf("lock file left: %v", err)
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
	secondRelease, err := LockTUI(path)
	if err != nil {
		t.Fatalf("our own lock again: %v", err)
	}
	release()
	if _, ok := LockedBy(path); !ok {
		t.Fatal("old release removed new acquisition")
	}
	secondRelease()
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

func TestUnlockedMetadataIsNotALock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	for _, body := range []string{`{"pid":2147483632}`, `{"pid":1,"kind":"tui"}`, `garbage`, ""} {
		if err := os.WriteFile(LockPath(path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := LockedBy(path); ok {
			t.Fatal("unlocked metadata treated as ownership")
		}
		release, err := Lock(path)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
}

// Re-exec the test binary: no Go registry state or cleanup can simulate OS
// ownership across processes, especially abrupt process death.
func TestLockProcessHelper(t *testing.T) {
	mode := os.Getenv("ATTO_TEST_LOCK_MODE")
	if mode == "" {
		return
	}
	path := os.Getenv("ATTO_TEST_LOCK_PATH")
	if mode == "race" {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	release, err := LockKind(path, KindBackground)
	if err != nil {
		if errors.Is(err, ErrLocked) {
			fmt.Println("busy")
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer release()
	fmt.Println("held")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

type lockChild struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
}

func startLockChild(t *testing.T, path, mode string) *lockChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockProcessHelper$")
	cmd.Env = append(os.Environ(), "ATTO_TEST_LOCK_MODE="+mode, "ATTO_TEST_LOCK_PATH="+path)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return &lockChild{cmd, input, bufio.NewReader(output)}
}
func (c *lockChild) line(t *testing.T) string {
	t.Helper()
	result := make(chan string, 1)
	go func() { line, _ := c.output.ReadString('\n'); result <- strings.TrimSpace(line) }()
	select {
	case line := <-result:
		return line
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for child")
		return ""
	}
}

func TestLockHeldByOtherProcessAndKilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	c := startLockChild(t, path, "hold")
	if got := c.line(t); got != "held" {
		t.Fatalf("child: %s", got)
	}
	l, ok := LockedBy(path)
	if !ok || l.PID != c.cmd.Process.Pid || l.Kind != KindBackground || l.Started.IsZero() {
		t.Fatalf("lock: %+v %v", l, ok)
	}
	if _, err := Lock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("contender: %v", err)
	}
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = c.cmd.Wait()
	if _, ok := LockedBy(path); ok {
		t.Fatal("dead process still owns lock")
	}
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestLockCrossProcessRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	for i := range 50 {
		a := startLockChild(t, path, "race")
		b := startLockChild(t, path, "race")
		_, _ = io.WriteString(a.input, "go\n")
		_, _ = io.WriteString(b.input, "go\n")
		al, bl := a.line(t), b.line(t)
		if !(al == "held" && bl == "busy" || al == "busy" && bl == "held") {
			t.Fatalf("race %d: %q %q", i, al, bl)
		}
		_ = a.input.Close()
		_ = b.input.Close()
		if err := a.cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		if err := b.cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOwnedBackgroundRelock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	old, err := LockFor(path, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	newer, err := LockKind(path, KindBackground)
	if err != nil {
		t.Fatal(err)
	}
	old()
	old()
	if _, ok := LockedBy(path); !ok {
		t.Fatal("old release dropped new lease")
	}
	newer()
	last, err := LockKind(path, KindBackground)
	if err != nil {
		t.Fatal(err)
	}
	old()
	newer()
	if _, ok := LockedBy(path); !ok {
		t.Fatal("old releases dropped subsequent acquisition")
	}
	last()
}

func TestHeldCorruptMetadataStillLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	c := startLockChild(t, path, "hold")
	if got := c.line(t); got != "held" {
		t.Fatal(got)
	}
	if err := os.WriteFile(LockPath(path), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := LockedBy(path); !ok {
		t.Fatal("partial metadata hides OS lock")
	}
	if _, err := Lock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("partial metadata: %v", err)
	}
}
