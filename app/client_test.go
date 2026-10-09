package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
)

// The terminal against its runtime: the interactive flows end to end.

// Enter during a turn steers it, Shift+Left takes the steer back, Tab
// queues a follow-up that runs once the turn ends.
func TestSteerTakeBackAndQueue(t *testing.T) {
	gate := make(chan struct{})
	a, m := liveApp(t, providertest.Reply{Text: "first", Gate: gate}, providertest.Reply{Text: "second"})
	typeLine(a, "start")
	m.Started(5 * time.Second)
	typeLine(a, "a steer")
	within(t, a, "the steer pending", func() bool { return strings.Contains(footerText(a, 100), "a steer") })
	key(a, "\x1b[1;2D") // shift+left
	within(t, a, "the steer back in the editor", func() bool { return a.editor.Text() == "a steer" && len(a.pending.Steers) == 0 })
	a.ui.Do(func() { a.editor.SetText("queued-x9") })
	key(a, "\t")
	within(t, a, "the queued follow-up", func() bool { return len(a.pending.Queued) == 1 })
	if f := footer(a, 100); !strings.Contains(f, "Queued follow-up inputs") {
		t.Fatalf("footer:\n%s", f)
	}
	close(gate)
	within(t, a, "the follow-up turn", func() bool { return strings.Contains(strings.Join(userBlocks(a), ","), "queued-x9") })
	waitIdle(t, a)
	reqs := m.Requests()
	if len(reqs) != 2 || strings.Contains(strings.Join(reqs, ""), "a steer") || !strings.Contains(reqs[1], "queued-x9") {
		t.Fatalf("%d requests: %v", len(reqs), reqs)
	}
}

// Ctrl+Enter interrupts the running turn and sends the draft at once.
func TestCtrlEnterClientRequest(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, m := liveApp(t, providertest.Reply{Text: "never", Gate: gate}, providertest.Reply{Text: "answered"})
	typeLine(a, "slow question")
	m.Started(5 * time.Second)
	a.ui.Do(func() { a.sendNowFromEditor("right now", nil) })
	within(t, a, "the new turn's answer", func() bool { return strings.Contains(bodyText(a), "answered") })
	if got := users(a); got != "slow question,right now" {
		t.Fatalf("user messages %q", got)
	}
	if s := shown(a); !strings.Contains(s, "Interrupted.") {
		t.Fatalf("transcript:\n%s", s)
	}
}

// A turn the model never answered gives the message back to the editor.
func TestFailedTurnClientRecovery(t *testing.T) {
	a, _ := liveApp(t, providertest.Reply{Status: 404})
	typeLine(a, "hello there")
	within(t, a, "the message back", func() bool { return a.editor.Text() == "hello there" })
	if s := shown(a); !strings.Contains(s, "Error:") {
		t.Fatalf("no error shown:\n%s", s)
	}
}

// A "!" command runs in the runtime; its output reaches the model with
// the next turn.
func TestShellCommandRuns(t *testing.T) {
	a, m := liveApp(t, providertest.Reply{Text: "seen"})
	typeLine(a, "!echo from-the-shell")
	within(t, a, "the shell block", func() bool {
		return a.shellBlk != nil && a.shellBlk.done && strings.Contains(bodyText(a), "from-the-shell")
	})
	send(t, a, "what did it print?")
	if reqs := m.Requests(); len(reqs) != 1 || !strings.Contains(reqs[0], "from-the-shell") {
		t.Fatalf("requests %v", reqs)
	}
}

// /goal sets the goal, which starts working; another objective asks
// first (a prompt of the runtime, shown here), and /goal shows it.
func TestGoalCommandsAndReplacePrompt(t *testing.T) {
	a, _ := liveApp(t, providertest.Reply{Text: "working", Words: 2, Delay: 30 * time.Millisecond})
	typeLine(a, "/goal ship the feature")
	within(t, a, "the goal", func() bool { return a.theGoal() != nil })
	typeLine(a, "/goal pause")
	within(t, a, "the goal paused", func() bool { g := a.theGoal(); return g != nil && g.Status == "paused" })
	waitIdle(t, a)
	typeLine(a, "/goal write the docs")
	within(t, a, "the confirmation", func() bool { _, ok := a.modal.(goalPrompt); return ok })
	if s := screen(a); !strings.Contains(s, "Replace goal?") || !strings.Contains(s, "write the docs") {
		t.Fatalf("prompt:\n%s", s)
	}
	key(a, "\r")
	within(t, a, "the new goal", func() bool { g := a.theGoal(); return g != nil && g.Objective == "write the docs" })
	typeLine(a, "/goal pause")
	waitIdle(t, a)
	typeLine(a, "/goal")
	within(t, a, "the summary", func() bool { return strings.Contains(bodyText(a), "Objective: write the docs") })
}

const dialogExtension = `
export default function (atto: any) {
  atto.registerCommand("demo", {
    description: "Demo things",
    handler: async (args: string, ctx: any) => {
      ctx.ui.setWidget("w", ["widget " + args]);
      const pick = await ctx.ui.select("Pick one", ["red", "green"]);
      const ok = await ctx.ui.confirm("Sure?");
      const name = await ctx.ui.input("Name?");
      ctx.ui.notify("picked " + pick + " " + ok + " " + name + " " + ctx.hasUI, "warning");
    },
  });
}
`

// An extension's dialogs are the runtime's prompts, answered here.
func TestExtensionDialogsInTerminal(t *testing.T) {
	if !extensions.Supported {
		t.Skip("requires the JS extension engine")
	}
	a := extApp(t, dialogExtension, newMainServer(t, reply{"", "ok"}), "")
	within(t, a, "the command", func() bool { return hasCommand(a.allCommands(), "demo") })
	typeLine(a, "/demo now")
	within(t, a, "the select", func() bool { return strings.Contains(screenText(a), "Pick one") })
	key(a, "\x1b[B") // down: green
	key(a, "\r")
	within(t, a, "the confirm", func() bool { return strings.Contains(screenText(a), "Sure?") })
	key(a, "\r") // Yes
	within(t, a, "the input", func() bool { return strings.Contains(screenText(a), "Name?") })
	for _, r := range "bob" {
		key(a, string(r))
	}
	key(a, "\r")
	within(t, a, "the notice", func() bool { return strings.Contains(bodyText(a), "[demo] picked green true bob true") })
	if w := footer(a, 80); !strings.Contains(w, "widget now") {
		t.Fatalf("widget:\n%s", w)
	}
}

// /clear starts a new session; the one left ends with SessionEnd
// (reason clear), as before.
func TestClearEndsTheSessionLeft(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("hook commands are bash here")
	}
	cwd, _ := testEnv(t, providertest.Reply{Text: "ok"})
	log := filepath.Join(t.TempDir(), "end.log")
	writeTestFile(t, filepath.Join(config.Dir(), "settings.json"), `{"hooks":{"SessionEnd":[{"hooks":[{"type":"command","command":`+jsonString(hooktest.LogStdin(log))+`}]}]}}`)
	a := startApp(t, cwd)
	send(t, a, "hello")
	old := a.threadID
	typeLine(a, "/clear")
	within(t, a, "the new session", func() bool {
		return a.threadID != old && strings.Contains(bodyText(a), "Started a new conversation.")
	})
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), `"reason":"clear"`) || !strings.Contains(string(b), old) {
		t.Fatalf("SessionEnd: %s", b)
	}
	if users(a) != "" {
		t.Fatal("the new session shows the old transcript")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// "Run in background" without the daemon hands the session to a
// background run (stubbed here) and exits.
func TestRunInBackgroundHandsOff(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, m := liveApp(t, providertest.Reply{Text: "never", Gate: gate})
	spawned := make(chan string, 1)
	old := server.Spawn
	server.Spawn = func(id, path, cwd string) (string, error) {
		// The real one hands the lease to the child; here it is let go.
		spawned <- id
		return "/tmp/x.log", nil
	}
	t.Cleanup(func() { server.Spawn = old })
	typeLine(a, "long task")
	m.Started(5 * time.Second)
	within(t, a, "busy", func() bool { return a.busy })
	a.ui.Do(a.requestQuit)
	if s := screen(a); !strings.Contains(s, "Run in background") {
		t.Fatalf("menu:\n%s", s)
	}
	key(a, "2")
	select {
	case id := <-spawned:
		a.ui.Do(func() {
			if id != a.threadID {
				t.Fatalf("spawned %s", id)
			}
		})
	case <-time.After(10 * time.Second):
		t.Fatal("no background run")
	}
	within(t, a, "atto to exit", func() bool { return quitting(a) && strings.Contains(a.bgLine, "Running in background") })
}

// A session another process runs is shown read-only; input is refused.
func TestReadOnlyProtocolSnapshot(t *testing.T) {
	cwd, _ := testEnv(t)
	w := saved(t, cwd, "user", "u1", "assistant", "a1")
	release, err := session.LockFor(w.Path, os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a := startApp(t, cwd)
	a.ui.Do(func() { a.resumeID(w.ID) })
	within(t, a, "the read-only view", func() bool { return a.readOnly != "" && strings.Join(userBlocks(a), ",") == "u1" })
	typeLine(a, "may I?")
	within(t, a, "the refusal", func() bool { return a.editor.Text() == "may I?" })
	if b := strings.Join(a.renderReadOnly(120), ""); !strings.Contains(b, "ctrl+r") {
		t.Fatalf("banner %q", b)
	}
}

// /remote only says that the web UI is being rebuilt and how to attach
// other clients meanwhile.
func TestRemoteCommandPointsToAppServer(t *testing.T) {
	a, _ := liveApp(t)
	typeLine(a, "/remote")
	within(t, a, "the pointer", func() bool {
		text := bodyText(a)
		return strings.Contains(text, "web UI is being rebuilt") && strings.Contains(text, "atto app-server --listen ws://HOST:PORT")
	})
}

// footerText and screenText draw without taking the UI lock: for
// conditions of within, which holds it.
func footerText(a *App, width int) string { return plainLines(a.ui.Footer.Render(width)) }

func screenText(a *App) string {
	lines := a.ui.Body.Render(80)
	if a.modal != nil {
		lines = append(lines, a.modal.Render(80)...)
	}
	return plainLines(lines)
}

// A snapshot covers transcript state, not addressed one-shot actions. A
// request reply may overtake a recovered draft on the event goroutine.
func TestSnapshotDoesNotDiscardRecoveredInput(t *testing.T) {
	a, _ := liveApp(t)
	var info server.ThreadInfo
	if err := a.conn.c.Call(context.Background(), "thread/read", map[string]any{"threadId": a.threadID}, &info); err != nil {
		t.Fatal(err)
	}
	a.ui.Do(func() {
		info.EventID += 100
		a.applySnapshot(info)
		raw, _ := json.Marshal(map[string]any{"threadId": a.threadID, "clientId": a.conn.id, "text": "recovered draft"})
		n := server.Notification{Method: "input/recovered", EventID: info.EventID - 1, Params: raw}
		a.onNotification(n)
		if a.editor.Text() != "recovered draft" {
			t.Fatal("snapshot dropped draft")
		}
		a.editor.SetText("edited")
		a.onNotification(n)
		if a.editor.Text() != "edited" {
			t.Fatal("duplicate recovery overwrote editor")
		}
	})
}

// Read-only display is lightweight, but its footer still uses session-wide
// totals and the latest response (not those totals) for the cache figures.
func TestReadOnlySnapshotLatestUsage(t *testing.T) {
	cwd, _ := testEnv(t)
	w := session.New(cwd)
	w.Append(session.Entry{Type: session.TypeModel, Provider: "fake", Model: "m"})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "one"}, Usage: &provider.Usage{PromptTokens: 100, CachedTokens: 10, CompletionTokens: 2}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "two"}, Usage: &provider.Usage{PromptTokens: 200, CachedTokens: 150, CompletionTokens: 3}})
	w.Close()
	release, err := session.LockFor(w.Path, os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a := startApp(t, cwd, Options{Session: w.ID})
	a.ui.Do(func() {
		if a.readOnly == "" || a.model().Model.ID != "m" || a.info.ContextWindow != 100000 {
			t.Fatalf("read-only model %+v", a.info)
		}
		if a.usage.input != 300 || a.usage.output != 5 || a.usage.last.PromptTokens != 200 || a.usage.cacheLabel() != "cache 75%" {
			t.Fatalf("usage %+v", a.usage)
		}
	})
}

func TestCompactAndContextClientCommands(t *testing.T) {
	a, model := liveApp(t, providertest.Reply{Text: "original answer", Prompt: 100, Completion: 5}, providertest.Reply{Text: "handoff notes", Prompt: 200, Completion: 10})
	send(t, a, "original question")
	send(t, a, "/compact")
	if s := shown(a); !strings.Contains(s, "Context compacted") {
		t.Fatalf("compaction block:\n%s", s)
	}
	_, entries, err := session.Load(a.sessPath)
	if err != nil {
		t.Fatal(err)
	}
	compacted := false
	for _, e := range entries {
		if e.Type == session.TypeCompaction {
			compacted = true
		}
	}
	if !compacted || len(model.Requests()) != 2 {
		t.Fatalf("compaction persisted=%v, requests=%d", compacted, len(model.Requests()))
	}
	typeLine(a, "/context")
	within(t, a, "context report", func() bool {
		return strings.Contains(bodyText(a), "system prompt") && strings.Contains(bodyText(a), "This session")
	})
}

func TestRequestAndDebugClientCommands(t *testing.T) {
	a, _ := liveApp(t, providertest.Reply{Text: "debug answer"})
	send(t, a, "debug question")
	typeLine(a, "/request")
	within(t, a, "saved request notice", func() bool { return strings.Contains(bodyText(a), "Saved the last request") })
	b, err := os.ReadFile(filepath.Join(config.Dir(), "cache", "last-request.json"))
	if err != nil || !json.Valid(b) || !strings.Contains(string(b), "debug question") {
		t.Fatalf("request %s: %v", b, err)
	}
	typeLine(a, "/debug")
	within(t, a, "debug report", func() bool { return strings.Contains(bodyText(a), "Saved a heap profile") })
	matches, err := filepath.Glob(filepath.Join(config.Dir(), "debug", "*", "requests", "recent-*.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("debug requests %v: %v", matches, err)
	}
	found := false
	for _, path := range matches {
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), "debug question") {
			found = true
		}
	}
	if !found {
		t.Fatal("debug report omitted runtime's request")
	}
}

// Protocol delivery is asynchronous: a key may precede turn/started or
// turn/pending on the UI goroutine. The ordered runtime chooses the action.
func TestSendNowWithLaggingBusyMirror(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, model := liveApp(t, providertest.Reply{Text: "old", Gate: gate}, providertest.Reply{Text: "replacement"})
	typeLine(a, "original")
	model.Started(5 * time.Second)
	a.ui.Do(func() {
		a.busy, a.runKind = false, ""
		a.sendNowFromEditor("send now", nil)
	})
	within(t, a, "runtime send-now despite lagging mirror", func() bool { return strings.Contains(bodyText(a), "replacement") })
	waitIdle(t, a)
	if users(a) != "original,send now" || !strings.Contains(shown(a), "Interrupted.") {
		t.Fatalf("replacement transcript:\n%s", shown(a))
	}
}

func TestTakebackBeforePendingNotification(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, model := liveApp(t, providertest.Reply{Text: "old", Gate: gate})
	typeLine(a, "original")
	model.Started(5 * time.Second)
	a.ui.Do(func() {
		a.submit("take this back", nil)
		// The event goroutine cannot apply turn/pending until this UI handler
		// returns; the request goroutine still sends these two calls in order.
		a.pending = server.PendingInput{}
		if !a.takeBackLast() {
			t.Fatal("takeback relied on the UI mirror")
		}
	})
	within(t, a, "ordered takeback", func() bool { return a.editor.Text() == "take this back" && len(a.pending.Steers) == 0 })
}

func TestWorkerConnectionSnapshotPreservesCommandClock(t *testing.T) {
	a, m := liveApp(t, providertest.Reply{Command: "sleep 30", Description: "Attach command clock"})
	typeLine(a, "run the clock")
	if m.Started(5*time.Second) == 0 {
		t.Fatal("no model request")
	}
	var id string
	var started time.Time
	within(t, a, "running tool", func() bool {
		for key, tool := range a.tools {
			if !tool.pending {
				id, started = key, tool.start
				return true
			}
		}
		return false
	})
	worker := a.conn.own
	old := a.conn
	cn, err := dialConn(server.Connect(context.Background(), worker), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.ui.Do(func() { a.quitting = true }); cn.c.Close(); worker.Close() })
	var info server.ThreadInfo
	if err := cn.c.Call(context.Background(), "thread/attach", map[string]any{"threadId": a.threadID}, &info); err != nil {
		t.Fatal(err)
	}
	a.ui.Do(func() { a.use(cn); a.show(info) })
	old.c.Close()
	a.ui.Do(func() {
		tool := a.tools[id]
		if tool == nil || tool.start.Sub(started) > time.Millisecond || started.Sub(tool.start) > time.Millisecond {
			t.Fatalf("reattach reset command clock: before %v after %+v", started, tool)
		}
	})
}

func TestCloseCommandUsesCloseReason(t *testing.T) {
	a, _ := liveApp(t)
	witness := server.Connect(context.Background(), a.conn.own)
	defer witness.Close()
	if err := witness.Call(context.Background(), "ping", nil, nil); err != nil {
		t.Fatal(err)
	}
	typeLine(a, "/close")
	within(t, a, "close exits the client", func() bool { return quitting(a) })
	for {
		select {
		case n := <-witness.Events():
			if n.Method != "thread/closed" {
				continue
			}
			var p struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(n.Params, &p)
			if p.Reason != "close" {
				t.Fatalf("close reason %q", p.Reason)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("no session close")
		}
	}
}
