package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
)

// hookedApp is a treeApp whose session has command hooks that append
// their stdin to a log file, one JSON object per line.
func hookedApp(t *testing.T, baseURL string, hookEvents ...string) (*App, string) {
	cwd, _ := testEnv(t)
	log := filepath.Join(t.TempDir(), "hooks.log")
	cfg := map[string][]config.HookMatcher{}
	for _, ev := range hookEvents {
		cfg[ev] = []config.HookMatcher{{Hooks: []config.HookSpec{{Type: "command", Command: hooktest.LogStdin(log)}}}}
	}
	settings, _ := json.Marshal(config.Settings{Hooks: cfg})
	writeTestFile(t, config.SettingsPath(), string(settings))
	return startApp(t, cwd), log
}

func hookLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(path)
	var out []map[string]any
	for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

// waitLog waits for n hook calls (hooks run in the background).
func waitLog(t *testing.T, path string, n int) []map[string]any {
	t.Helper()
	for range 200 {
		if got := hookLog(t, path); len(got) >= n {
			return got
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected %d hook calls, got %v", n, hookLog(t, path))
	return nil
}

func TestSessionEndReasons(t *testing.T) {
	a, log := hookedApp(t, "", "SessionEnd")
	first := a.threadID
	a.ui.Do(func() { a.cmdClear("") })
	settle(a)
	second := a.threadID
	path := saveSession(t, a.cwd, "earlier")
	a.ui.Do(func() { a.requestResume(path) })
	settle(a)

	got := hookLog(t, log) // SessionEnd runs before the switch returns
	if len(got) != 2 {
		t.Fatalf("calls %v", got)
	}
	if got[0]["reason"] != "clear" || got[0]["session_id"] != first || got[0]["hook_event_name"] != "SessionEnd" {
		t.Fatalf("/clear: %v", got[0])
	}
	if got[1]["reason"] != "resume" || got[1]["session_id"] != second {
		t.Fatalf("resume: %v", got[1])
	}
}
