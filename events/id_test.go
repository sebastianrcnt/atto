package events

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTimerIDsCannotEscapeDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ATTO_DIR", root)
	target := filepath.Join(root, "victim.json")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../../../victim", "../abc123", "abc123/..", "ABC123", "abc12", "gggggg", `..\..\victim`} {
		if err := CancelTimer("s", id); err == nil {
			t.Errorf("accepted id %q", id)
		}
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
		t.Fatalf("target changed: %q, %v", data, err)
	}
	if err := writeAtomic(filepath.Join(timerDir("s"), "abc123.json"), []byte(`{"id":"../../../victim","due":"2000-01-01T00:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}
	if n := FireDue("s", time.Now()); n != 0 {
		t.Fatalf("fired invalid metadata: %d", n)
	}
	for _, session := range []string{"../outside", `..\outside`, "/outside", ""} {
		if _, err := AddTimer(session, time.Now(), "bad"); err == nil {
			t.Errorf("accepted session %q", session)
		}
		if err := Push(session, Event{Text: "bad"}); err == nil {
			t.Errorf("pushed to session %q", session)
		}
	}
}
