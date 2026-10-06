package jobs

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestJobPathsCannotEscapeSessionRoot(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for _, session := range []string{"../outside", `..\outside`, "/outside", ""} {
		if _, _, err := reserve(session); err == nil {
			t.Errorf("reserved invalid session %q", session)
		}
		if _, err := Get(session, 1); err == nil {
			t.Errorf("loaded invalid session %q", session)
		}
		if !strings.HasSuffix(Root(session), filepath.Join("jobs", ".invalid-session")) {
			t.Errorf("unsafe root: %q", Root(session))
		}
	}
	if _, err := Get("s", -1); err == nil {
		t.Fatal("accepted negative job id")
	}
}
