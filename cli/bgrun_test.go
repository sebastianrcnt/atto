package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// bgSession is a session in a fresh cwd that stopped with the user's
// message unanswered, as "Run in background" leaves it.
func bgSession(t *testing.T) (cwd string, w *session.Writer) {
	t.Helper()
	cwd = t.TempDir()
	t.Chdir(cwd)
	w = session.New(cwd)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "do the thing"}})
	w.Close()
	return cwd, w
}

// continueServer is a fake model that records the requests and whether the
// session was locked while it answered.
func continueServer(t *testing.T, path *string) (bodies func() []string, lockedDuring func() bool) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	locked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		_, ok := session.LockedBy(*path)
		locked = locked || ok
		mu.Unlock()
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"all done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv(config.EnvDir, dir)
	models := `{"providers":{"fake":{"baseUrl":"` + srv.URL + `","models":[{"id":"m","contextWindow":10000,"input":["text"]}]}}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) },
		func() bool { mu.Lock(); defer mu.Unlock(); return locked }
}

func TestContinueRunsTheStoppedTurn(t *testing.T) {
	var path string
	bodies, lockedDuring := continueServer(t, &path)
	cwd := t.TempDir()
	t.Chdir(cwd)
	logPath := filepath.Join(t.TempDir(), "hooks.log")
	hook := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": hooktest.LogStdin(logPath)}}}}
	raw, _ := json.Marshal(map[string]any{"hooks": map[string]any{"Notification": hook, "UserPromptSubmit": hook}})
	if err := os.MkdirAll(filepath.Join(cwd, ".atto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".atto", "settings.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	w := session.New(cwd)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "do the thing"}})
	w.Close()
	path = w.Path

	projectHooks, err := config.ProjectHooks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range projectHooks {
		if err := config.SetHookApproval(h, true); err != nil {
			t.Fatal(err)
		}
	}
	quiet(t)

	if err := RunContinue([]string{w.ID}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b := bodies()
	if len(b) != 1 || strings.Count(b[0], `"role":"user"`) != 1 || !strings.Contains(b[0], "do the thing") {
		t.Fatalf("the model answers the stopped message, nothing new is sent: %v", b)
	}
	if !lockedDuring() {
		t.Fatal("the session is locked while the run writes it")
	}
	if _, ok := session.LockedBy(path); ok {
		t.Fatal("the lock is released at the end")
	}
	_, entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, e := range entries {
		if e.Type == session.TypeMessage {
			texts = append(texts, e.Message.Role+": "+e.Message.Content)
		}
	}
	if fmt.Sprint(texts) != "[user: do the thing assistant: all done]" {
		t.Fatalf("session: %v", texts)
	}
	// The Notification hook fires once, and the prompt hook never (no new message).
	data, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("hook calls: %q", data)
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &in); err != nil {
		t.Fatal(err)
	}
	if in["hook_event_name"] != "Notification" || in["notification_type"] != "background_done" || !strings.Contains(fmt.Sprint(in["message"]), w.ID) {
		t.Fatalf("hook input %v", in)
	}
}

func TestContinueRefusesASessionAnotherProcessWrites(t *testing.T) {
	var path string
	bodies, _ := continueServer(t, &path)
	_, w := bgSession(t)
	path = w.Path
	if _, err := session.LockFor(path, os.Getppid()); err != nil {
		t.Fatal(err)
	}
	quiet(t)
	if err := RunContinue([]string{w.ID}, io.Discard); !errors.Is(err, session.ErrLocked) {
		t.Fatalf("continue: %v", err)
	}
	// So does a plain -p run into the session.
	if err := RunPrint(PrintOptions{Prompt: "more", Resume: w.ID}); !errors.Is(err, session.ErrLocked) {
		t.Fatalf("-p -session: %v", err)
	}
	if len(bodies()) != 0 {
		t.Fatal("nothing was sent")
	}
}

func TestContinueArgs(t *testing.T) {
	if err := RunContinue(nil, io.Discard); err == nil {
		t.Fatal("needs a session id")
	}
	t.Setenv(config.EnvAgent, "1")
	if err := RunContinue([]string{"x"}, io.Discard); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("an agent may not: %v", err)
	}
}

func TestBgPrepare(t *testing.T) {
	user := session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "u"}}
	tool := session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "tool", Content: "t"}}
	asst := session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "a"}}
	g, _ := goal.New("ship it")
	g.TokensUsed = 1234
	raw, _ := json.Marshal(g)
	snap := session.Entry{Type: session.TypeGoal, Goal: raw}
	t.Setenv(config.EnvDir, t.TempDir())

	prep := func(entries ...session.Entry) (bgMode, *core.GoalDriver) {
		var e []session.Entry
		for i, x := range entries { // chain them as a branch
			x.ID = fmt.Sprint(i + 1)
			if i > 0 {
				x.Parent = fmt.Sprint(i)
			}
			e = append(e, x)
		}
		d := &core.GoalDriver{Session: "s1"}
		return bgPrepare(d, core.Saved{Entries: e}), d
	}
	if m, _ := prep(user); m != bgResumeTurn {
		t.Fatalf("user message unanswered: %v", m)
	}
	if m, _ := prep(user, asst, tool); m != bgResumeTurn {
		t.Fatalf("tool results unanswered: %v", m)
	}
	if m, _ := prep(user, asst); m != bgNothing {
		t.Fatalf("finished turn: %v", m)
	}
	m, d := prep(user, asst, snap)
	if m != bgGoalTurn || !d.Active() || d.Goal.TokensUsed != 1234 {
		t.Fatalf("an active goal goes on, usage kept: %v %+v", m, d.Goal)
	}
	if m, d := prep(user, snap); m != bgResumeTurn || !d.Active() {
		t.Fatalf("goal turn interrupted: %v", m)
	}
}

func TestSessionsListMarksRunning(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	cwd := t.TempDir()
	t.Chdir(cwd)
	w := session.New(cwd)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "long job"}})
	w.Close()
	var out bytes.Buffer
	if err := RunSessions([]string{"list"}, &out); err != nil || strings.Contains(out.String(), "running") || strings.Contains(out.String(), "STATUS") {
		t.Fatalf("not running: %v\n%s", err, out.String())
	}
	release, err := session.Lock(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	out.Reset()
	if err := RunSessions([]string{"list"}, &out); err != nil || !strings.Contains(out.String(), "STATUS") || !strings.Contains(out.String(), "running") {
		t.Fatalf("running not shown: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := RunSessions([]string{"list", "-json"}, &out); err != nil || !strings.Contains(out.String(), `"running": true`) {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
}

func TestOrdinaryPrintHoldsWriterLease(t *testing.T) {
	var path string
	_, lockedDuring := continueServer(t, &path)
	_, w := bgSession(t)
	path = w.Path
	if err := RunPrint(PrintOptions{Prompt: "continue", Resume: w.ID}); err != nil {
		t.Fatal(err)
	}
	if !lockedDuring() {
		t.Fatal("ordinary run had no writer lease")
	}
	if _, ok := session.LockedBy(path); ok {
		t.Fatal("lease not released")
	}
}

func TestBgPrepareAfterCompaction(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	w := session.New(t.TempDir())
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "unfinished"}})
	w.Append(session.Entry{Type: session.TypeCompaction, Replacement: []provider.Message{{Role: "user", Content: "notes"}}})
	w.Close()
	saved, err := core.Read(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	d := &core.GoalDriver{Session: w.ID}
	if len(saved.Branch()) != 1 || bgPrepare(d, saved) != bgResumeTurn {
		t.Fatal("compaction must not hide the unanswered message from background preparation")
	}
}
