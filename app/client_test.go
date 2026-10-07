package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
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
func TestCtrlEnterSendsNow(t *testing.T) {
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
func TestFailedTurnRestoresTypedText(t *testing.T) {
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
		if id != a.threadID {
			t.Fatalf("spawned %s", id)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no background run")
	}
	within(t, a, "atto to exit", func() bool { return quitting(a) && strings.Contains(a.bgLine, "Running in background") })
}

// A session another process runs is shown read-only; input is refused.
func TestReadOnlyLockedSession(t *testing.T) {
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

// /remote serves the session shown here to a browser: the web client's
// requests reach the same runtime, and what it sends shows here.
func TestRemoteGateway(t *testing.T) {
	a, _ := liveApp(t, providertest.Reply{Text: "hi web"})
	port := 0
	a.ui.Do(func() {
		a.remoteHost, a.remotePort = "127.0.0.1", &port
		a.cmdRemote("on")
	})
	var addr, token string
	a.ui.Do(func() { addr, token = a.remote.addr, a.remote.token })
	call := func(method string, params map[string]any) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", "http://"+addr+"/rpc", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var r struct {
			Result map[string]any `json:"result"`
			Error  *struct{ Message string }
		}
		json.NewDecoder(resp.Body).Decode(&r)
		if r.Error != nil {
			t.Fatalf("%s: %s", method, r.Error.Message)
		}
		return r.Result
	}
	init := call("initialize", nil)
	if init["live"] != true || init["threadId"] != a.threadID {
		t.Fatalf("initialize %v", init)
	}
	call("turn/start", map[string]any{"input": "hello from the phone"})
	within(t, a, "the remote message", func() bool {
		return strings.Contains(bodyText(a), "from remote") && strings.Contains(bodyText(a), "hi web")
	})
	a.ui.Do(func() { a.cmdRemote("off") })
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
