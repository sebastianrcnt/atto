package agentmigrate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// legacy is the old layout, built by hand.
type legacy struct {
	t   *testing.T
	ids map[string]string
}

func (l *legacy) state(parent, name, id string, extra string) {
	l.t.Helper()
	write(l.t, filepath.Join(config.AgentStateDir(), parent, name+".json"),
		`{"name":"`+name+`","parent":"`+parent+`","session":"`+id+`","preset":"general","model":"fake/m","cwd":"/w/`+name+`","task":"do `+name+`","created":"2026-01-01T00:00:0`+string(rune('0'+len(l.ids)%10))+`Z","turns":1,"prompt":"do `+name+`","job":1`+extra+`}`)
	write(l.t, filepath.Join(config.AgentStateDir(), parent, name+".turn.json"), `{"turn":1,"status":"done","queued":"2026-01-01T00:00:00Z","started":"2026-01-01T00:00:01Z","ended":"2026-01-01T00:00:02Z"}`)
	write(l.t, filepath.Join(config.AgentStateDir(), "_up", id+".json"), `{"parent":"`+parent+`","name":"`+name+`"}`)
	// The turn was a job of the parent session.
	write(l.t, filepath.Join(config.Dir(), "jobs", parent, "1", "job.json"), `{"kind":"agent","id":1,"session":"`+parent+`","name":"agent `+name+`","status":"exited","exitCode":0,"started":"2026-01-01T00:00:00Z","ended":"2026-01-01T00:00:02Z","quiet":true}`)
	write(l.t, filepath.Join(config.Dir(), "jobs", parent, "1", "output.log"), "log of "+name+"\n")
	l.ids[name] = id
}

// agentSession writes the session of a legacy agent: a header with agentOf only.
func (l *legacy) agentSession(parent, id, name string) string {
	l.t.Helper()
	w := session.NewManagedID(id, "/w/"+name, func(string) session.AgentMeta {
		p := parent
		return session.AgentMeta{Version: 1, ParentSessionID: &p, RootSessionID: parent, Depth: 1, Path: "/root/" + name, Name: name}
	})
	w.Append(session.Entry{Type: session.TypeName, Name: name})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "do " + name}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "answer of " + name}})
	w.Close()
	if err := session.RewriteHeader(w.Path, func(h *session.Entry) { h.Agent = nil }); err != nil {
		l.t.Fatal(err)
	}
	return w.Path
}

// fake writes a lightweight external parent, with n messages.
func (l *legacy) fake(id string, cwd string, messages int) string {
	l.t.Helper()
	w := session.NewExternal(cwd)
	renamed := session.Resume(w.Path, session.Entry{ID: w.ID})
	_ = renamed
	w.Append(session.Entry{Type: session.TypeName, Name: "atto agent (external)"})
	for range messages {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "hello"}})
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "hi"}})
	}
	w.Close()
	l.ids[id] = w.ID
	return w.ID
}

func newLegacy(t *testing.T) *legacy {
	t.Helper()
	t.Setenv(config.EnvDir, t.TempDir())
	return &legacy{t: t, ids: map[string]string{}}
}

func opts(out *bytes.Buffer) Options {
	return Options{Out: out, Version: "test", Project: filepath.Clean}
}

// buildAll makes every legacy shape: parent directories, nested agents, _up,
// _closed (under a real parent and under an external one), the subagents
// symlink, per-spawn and shared external parents, an external parent with
// real messages.
func buildAll(t *testing.T) *legacy {
	l := newLegacy(t)
	p := session.New("/work")
	p.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "hi"}})
	p.Close()
	l.ids["P"] = p.ID
	// A real parent with a nested agent.
	l.state(p.ID, "a", "aaaa0001", "")
	l.state("aaaa0001", "b", "bbbb0001", "")
	l.agentSession(p.ID, "aaaa0001", "a")
	l.agentSession("aaaa0001", "bbbb0001", "b")
	// Per-spawn external parents, each with one agent of the same name.
	f1 := l.fake("F1", "/proj", 0)
	f2 := l.fake("F2", "/proj", 0)
	l.state(f1, "panes", "xxxx0001", "")
	l.state(f2, "panes", "yyyy0001", "")
	l.agentSession(f1, "xxxx0001", "panes")
	l.agentSession(f2, "yyyy0001", "panes")
	// A shared (older) external parent with two agents and a project mapping.
	s := l.fake("S", "/shared", 0)
	l.state(s, "s1", "ssss0001", "")
	l.state(s, "s2", "ssss0002", "")
	l.agentSession(s, "ssss0001", "s1")
	l.agentSession(s, "ssss0002", "s2")
	write(t, filepath.Join(config.Dir(), "external_parents", "abc123"), s+"\n")
	// An external parent someone talked to: it stays an ordinary session.
	m := l.fake("M", "/talked", 1)
	l.state(m, "m1", "mmmm0001", "")
	l.agentSession(m, "mmmm0001", "m1")
	// Removed agents remember their tree and path.
	write(t, filepath.Join(config.AgentStateDir(), "_closed", "cccc0001.json"), `{"session":"cccc0001","root":"`+p.ID+`","path":"/root/gone"}`)
	write(t, filepath.Join(config.AgentStateDir(), "_closed", "dddd0001.json"), `{"session":"dddd0001","root":"`+f1+`","path":"/root/old"}`)
	write(t, filepath.Join(config.AgentStateDir(), "_closed", "eeee0001.json"), `{"session":"eeee0001","root":"`+f1+`","path":"/root/old/deep"}`)
	// The compatibility link of the rename that once moved subagents/.
	if err := os.Symlink("agent-state", filepath.Join(config.Dir(), "subagents")); err != nil {
		t.Skip("no symlinks:", err)
	}
	return l
}

func TestMigrateEveryLegacyShape(t *testing.T) {
	l := buildAll(t)
	if layout, _, err := agentstate.Detect(); err != nil || layout != agentstate.LayoutLegacy {
		t.Fatalf("detect: %v %v", layout, err)
	}
	var out bytes.Buffer
	r, err := Run(opts(&out))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if r.Agents != 7 || r.Roots != 5 || r.Closed != 3 || r.FakeDeleted != 3 || r.FakeKept != 1 {
		t.Fatalf("report %+v\n%s", r, out.String())
	}
	// The backup was taken first, inside ATTO_DIR/backups.
	if st, err := os.Stat(r.Backup); err != nil || st.Size() == 0 || filepath.Dir(r.Backup) != filepath.Join(config.Dir(), "backups") {
		t.Fatalf("backup %s %v", r.Backup, err)
	}
	// The marker is there, and the old layout is gone.
	m, ok, err := agentstate.ReadMarker()
	if err != nil || !ok || m.Version != agentstate.FormatVersion || m.Backup != r.Backup {
		t.Fatalf("marker %+v %v %v", m, ok, err)
	}
	ents, _ := os.ReadDir(config.AgentStateDir())
	for _, e := range ents {
		if e.IsDir() && e.Name() != ".coord" {
			t.Fatalf("old directory left: %s", e.Name())
		}
	}
	if _, err := os.Lstat(filepath.Join(config.Dir(), "subagents")); !os.IsNotExist(err) {
		t.Fatalf("subagents link left: %v", err)
	}
	P := l.ids["P"]

	// Children of a real parent keep it and gain a position.
	a, err := agentstate.Load("aaaa0001")
	if err != nil || a.Parent != P || a.Root != P || a.Depth != 1 || a.Path != "/root/a" || a.Origin != session.OriginAgent || a.JobOwner != P || a.Lifecycle != agentstate.Open || a.Latest().Status != agentstate.Done {
		t.Fatalf("a: %+v %v", a, err)
	}
	b, err := agentstate.Load("bbbb0001")
	if err != nil || b.Parent != "aaaa0001" || b.Root != P || b.Depth != 2 || b.Path != "/root/a/b" {
		t.Fatalf("b: %+v %v", b, err)
	}
	// Direct children of external parents are roots of their own, whatever the shape.
	for _, id := range []string{"xxxx0001", "yyyy0001", "ssss0001", "ssss0002", "mmmm0001"} {
		st, err := agentstate.Load(id)
		if err != nil || st.Parent != "" || st.Root != id || st.Depth != 0 || st.Path != "/root" || st.Origin != session.OriginExternal || st.Project == "" || st.JobOwner != id {
			t.Fatalf("%s: %+v %v", id, st, err)
		}
		// Its turn job moved with it: the status survives losing the parent.
		if got := st.Latest(); got.Status != agentstate.Done {
			t.Fatalf("%s turn: %+v", id, got)
		}
		if _, err := os.Stat(filepath.Join(config.Dir(), "jobs", id, "1", "output.log")); err != nil {
			t.Fatalf("%s job log: %v", id, err)
		}
		path, err := session.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		h, _, err := session.Load(path)
		if err != nil || h.Agent == nil || !h.Agent.IsRoot() || h.AgentOf != "" || h.Agent.RootSessionID != id || h.Agent.Project != st.Project {
			t.Fatalf("%s header: %+v %v", id, h, err)
		}
	}
	// Per-spawn parents gave two roots the same name; both are addressable by ID.
	if x, _ := agentstate.Load("xxxx0001"); x.Project != "/proj" {
		t.Fatalf("project: %+v", x)
	}
	if _, err := agentstate.ResolveOutside("/proj", "panes"); err == nil || !strings.Contains(err.Error(), "2 agents named panes") {
		t.Fatalf("duplicate roots: %v", err)
	}
	if tg, err := agentstate.ResolveOutside("/shared", "s1"); err != nil || tg.Session != "ssss0001" {
		t.Fatalf("shared parent's agent: %+v %v", tg, err)
	}
	// Headers of children carry the metadata and keep agentOf.
	path, _ := session.Find("bbbb0001")
	if h, _, err := session.Load(path); err != nil || h.Agent == nil || h.Agent.Parent() != "aaaa0001" || h.Agent.Depth != 2 || h.AgentOf != "aaaa0001" || h.Agent.Path != "/root/a/b" {
		t.Fatalf("b header: %+v %v", h, err)
	}
	// Transcripts are untouched.
	if got := session.LastAssistant("bbbb0001"); got != "answer of b" {
		t.Fatalf("transcript: %q", got)
	}
	// Fake parents without messages are deleted, the one with messages is an ordinary session.
	for _, name := range []string{"F1", "F2", "S"} {
		if _, err := session.Find(l.ids[name]); err == nil {
			t.Fatalf("empty external parent %s kept", name)
		}
	}
	mp, err := session.Find(l.ids["M"])
	if err != nil {
		t.Fatal(err)
	}
	if h, _, err := session.Load(mp); err != nil || h.External || h.IsAgent() {
		t.Fatalf("the talked-to parent: %+v %v", h, err)
	}
	if list, _ := session.List("", false); len(list) != 2 {
		t.Fatalf("ordinary sessions: %+v", list)
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), "external_parents")); !os.IsNotExist(err) {
		t.Fatal("project mappings left:", err)
	}
	// Closed agents are records now: their IDs resolve as closed, in their trees.
	c, err := agentstate.Load("cccc0001")
	if err != nil || c.Lifecycle != agentstate.Closed || c.Parent != P || c.Root != P || c.Depth != 1 || c.Name != "gone" {
		t.Fatalf("closed c: %+v %v", c, err)
	}
	d, err := agentstate.Load("dddd0001")
	if err != nil || d.Lifecycle != agentstate.Closed || d.Parent != "" || d.Root != "dddd0001" || d.Origin != session.OriginExternal {
		t.Fatalf("closed d (a child of an external parent): %+v %v", d, err)
	}
	e, err := agentstate.Load("eeee0001")
	if err != nil || e.Parent != "dddd0001" || e.Root != "dddd0001" || e.Depth != 1 || e.Path != "/root/deep" {
		t.Fatalf("closed e: %+v %v", e, err)
	}
	for _, id := range []string{"@cccc0001", "@dddd0001", "@eeee0001"} {
		if _, err := agentstate.Resolve("", id); !errors.Is(err, agentstate.ErrClosed) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	// Names are free again, and the live tree resolves as before.
	if tg, err := agentstate.Resolve(P, "a/b"); err != nil || tg.Session != "bbbb0001" {
		t.Fatalf("a/b: %+v %v", tg, err)
	}
	if _, err := agentstate.Ancestry("bbbb0001"); err != nil {
		t.Fatal(err)
	}
	if err := agentstate.Ready(); err != nil {
		t.Fatal(err)
	}

	// Running it again is safe and does nothing.
	out.Reset()
	again, err := Run(opts(&out))
	if err != nil || !again.AlreadyCurrent {
		t.Fatalf("second run: %+v %v", again, err)
	}
	if backups, _ := os.ReadDir(filepath.Join(config.Dir(), "backups")); len(backups) != 1 {
		t.Fatalf("a second run took another backup: %v", backups)
	}
}

func TestMigrateRefusesWhileAgentWorkRuns(t *testing.T) {
	l := newLegacy(t)
	l.state("pppp0001", "a", "aaaa0001", "")
	l.agentSession("pppp0001", "aaaa0001", "a")
	// An agent turn that is running: the turn file says so and its job is alive.
	write(t, filepath.Join(config.AgentStateDir(), "pppp0001", "a.turn.json"), `{"turn":1,"status":"running","started":"2026-01-01T00:00:01Z"}`)
	write(t, filepath.Join(config.Dir(), "jobs", "pppp0001", "1", "job.json"),
		`{"kind":"agent","id":1,"session":"pppp0001","status":"running","supervisorPid":`+itoa(os.Getpid())+`,"started":"2026-01-01T00:00:00Z"}`)
	// A job of an agent session counts too.
	write(t, filepath.Join(config.Dir(), "jobs", "aaaa0001", "7", "job.json"),
		`{"id":7,"session":"aaaa0001","name":"dev server","status":"running","supervisorPid":`+itoa(os.Getpid())+`,"started":"2026-01-01T00:00:00Z"}`)
	var out bytes.Buffer
	_, err := Run(opts(&out))
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "agent a (@aaaa0001): turn 1 running") ||
		!strings.Contains(err.Error(), "job pppp0001/1") || !strings.Contains(err.Error(), "job aaaa0001/7 (dev server)") {
		t.Fatalf("busy: %v", err)
	}
	// Nothing changed: no backup, no marker, the old layout intact.
	if _, ok, _ := agentstate.ReadMarker(); ok {
		t.Fatal("marker written")
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), "backups")); !os.IsNotExist(err) {
		t.Fatalf("backup taken while busy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.AgentStateDir(), "pppp0001", "a.json")); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// A failure after the backup stops before the marker, says how to go back,
// and a later run, once the cause is fixed, finishes the job.
func TestMigrateFailureLeavesNoMarkerAndRerunIsSafe(t *testing.T) {
	l := newLegacy(t)
	l.state("pppp0001", "a", "aaaa0001", "")
	path := l.agentSession("pppp0001", "aaaa0001", "a")
	l.state("aaaa0001", "b", "bbbb0001", "")
	l.agentSession("aaaa0001", "bbbb0001", "b")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// a's session file is not a session: its header cannot be rewritten.
	if err := os.WriteFile(path, []byte("not a session\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r, err := Run(opts(&out))
	if err == nil || !strings.Contains(err.Error(), "atto restore -force "+r.Backup) || !strings.Contains(err.Error(), "marker was not written") {
		t.Fatalf("failure: %v", err)
	}
	if _, ok, _ := agentstate.ReadMarker(); ok {
		t.Fatal("marker written after a failure")
	}
	if st, err := os.Stat(r.Backup); err != nil || st.Size() == 0 {
		t.Fatalf("backup %v", err)
	}
	// The old layout is still there.
	if _, err := os.Stat(filepath.Join(config.AgentStateDir(), "pppp0001", "a.json")); err != nil {
		t.Fatal("old layout removed after a failure:", err)
	}
	if layout, _, _ := agentstate.Detect(); layout != agentstate.LayoutLegacy {
		t.Fatalf("layout %v", layout)
	}
	if err := agentstate.Ready(); !errors.Is(err, agentstate.ErrNeedsMigration) {
		t.Fatalf("ready after failure: %v", err)
	}
	// Fix the cause, run again.
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	r2, err := Run(opts(&out))
	if err != nil || r2.Agents != 2 {
		t.Fatalf("rerun: %+v %v\n%s", r2, err, out.String())
	}
	if _, ok, _ := agentstate.ReadMarker(); !ok {
		t.Fatal("no marker after the rerun")
	}
	if b, err := agentstate.Load("bbbb0001"); err != nil || b.Root != "pppp0001" || b.Depth != 2 {
		t.Fatalf("b: %+v %v", b, err)
	}
}

func TestMigrateBadRecordStopsBeforeAnyChange(t *testing.T) {
	l := newLegacy(t)
	l.state("pppp0001", "a", "aaaa0001", "")
	write(t, filepath.Join(config.AgentStateDir(), "pppp0001", "broken.json"), "{")
	var out bytes.Buffer
	if _, err := Run(opts(&out)); err == nil || !strings.Contains(err.Error(), "broken.json") || !strings.Contains(err.Error(), "nothing changed") {
		t.Fatalf("bad record: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), "backups")); !os.IsNotExist(err) {
		t.Fatal("backup taken before the data was understood")
	}
}

func TestMigrateEmptyAndNewerData(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	var out bytes.Buffer
	r, err := Run(opts(&out))
	if err != nil || !r.AlreadyCurrent {
		t.Fatalf("empty: %+v %v", r, err)
	}
	if _, ok, _ := agentstate.ReadMarker(); !ok {
		t.Fatal("the marker of a layout nobody used was not written")
	}
	// A marker from a newer atto: refuse, change nothing.
	write(t, filepath.Join(config.AgentStateDir(), ".format"), `{"version":99}`)
	if _, err := Run(opts(&out)); !errors.Is(err, agentstate.ErrNewerFormat) {
		t.Fatalf("newer: %v", err)
	}
}

// An old binary may have written the old layout after the marker (on a machine
// that was not upgraded): migrating again converts it and keeps what is current.
func TestMigrateLeftoversAfterTheMarker(t *testing.T) {
	l := newLegacy(t)
	if err := agentstate.WriteMarker("t", ""); err != nil {
		t.Fatal(err)
	}
	current := agentstate.State{Name: "kept", Session: "kkkk0001", Parent: "pppp0001", Task: "current", Created: time.Now()}
	if err := agentstate.Save(current); err != nil {
		t.Fatal(err)
	}
	l.state("pppp0001", "kept", "kkkk0001", `,"task_":"stale"`)
	l.state("pppp0001", "late", "llll0001", "")
	l.agentSession("pppp0001", "llll0001", "late")
	var out bytes.Buffer
	if _, err := Run(opts(&out)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got, err := agentstate.Load("kkkk0001"); err != nil || got.Task != "current" {
		t.Fatalf("the record in the current format was replaced: %+v %v", got, err)
	}
	if got, err := agentstate.Load("llll0001"); err != nil || got.Root != "pppp0001" {
		t.Fatalf("leftover: %+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(config.AgentStateDir(), "pppp0001")); !os.IsNotExist(err) {
		t.Fatal("leftover directory kept")
	}
}
