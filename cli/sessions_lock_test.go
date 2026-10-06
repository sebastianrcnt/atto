package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/sebastianrcnt/atto/session"
)

func TestSessionsDeleteWriterLeaseAndSidecars(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "")
	w := session.New(t.TempDir())
	w.Append(session.Entry{Type: session.TypeName, Name: "kept"})
	w.Close()
	release, err := session.Lock(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := sessionsDelete(&out, w.Path, true); !errors.Is(err, session.ErrLocked) {
		t.Fatalf("delete live writer: %v", err)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatal(err)
	}
	release()
	if err := os.WriteFile(session.LogPath(w.Path), []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sessionsDelete(&out, w.Path, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{w.Path, session.LockPath(w.Path), session.LogPath(w.Path)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted path %s: %v", path, err)
		}
	}
}

func TestSessionsDeleteRefusesCrossProcessWriter(t *testing.T) {
	if path := os.Getenv("ATTO_TEST_DELETE_LOCK"); path != "" {
		var out bytes.Buffer
		if err := sessionsDelete(&out, path, true); !errors.Is(err, session.ErrLocked) {
			t.Fatalf("delete external writer: %v", err)
		}
		return
	}
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "")
	w := session.New(t.TempDir())
	w.Append(session.Entry{Type: session.TypeName, Name: "kept"})
	w.Close()
	release, err := session.Lock(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestSessionsDeleteRefusesCrossProcessWriter$")
	cmd.Env = append(os.Environ(), "ATTO_TEST_DELETE_LOCK="+w.Path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatal(err)
	}
	if _, locked := session.LockedBy(w.Path); !locked {
		t.Fatal("writer lease lost")
	}
}
