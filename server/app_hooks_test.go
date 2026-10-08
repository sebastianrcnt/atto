package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/hooks"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
)

func hookedThread(t *testing.T, url string, names ...string) (*thread, string) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	th.inboxOff.Store(true)
	log := filepath.Join(t.TempDir(), "hooks.log")
	cfg := map[string][]config.HookMatcher{}
	for _, name := range names {
		cfg[name] = []config.HookMatcher{{Hooks: []config.HookSpec{{Type: "command", Command: hooktest.LogStdin(log)}}}}
	}
	th.hooks = hooks.New(cfg, th.agent.Cwd)
	return th, log
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
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func waitLog(t *testing.T, path string, n int) []map[string]any {
	t.Helper()
	for range 200 {
		if got := hookLog(t, path); len(got) >= n {
			return got
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected %d calls got %v", n, hookLog(t, path))
	return nil
}

func TestNotificationAfterLongTurn(t *testing.T) {
	a, log := hookedThread(t, "", "Notification")
	a.runKind, a.runStart = "turn", time.Now() // short: no notification
	a.afterRun(nil)
	a.runKind, a.runStart = "turn", time.Now().Add(-time.Minute)
	a.afterRun(nil)
	got := waitLog(t, log, 1)
	time.Sleep(200 * time.Millisecond)
	if got = hookLog(t, log); len(got) != 1 {
		t.Fatalf("only the long turn notifies: %v", got)
	}
	if got[0]["notification_type"] != "idle_prompt" || got[0]["hook_event_name"] != "Notification" || got[0]["message"] == "" {
		t.Fatalf("input %v", got[0])
	}

	// Not when the user's queued message starts the next turn right away.
	a.runKind, a.runStart = "turn", time.Now().Add(-time.Minute)
	a.turns.Queued = append(a.turns.Queued, &pendingInput{Text: "next"})
	a.notifyIdle()
	time.Sleep(200 * time.Millisecond)
	if got = hookLog(t, log); len(got) != 1 {
		t.Fatalf("queued input follows; no notification: %v", got)
	}
}

func TestNotificationForBackgroundEventWhenIdle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	a, log := hookedThread(t, srv.URL, "Notification")

	// During a turn the event is a steer: the user is already looking.
	a.turns.Busy, a.runKind = true, "turn"
	a.turns.PendingEvents = []events.Event{{Source: "job", Title: "job 1 exited"}}
	a.deliverEvents()
	time.Sleep(200 * time.Millisecond)
	if got := hookLog(t, log); len(got) != 0 {
		t.Fatalf("busy: %v", got)
	}

	a.turns.Busy, a.runKind = false, ""
	a.turns.PendingEvents = []events.Event{{Source: "job", Title: "job 2 exited"}, {Source: "timer", Title: "timer fired"}}
	a.deliverEvents()
	got := waitLog(t, log, 1)
	if got[0]["notification_type"] != "background_event" || got[0]["message"] != "job 2 exited (+1 more)" {
		t.Fatalf("input %v", got[0])
	}
	for busy := true; busy; { // let the started turn finish before the test's cleanup
		time.Sleep(10 * time.Millisecond)
		a.call(func() error { busy = a.turns.Busy; return nil })
	}
}

func TestNotificationWhenGoalBlocked(t *testing.T) {
	a, log := hookedThread(t, "", "Notification")
	a.announceGoal(&goal.Goal{Objective: "x", Status: goal.Active})
	a.announceGoal(&goal.Goal{Objective: "x", Status: goal.Blocked, Note: "needs a credential"})
	got := waitLog(t, log, 1)
	if got[0]["notification_type"] != "goal_blocked" || !strings.Contains(got[0]["message"].(string), "needs a credential") {
		t.Fatalf("input %v", got[0])
	}
}
