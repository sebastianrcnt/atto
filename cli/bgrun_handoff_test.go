package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
)

func TestContinueHandoffHelper(t *testing.T) {
	id := os.Getenv("ATTO_TEST_CONTINUE_HANDOFF")
	if id == "" {
		return
	}
	t.Setenv(config.EnvDir, os.Getenv("ATTO_TEST_CONTINUE_DIR"))
	if err := RunContinue([]string{id}, io.Discard); err != nil {
		t.Fatal(err)
	}
}

// Run the real _continue entry point in a child, with a fake model only.
// The model blocks so the test can observe adoption while the child writes.
func TestContinueAdoptsInheritedWriterLease(t *testing.T) {
	arrived := make(chan struct{}, 1)
	finish := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"all done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	defer close(finish)
	dir := t.TempDir()
	t.Setenv(config.EnvDir, dir)
	t.Setenv(config.EnvAgent, "")
	models := fmt.Sprintf(`{"providers":{"fake":{"baseUrl":%q,"models":[{"id":"m","contextWindow":10000,"input":["text"]}]}}}`, srv.URL)
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, w := bgSession(t)
	parentRelease, err := session.LockTUI(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer parentRelease()
	cmd := exec.Command(os.Args[0], "-test.run=^TestContinueHandoffHelper$")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "ATTO_TEST_CONTINUE_HANDOFF="+w.ID, "ATTO_TEST_CONTINUE_DIR="+dir)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := session.StartBackground(w.Path, cmd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	parentRelease()
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("background continuation never reached fake model")
	}
	l, ok := session.LockedBy(w.Path)
	if !ok || l.PID != cmd.Process.Pid || l.Kind != session.KindBackground {
		t.Fatalf("child lease: %+v %v", l, ok)
	}
	if _, err := session.Lock(w.Path); !errors.Is(err, session.ErrLocked) {
		t.Fatalf("another writer acquired child's lease: %v", err)
	}
	finish <- struct{}{}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, ok := session.LockedBy(w.Path); ok {
		t.Fatal("completed continuation retained lease")
	}
	_, entries, err := session.Load(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Message != nil && entry.Message.Role == "assistant" && entry.Message.Content == "all done" {
			found = true
		}
	}
	if !found {
		t.Fatal("continuation did not save fake model response")
	}
}
