package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests never write to the real ~/.atto: New puts sessions under
// config.SessionsDir() whatever their cwd, and a test that forgot
// ATTO_DIR once left a 65 MB session there. Tests that need their own
// directory still set ATTO_DIR themselves; a test binary run as a child
// with a temporary ATTO_DIR keeps it.
func TestMain(m *testing.M) {
	dir := ""
	if d := os.Getenv("ATTO_DIR"); d == "" || !strings.HasPrefix(filepath.Clean(d), filepath.Clean(os.TempDir())) {
		var err error
		if dir, err = os.MkdirTemp("", "atto-session-test"); err != nil {
			panic(err)
		}
		os.Setenv("ATTO_DIR", dir)
	}
	code := m.Run()
	if dir != "" {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}
