package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/mcp"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
)

// fakeHost records what extensions ask of the front end.
type fakeHost struct {
	mu      sync.Mutex
	ui      bool
	notices []string
	status  map[string]string // ext/key
	widgets map[string][]string
	asked   []Question
	answers []any // given in order; then the default
	sent    []string
	cleared []string
	// block status and display text, by ext/blockID.
	blockStatus  map[string]string
	blockDisplay map[string]string
	texts        []shownText
	names        []string // SetSessionName
}

func (h *fakeHost) SetSessionName(ext, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.names = append(h.names, name)
	return nil
}

// shownText is a call of ctx.ui.showText.
type shownText struct {
	ext, title, text string
	opts             TextOptions
}

func (h *fakeHost) shown() []shownText {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.texts)
}

func (h *fakeHost) ShowText(ext, title, text string, o TextOptions) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.texts = append(h.texts, shownText{ext, title, text, o})
}

func newHost(ui bool) *fakeHost {
	return &fakeHost{ui: ui, status: map[string]string{}, widgets: map[string][]string{}}
}

func (h *fakeHost) HasUI() bool { return h.ui }

func (h *fakeHost) SetBlockStatus(ext, id, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.blockStatus == nil {
		h.blockStatus = map[string]string{}
	}
	h.blockStatus[ext+"/"+id] = text
}

func (h *fakeHost) SetBlockDisplay(ext, id, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.blockDisplay == nil {
		h.blockDisplay = map[string]string{}
	}
	h.blockDisplay[ext+"/"+id] = text
}

func (h *fakeHost) Notify(ext, text, level string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notices = append(h.notices, fmt.Sprintf("%s %s: %s", level, ext, text))
}

func (h *fakeHost) SetStatus(ext, key, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if text == "" {
		delete(h.status, ext+"/"+key)
		return
	}
	h.status[ext+"/"+key] = text
}

func (h *fakeHost) SetWidget(ext, key string, lines []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if lines == nil {
		delete(h.widgets, ext+"/"+key)
		return
	}
	h.widgets[ext+"/"+key] = lines
}

func (h *fakeHost) Ask(ext string, q Question, answer func(any)) {
	h.mu.Lock()
	h.asked = append(h.asked, q)
	var v any
	if len(h.answers) > 0 {
		v, h.answers = h.answers[0], h.answers[1:]
	} else if q.Kind == "confirm" {
		v = false
	}
	h.mu.Unlock()
	go answer(v) // from another goroutine, as a front end does
}

func (h *fakeHost) ClearUI(ext string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleared = append(h.cleared, ext)
	for k := range h.status {
		if strings.HasPrefix(k, ext+"/") {
			delete(h.status, k)
		}
	}
	for k := range h.widgets {
		if strings.HasPrefix(k, ext+"/") {
			delete(h.widgets, k)
		}
	}
}

func (h *fakeHost) SendMessage(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, text)
}

// hostState is a copy of what a fakeHost recorded.
type hostState struct {
	notices, sent, cleared []string
	asked                  []Question
	status                 map[string]string
	widgets                map[string][]string
}

func (h *fakeHost) snapshot() hostState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return hostState{notices: slices.Clone(h.notices), sent: slices.Clone(h.sent), asked: slices.Clone(h.asked),
		cleared: slices.Clone(h.cleared), status: clone(h.status), widgets: cloneW(h.widgets)}
}

func clone(m map[string]string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, m)
	return out
}

func cloneW(m map[string][]string) map[string][]string {
	out := map[string][]string{}
	maps.Copy(out, m)
	return out
}

// env gives a test its own ATTO_DIR and a project (a git root); it
// returns the user extensions dir and the project's cwd.
func env(t *testing.T) (userDir, cwd string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("ATTO_DIR", filepath.Join(root, "atto"))
	cwd = filepath.Join(root, "proj")
	write(t, filepath.Join(cwd, ".git", "HEAD"), "x")
	if err := config.Ensure(); err != nil {
		t.Fatal(err)
	}
	// One second keeps the timeout tests short.
	write(t, config.SettingsPath(), `{"extensions":{"timeout":1}}`)
	return config.ExtensionsDir(), cwd
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, cwd string, h Host) *Manager {
	t.Helper()
	m := Load(Options{Cwd: cwd, Host: h})
	t.Cleanup(m.Close)
	return m
}

func info(t *testing.T, m *Manager, name string) Info {
	t.Helper()
	for _, in := range m.Report() {
		if in.Name == name {
			return in
		}
	}
	t.Fatalf("no extension %q in %+v", name, m.Report())
	return Info{}
}

// eventually waits for cond, for what extensions do asynchronously.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The spike: TypeScript with the syntax extensions are written in, a
// relative import, bundled down to what goja runs.
func TestModernTypeScript(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "modern", "lib", "util.ts"), `
export class Greeter {
  #n = 1;
  static count = 0;
  constructor(private name: string) { Greeter.count++; }
  greet(): string { return `+"`hi ${this.name} ${this.#n}`"+`; }
}
export const add = (a: number, b: number = 2): number => a + b;
`)
	write(t, filepath.Join(dir, "modern", "index.ts"), `
import { Greeter, add } from "./lib/util";
interface Opts { deep?: { v?: number } }
async function twice(n: number): Promise<number> { await null; return n * 2; }
async function* gen() { yield 1; yield 2; }
export default async function (atto: any) {
  const o: Opts = {};
  const { a, ...rest } = { a: 1, b: 2, c: 3 };
  let total = 0;
  for await (const v of gen()) total += v;
  const out = [new Greeter("x").greet(), add(1), o.deep?.v ?? "none", Object.keys(rest).join(","),
    new Map([["k", 1]]).get("k"), [1, [2, [3]]].flat(2).length, 2 ** 10, await twice(21), total, 10n ** 3n === 1000n];
  atto.fs.writeFile("out.json", JSON.stringify(out));
}
`)
	m := load(t, cwd, newHost(false))
	if in := info(t, m, "modern"); in.Status != Loaded {
		t.Fatalf("%+v", in)
	}
	got, err := os.ReadFile(filepath.Join(cwd, "out.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `["hi x 1",3,"none","b,c",1,3,1024,42,3,true]`; string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestBundleErrors(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "bad.ts"), "export default function (atto) {\n  const x = ;\n}\n")
	write(t, filepath.Join(dir, "missing.ts"), "import { x } from './nope';\nexport default function () { x }\n")
	m := load(t, cwd, newHost(false))
	bad := info(t, m, "bad")
	if bad.Status != Failed || !strings.Contains(bad.Error, filepath.Join(dir, "bad.ts")+":2:13:") {
		t.Fatalf("a syntax error names file:line:col: %+v", bad)
	}
	if in := info(t, m, "missing"); in.Status != Failed || !strings.Contains(in.Error, "missing.ts:1:") || !strings.Contains(in.Error, "./nope") {
		t.Fatalf("%+v", in)
	}
}

func TestLoadFailures(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "noexport.ts"), "const x = 1;\n")
	write(t, filepath.Join(dir, "throws.ts"), "export default function () {\n  throw new Error('boom');\n}\n")
	write(t, filepath.Join(dir, "spins.js"), "export default function () { while (true) {} }\n")
	write(t, filepath.Join(dir, "badevent.js"), "export default function (atto) { atto.on('nope', () => {}) }\n")
	write(t, filepath.Join(dir, "ok.js"), "export default function () {}\n")
	m := load(t, cwd, newHost(false))
	for name, want := range map[string]string{
		"noexport": "no default export",
		"throws":   "boom",
		"spins":    "interrupted",
		"badevent": `unknown event "nope"`,
	} {
		if in := info(t, m, name); in.Status != Failed || !strings.Contains(in.Error, want) {
			t.Errorf("%s: %+v, want %q", name, in, want)
		}
	}
	if in := info(t, m, "throws"); !strings.Contains(in.Error, "throws.ts:2") {
		t.Errorf("the stack points into the source (source map): %q", in.Error)
	}
	if in := info(t, m, "ok"); in.Status != Loaded {
		t.Fatalf("one failure does not stop the others: %+v", in)
	}
}

func TestDiscover(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "a.ts"), "export default () => {}")
	write(t, filepath.Join(dir, "b.js"), "export default () => {}")
	write(t, filepath.Join(dir, "types.d.ts"), "declare const x: number;")
	write(t, filepath.Join(dir, ".hidden.ts"), "export default () => {}")
	write(t, filepath.Join(dir, "notes.md"), "no")
	write(t, filepath.Join(dir, "folder", "index.ts"), "export default () => {}")
	write(t, filepath.Join(dir, "folder", "helper.ts"), "export const y = 1")
	write(t, filepath.Join(dir, "empty", "readme.md"), "no index")
	write(t, filepath.Join(cwd, ".atto", "extensions", "proj.ts"), "export default () => {}")
	sub := filepath.Join(cwd, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range Discover(sub) { // the project is found from below its root
		got = append(got, s.Source+":"+s.Name+":"+filepath.Base(s.Path))
	}
	want := []string{"user:a:a.ts", "user:b:b.js", "user:folder:index.ts", "project:proj:proj.ts", "builtin:autorename:autorename.ts", "builtin:diff:diff.ts"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestProjectApproval(t *testing.T) {
	_, cwd := env(t)
	path := filepath.Join(cwd, ".atto", "extensions", "guard.ts")
	write(t, path, "export default (atto) => atto.registerCommand('guard', { handler() {} })")
	m := load(t, cwd, newHost(false))
	if in := info(t, m, "guard"); in.Status != NeedsApproval || in.Source != Project || len(in.Commands) > 0 {
		t.Fatalf("an unapproved project extension does not run: %+v", in)
	}
	if _, err := Approve(cwd, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := Approve(cwd, "guard"); err != nil {
		t.Fatal(err)
	}
	m.Reload()
	if in := info(t, m, "guard"); in.Status != Loaded || !slices.Equal(in.Commands, []string{"guard"}) {
		t.Fatalf("approved, it runs: %+v", in)
	}
	write(t, path, "export default (atto) => atto.registerCommand('guard2', { handler() {} })")
	m.Reload()
	if in := info(t, m, "guard"); in.Status != NeedsApproval {
		t.Fatalf("a changed file needs approval again: %+v", in)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".atto", "extensions", TypesFile)); err != nil {
		t.Fatal("atto.d.ts is written next to extensions:", err)
	}
}

func TestUserExtensionNeedsNoApproval(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "mine.ts"), "export default () => {}")
	load(t, cwd, newHost(false))
	if _, err := Approve(cwd, "mine"); err == nil || !strings.Contains(err.Error(), "need no approval") {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, TypesFile)); err != nil || string(b) != Types {
		t.Fatal("atto.d.ts is written next to user extensions", err)
	}
}

func TestDisabledInSettings(t *testing.T) {
	dir, cwd := env(t)
	write(t, config.SettingsPath(), `{"extensions":{"disabled":["off"]}}`)
	write(t, filepath.Join(dir, "off.ts"), "export default (atto) => atto.registerCommand('x', { handler() {} })")
	m := load(t, cwd, newHost(false))
	if in := info(t, m, "off"); in.Status != Disabled || len(in.Commands) > 0 {
		t.Fatalf("%+v", in)
	}
}

// fixture is an extension that uses each event.
const fixture = `
export default function (atto: Atto) {
  atto.on("session_start", (e) => atto.fs.writeFile("events.log", "start:" + e.reason + "\n"));
  const log = (s: string) => atto.fs.writeFile("events.log", atto.fs.readFile("events.log") + s + "\n");
  atto.on("turn_start", (e) => log("turn_start:" + e.prompt));
  atto.on("turn_end", (e) => log("turn_end:" + e.error + ":" + e.aborted));
  atto.on("session_end", async (e) => { await null; log("end:" + e.reason); });
  atto.on("tool_call", (e) => {
    if (e.command.includes("rm -rf")) return { block: true, reason: "too dangerous" };
    if (e.command === "ls") return { command: "ls -la" };
  });
  atto.on("tool_call", async (e) => {
    await new Promise((r) => setTimeout(r, 10));
    if (e.command === "ls -la") return { command: "ls -la --color=never" }; // sees the first rewrite
  });
  atto.on("tool_result", (e) => e.output.replace(/sk-[a-z0-9]+/g, "[redacted]"));
  atto.on("tool_result", async (e) => ({ output: e.output + " (" + e.exitCode + ")" }));
  atto.on("user_prompt", (e) => e.prompt.includes("deploy") ? "Deploys need a ticket." : undefined);
  atto.on("user_prompt", (e) => e.prompt === "forbidden" ? { block: true, reason: "not that" } : undefined);
}
`

func TestEvents(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "fixture.ts"), "/// <reference path=\"./atto.d.ts\" />\n"+fixture)
	m := load(t, cwd, newHost(false))
	if in := info(t, m, "fixture"); in.Status != Loaded || len(in.Events) != 7 {
		t.Fatalf("%+v", in)
	}
	ctx := context.Background()
	readLog := func() string { b, _ := os.ReadFile(filepath.Join(cwd, "events.log")); return string(b) }

	m.SessionStart("startup")
	eventually(t, "session_start", func() bool { return readLog() == "start:startup\n" })

	args, o := m.ToolCall(ctx, agent.BashArgs{Command: "rm -rf /tmp/x"})
	if !o.Block || o.Reason != "fixture: too dangerous" {
		t.Fatalf("blocked: %+v", o)
	}
	args, o = m.ToolCall(ctx, agent.BashArgs{Command: "ls"})
	if o.Block || args.Command != "ls -la --color=never" {
		t.Fatalf("rewritten twice, in order: %q %+v", args.Command, o)
	}
	out, _ := m.ToolResult(ctx, args, agent.BashResult{ExitCode: 3}, "key sk-abc123 here")
	if out != "key [redacted] here (3)" {
		t.Fatalf("redacted, then appended: %q", out)
	}
	o = m.UserPrompt(ctx, "please deploy")
	if o.Block || o.Context != "Deploys need a ticket." {
		t.Fatalf("%+v", o)
	}
	if o = m.UserPrompt(ctx, "forbidden"); !o.Block || o.Reason != "fixture: not that" {
		t.Fatalf("%+v", o)
	}
	m.TurnStart("hello")
	m.TurnEnd(context.Canceled)
	m.SessionEnd("exit") // waits for the async handler
	if got, want := readLog(), "start:startup\nturn_start:hello\nturn_end:context canceled:true\nend:exit\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The agent runs the hooks: tool_call can block a command and rewrite
// it, tool_result rewrites what the model receives.
func TestAgentRunsExtensions(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "fixture.ts"), fixture)
	m := load(t, cwd, newHost(false))
	srv := modelServer(t, []string{"rm -rf /x", "echo sk-secret1"})
	ag := agent.New(srv, "medium", cwd)
	ag.Extensions = m
	var notices []string
	err := ag.Run(context.Background(), "please deploy", func(ev any) {
		if n, ok := ev.(agent.HookNotice); ok {
			notices = append(notices, n.Event+": "+n.Message)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	sent := lastRequest(t, ag)
	for _, want := range []string{"Deploys need a ticket.", "Blocked by an extension: fixture: too dangerous", "[redacted]"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the model got %q: %s", want, sent)
		}
	}
	if strings.Count(sent, "sk-secret1") != 1 { // in the command the model wrote, not in its output
		t.Error("the secret reached the model")
	}
	if !slices.Contains(notices, "extension tool_call: fixture: too dangerous") {
		t.Errorf("%q", notices)
	}
}

func TestCommandsAndUI(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "ui.ts"), `
export default function (atto: any) {
  atto.registerCommand("ask", {
    description: "Ask things",
    handler: async (args: string, ctx: any) => {
      const pick = await ctx.ui.select("Pick", ["a", "b"]);
      const ok = await ctx.ui.confirm("Sure?");
      const text = await ctx.ui.input("Name?");
      ctx.ui.notify(args + ":" + pick + ":" + ok + ":" + text + ":" + ctx.hasUI, "warning");
      ctx.ui.setStatus("state", "ready");
      ctx.ui.setWidget("w", ["line 1", "line 2"]);
    },
  });
  atto.registerCommand("fail", { handler: () => { throw new Error("nope") } });
}
`)
	h := newHost(true)
	h.answers = []any{"b", true, "Ann"}
	m := load(t, cwd, h)
	if got := m.Commands(); len(got) != 4 || got[0] != (Command{"ask", "Ask things", "ui"}) || got[3].Ext != "diff" {
		t.Fatalf("%+v", got)
	}
	if m.RunCommand("nope", "") {
		t.Fatal("no such command")
	}
	if !m.RunCommand("ask", "x") {
		t.Fatal("ask is a command")
	}
	eventually(t, "the command", func() bool { return len(h.snapshot().widgets) == 1 })
	s := h.snapshot()
	if !slices.Contains(s.notices, "warning ui: x:b:true:Ann:true") {
		t.Fatalf("%q", s.notices)
	}
	if len(s.asked) != 3 || s.asked[0].Kind != "select" || !slices.Equal(s.asked[0].Options, []string{"a", "b"}) || s.asked[2].Title != "Name?" {
		t.Fatalf("%+v", s.asked)
	}
	if s.status["ui/state"] != "ready" || !slices.Equal(s.widgets["ui/w"], []string{"line 1", "line 2"}) {
		t.Fatalf("%+v %+v", s.status, s.widgets)
	}
	m.RunCommand("fail", "")
	eventually(t, "the error", func() bool {
		return slices.ContainsFunc(h.snapshot().notices, func(n string) bool { return strings.Contains(n, "/fail: Error: nope") })
	})
}

// Without a UI, dialogs give their defaults at once.
func TestHeadlessUI(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "ui.ts"), `
export default function (atto: any) {
  atto.on("user_prompt", async (_e: any, ctx: any) => {
    const r = [await ctx.ui.select("Pick", ["a"]), await ctx.ui.confirm("Sure?"), await ctx.ui.input("Name?"), ctx.hasUI];
    ctx.ui.setStatus("k", "v");
    ctx.ui.notify("hi");
    return JSON.stringify(r);
  });
}
`)
	var out strings.Builder
	m := load(t, cwd, &Headless{Out: &out})
	if o := m.UserPrompt(context.Background(), "x"); o.Context != "[null,false,null,false]" {
		t.Fatalf("%+v", o)
	}
	if out.String() != "[ui] hi\n" {
		t.Fatalf("%q", out.String())
	}
}

func TestExecFSAndMessages(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(cwd, "sub", "f.txt"), "héllo")
	write(t, filepath.Join(dir, "tools.ts"), `
export default function (atto: any) {
  atto.registerCommand("go", { handler: async () => {
    const r = await atto.exec("echo hi");
    const bad = await atto.exec("exit 3");
    const sub = await atto.exec(atto.session.id === "s1" ? "echo ok" : "echo wrong", { cwd: "sub" });
    const slow = await atto.exec("sleep 5", { timeout: 100 });
    const fs = atto.fs;
    fs.writeFile("new/dir/out.txt", fs.readFile("sub/f.txt") + "!");
    let err = "";
    try { fs.readFile("missing.txt"); } catch (e) { err = String(e); }
    atto.sendMessage(JSON.stringify({
      out: r.stdout.trim(), code: r.code, bad: bad.code, sub: sub.stdout.trim(), killed: slow.killed,
      exists: [fs.exists("new/dir/out.txt"), fs.exists("nope")], list: fs.list("."), err: err !== "",
      session: [atto.session.cwd === atto.cwd, atto.name],
    }));
  } });
}
`)
	h := newHost(true)
	m := load(t, cwd, h)
	m.SetSession("s1")
	m.RunCommand("go", "")
	eventually(t, "the message", func() bool { return len(h.snapshot().sent) == 1 })
	var got struct {
		Out, Sub      string
		Code, Bad     int
		Killed, Err   bool
		Exists        []bool
		List, Session []any
	}
	if err := json.Unmarshal([]byte(h.snapshot().sent[0]), &got); err != nil {
		t.Fatal(err, h.snapshot().sent, h.snapshot().notices)
	}
	if got.Out != "hi" || got.Code != 0 || got.Bad != 3 || got.Sub != "ok" || !got.Killed || !got.Err {
		t.Fatalf("%+v", got)
	}
	if !slices.Equal(got.Exists, []bool{true, false}) || fmt.Sprint(got.List) != "[.git/ new/ sub/]" || fmt.Sprint(got.Session) != "[true tools]" {
		t.Fatalf("%+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(cwd, "new", "dir", "out.txt")); string(b) != "héllo!" {
		t.Fatalf("%q", b)
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Seen", r.Method+" "+r.Header.Get("X-Key")+" "+string(body))
		if r.URL.Path == "/missing" {
			w.WriteHeader(404)
		}
		fmt.Fprint(w, `{"n": 7}`)
	}))
	defer srv.Close()
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "net.ts"), `
export default function (atto: any) {
  atto.on("user_prompt", async (e: any) => {
    const r = await fetch(e.prompt, { method: "post", headers: { "X-Key": "k" }, body: "hi" });
    const j = await r.json();
    const miss = await atto.fetch(e.prompt + "/missing");
    let err = "";
    try { await fetch("http://127.0.0.1:1/"); } catch (x) { err = "rejected"; }
    return [r.status, r.ok, r.headers["x-seen"], j.n, miss.status, miss.ok, await miss.text(), err].join("|");
  });
}
`)
	m := load(t, cwd, newHost(false))
	o := m.UserPrompt(context.Background(), srv.URL)
	if want := `200|true|POST k hi|7|404|false|{"n": 7}|rejected`; o.Context != want {
		t.Fatalf("got %q (%q), want %q", o.Context, o.Notices, want)
	}
}

func TestTimers(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "timers.ts"), `
export default function (atto: any) {
  let n = 0;
  const id = setInterval(() => { n++; if (n === 3) { clearInterval(id); atto.sendMessage("interval " + n); } }, 5);
  const never = setTimeout(() => atto.sendMessage("cleared timer ran"), 20);
  clearTimeout(never);
  setTimeout((a: string, b: string) => atto.sendMessage("timeout " + a + b), 10, "x", "y");
}
`)
	h := newHost(false)
	load(t, cwd, h)
	eventually(t, "the timers", func() bool { return len(h.snapshot().sent) == 2 })
	time.Sleep(50 * time.Millisecond)
	if s := h.snapshot().sent; !slices.Contains(s, "interval 3") || !slices.Contains(s, "timeout xy") || len(s) != 2 {
		t.Fatalf("%q", s)
	}
}

func TestHandlerFailures(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "flaky.ts"), `
export default function (atto: any) {
  atto.on("tool_call", (e: any) => {
    if (e.command === "throw") throw new Error("bad handler");
    if (e.command === "hang") return new Promise(() => {});
    if (e.command === "spin") { while (true) {} }
  });
}
`)
	write(t, filepath.Join(dir, "steady.ts"), `
export default function (atto: any) {
  atto.on("tool_call", (e: any) => ({ command: e.command + " # seen" }));
}
`)
	h := newHost(false)
	m := load(t, cwd, h)
	ctx := context.Background()

	args, o := m.ToolCall(ctx, agent.BashArgs{Command: "throw"})
	if args.Command != "throw # seen" || len(o.Notices) != 2 || !strings.Contains(o.Notices[0], "flaky: tool_call handler: Error: bad handler") ||
		!strings.Contains(o.Notices[0], "flaky.ts:4") {
		t.Fatalf("a throw is reported (where, in the source) and skipped: %q %+v", args.Command, o)
	}
	start := time.Now()
	args, o = m.ToolCall(ctx, agent.BashArgs{Command: "hang"})
	if args.Command != "hang # seen" || !strings.Contains(o.Notices[0], "no answer within 1s") || time.Since(start) > 3*time.Second {
		t.Fatalf("a handler that never answers is given up on: %+v", o)
	}
	if info(t, m, "flaky").Status != Loaded {
		t.Fatal("still loaded after a timeout")
	}
	args, _ = m.ToolCall(ctx, agent.BashArgs{Command: "spin"})
	if args.Command != "spin # seen" {
		t.Fatalf("%q", args.Command)
	}
	in := info(t, m, "flaky")
	if in.Status != Failed || !strings.Contains(in.Error, "interrupted") {
		t.Fatalf("a runaway script is interrupted and disabled: %+v", in)
	}
	// The status changes before the notice is sent.
	eventually(t, "the disabled notice", func() bool {
		return slices.ContainsFunc(h.snapshot().notices, func(n string) bool { return strings.HasPrefix(n, "error flaky: extension disabled") })
	})
	if args, _ = m.ToolCall(ctx, agent.BashArgs{Command: "x"}); args.Command != "x # seen" {
		t.Fatalf("the others go on: %q", args.Command)
	}
}

// A dialog the user takes long to answer does not count against the
// handler timeout.
func TestDialogPausesTimeout(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "ask.ts"), `
export default function (atto: any) {
  atto.on("tool_call", async (e: any, ctx: any) => (await ctx.ui.confirm("Run " + e.command + "?")) ? undefined : { block: true, reason: "declined" });
}
`)
	h := &slowHost{fakeHost: newHost(true), delay: 1500 * time.Millisecond}
	m := load(t, cwd, h)
	_, o := m.ToolCall(context.Background(), agent.BashArgs{Command: "make"})
	if !o.Block || o.Reason != "ask: declined" {
		t.Fatalf("%+v", o)
	}
}

type slowHost struct {
	*fakeHost
	delay time.Duration
}

func (h *slowHost) Ask(ext string, q Question, answer func(any)) {
	go func() {
		time.Sleep(h.delay)
		answer(false)
	}()
}

func TestReload(t *testing.T) {
	dir, cwd := env(t)
	path := filepath.Join(dir, "w.ts")
	write(t, path, `
export default function (atto: any) {
  atto.ui.setWidget("w", ["v1"]);
  atto.ui.setStatus("s", "on");
  setInterval(() => {}, 5);
  atto.onDispose(async () => { await null; atto.fs.writeFile("disposed.txt", "v1"); });
}
`)
	write(t, filepath.Join(dir, "same.ts"), "export default () => {}")
	h := newHost(true)
	m := load(t, cwd, h)
	before := info(t, m, "w")
	if s := h.snapshot(); !slices.Equal(s.widgets["w/w"], []string{"v1"}) || s.status["w/s"] != "on" {
		t.Fatalf("%+v", s)
	}
	write(t, path, `export default function (atto: any) { atto.registerCommand("v2", { handler() {} }); }`)
	m.Reload()
	if b, _ := os.ReadFile(filepath.Join(cwd, "disposed.txt")); string(b) != "v1" {
		t.Fatal("the old runtime's onDispose ran")
	}
	s := h.snapshot()
	if len(s.widgets) != 0 || len(s.status) != 0 || !slices.Contains(s.cleared, "w") {
		t.Fatalf("its widgets and status went: %+v", s)
	}
	after := info(t, m, "w")
	if after.Hash == before.Hash || !slices.Equal(after.Commands, []string{"v2"}) {
		t.Fatalf("the new code runs: %+v %+v", before, after)
	}
	if info(t, m, "same").Hash == "" {
		t.Fatal("hash")
	}
}

// The extension goroutines end with the manager.
func TestCloseStopsGoroutines(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "a.ts"), "export default () => { setInterval(() => {}, 1) }")
	before := runtime.NumGoroutine()
	m := Load(Options{Cwd: cwd})
	m.Close()
	eventually(t, "goroutines to end", func() bool { return runtime.NumGoroutine() <= before })
}

// The examples in the guide load.
func TestGuideExamples(t *testing.T) {
	dir, cwd := env(t)
	guide, err := os.ReadFile(filepath.Join("..", "docs", "extensions.md"))
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout may have CRLF line ends.
	parts := strings.Split(strings.ReplaceAll(string(guide), "\r\n", "\n"), "```ts\n")[1:]
	if len(parts) < 3 {
		t.Fatalf("%d examples", len(parts))
	}
	for i, p := range parts {
		code, _, _ := strings.Cut(p, "```")
		write(t, filepath.Join(dir, fmt.Sprintf("example%d.ts", i)), code)
	}
	m := load(t, cwd, newHost(true))
	for i := range parts {
		if in := info(t, m, fmt.Sprintf("example%d", i)); in.Status != Loaded {
			t.Errorf("%+v", in)
		}
	}
}

// The extensions in examples/ load.
func TestRepoExamples(t *testing.T) {
	dir, cwd := env(t)
	files, _ := filepath.Glob(filepath.Join("..", "examples", "extensions", "*.ts"))
	if len(files) == 0 {
		t.Fatal("no examples")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, filepath.Base(f)), string(b))
	}
	m := load(t, cwd, newHost(true))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".ts")
		if in := info(t, m, name); in.Status != Loaded {
			t.Errorf("%s: %+v", name, in)
		}
	}
}

// modelServer is a fake model that runs each command, one per step, then
// answers "done".
func modelServer(t *testing.T, commands []string) config.ModelRef {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		chunk := `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`
		if i < len(commands) {
			args, _ := json.Marshal(map[string]string{"description": "test", "command": commands[i]})
			a, _ := json.Marshal(string(args))
			chunk = fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c%d","type":"function","function":{"name":"bash","arguments":%s}}]},"finish_reason":"tool_calls"}]}`, i, a)
		}
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	t.Cleanup(srv.Close)
	return config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: srv.URL}, Model: config.Model{ID: "m"}}
}

// lastRequest is the last request the agent sent, as text.
func lastRequest(t *testing.T, ag *agent.Agent) string {
	t.Helper()
	return string(ag.LastRequest())
}

func TestExtensionDecisionAndRevocation(t *testing.T) {
	_, cwd := env(t)
	path := filepath.Join(cwd, ".atto", "extensions", "guard.ts")
	write(t, path, "export default () => {}")
	s := Spec{Name: "guard", Path: path, Source: Project}
	code, err := Bundle(path)
	if err != nil {
		t.Fatal(err)
	}
	h := hash(code)
	if ApprovalOf(s, h) != mcp.Pending {
		t.Fatal("new project extension is not pending")
	}
	if err := SetApproval(s, h, false); err != nil {
		t.Fatal(err)
	}
	if ApprovalOf(s, h) != mcp.Denied || approved(path, code) {
		t.Fatal("denied extension may run")
	}
	if err := SetApproval(s, h, true); err != nil {
		t.Fatal(err)
	}
	if ApprovalOf(s, h) != mcp.Approved || !approved(path, code) {
		t.Fatal("approved extension cannot run")
	}
	write(t, path, "export default (atto: any) => atto.log('changed')")
	changed, err := Bundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if ApprovalOf(s, hash(changed)) != mcp.Pending {
		t.Fatal("approval survived a code change")
	}
	if err := Revoke(s); err != nil {
		t.Fatal(err)
	}
	if ApprovalOf(s, h) != mcp.Pending {
		t.Fatal("approval survived revocation")
	}
}
