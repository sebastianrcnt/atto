package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/subagent"
	"github.com/sebastianrcnt/atto/tui"
)

func treeIDs(items []centerItem) []string {
	var ids []string
	for _, it := range items {
		ids = append(ids, it.id)
	}
	return ids
}

func smallCenter() *agentCenter {
	c := &agentCenter{flat: true, onClose: func() {}}
	c.apply(centerSnapshot{
		saved: []session.Summary{
			{ID: "root", Name: "Fix API", Cwd: "/work", LastMessage: "Started the tests."},
			{ID: "tests", Name: "tests", AgentOf: "root", Cwd: "/trees/tests", Branch: "atto/tests", Preview: "Run the suite\nThen review failures", LastMessage: "All tests pass."},
			{ID: "lint", Name: "lint", AgentOf: "tests", Cwd: "/trees/tests"},
			{ID: "docs", Name: "docs", AgentOf: "root", Cwd: "/work"},
			{ID: "shell", External: true, Cwd: "/shell"},
			{ID: "check", Name: "check", AgentOf: "shell", Cwd: "/shell"},
			{ID: "orphan", Name: "orphan", AgentOf: "gone", Cwd: "/other"},
		},
		agents: []centerAgent{
			{state: subagent.State{Session: "tests", Parent: "root", Name: "tests", Preset: "tester", Model: "fake/fast", Branch: "atto/tests", Task: "Run the suite\nThen review failures"}, turn: subagent.Turn{Status: subagent.Running, PromptTokens: 120, CachedTokens: 20, OutputTokens: 30, Started: time.Unix(1, 0), Ended: time.Unix(3, 0)}},
			{state: subagent.State{Session: "lint", Parent: "tests", Name: "lint", Preset: "review", Model: "fake/fast", Task: "Lint all packages"}, turn: subagent.Turn{Status: subagent.Idle}},
			{state: subagent.State{Session: "docs", Parent: "root", Name: "docs", Preset: "writer", Model: "fake/fast", Task: "Update README"}, turn: subagent.Turn{Status: subagent.Done}},
			{state: subagent.State{Session: "check", Parent: "shell", Name: "check", Preset: "general", Model: "fake/fast", Task: "Check shell agents"}, turn: subagent.Turn{Status: subagent.Failed}},
		},
	})
	return c
}

func TestCenterTree(t *testing.T) {
	c := smallCenter()
	tree := c.shown()
	want := []string{"root", "tests", "lint", "docs", "shell", "check", "orphan"}
	if got := treeIDs(tree); !reflect.DeepEqual(got, want) {
		t.Fatalf("tree %v", got)
	}
	for i, want := range []struct {
		depth                 int
		prefix, path, project string
	}{
		{0, "", "", "/work"},
		{1, "├─ ", "/root/tests", "/work"},
		{2, "│  └─ ", "/root/tests/lint", "/work"},
		{1, "└─ ", "/root/docs", "/work"},
		{0, "", "", "/shell"},
		{1, "└─ ", "/root/check", "/shell"},
		{0, "", "/root/orphan", "/other"},
	} {
		it := tree[i]
		if it.depth != want.depth || it.prefix != want.prefix || it.agentPath != want.path || it.project != want.project {
			t.Fatalf("row %d: %+v", i, it)
		}
	}
	if tree[1].cwd != "/trees/tests" {
		t.Fatal("worktree cwd lost")
	}
	// Corrupt parent cycles must not hide rows or recurse forever.
	cycle := centerTree([]centerItem{{id: "a", parent: "b"}, {id: "b", parent: "a"}, {id: "self", parent: "self"}})
	if len(cycle) != 3 {
		t.Fatalf("cycle %v", treeIDs(cycle))
	}
}

func TestCenterFoldAndFilter(t *testing.T) {
	c := smallCenter()
	c.HandleInput(" ")
	if got := treeIDs(c.shown()); !reflect.DeepEqual(got, []string{"root", "shell", "check", "orphan"}) {
		t.Fatalf("fold %v", got)
	}
	// Refresh keeps folds and selection, and tabs reveal working descendants.
	c.apply(centerSnapshot{saved: []session.Summary{{ID: "root", Cwd: "/work"}, {ID: "tests", Name: "tests", AgentOf: "root", Cwd: "/work"}}, agents: []centerAgent{{state: subagent.State{Session: "tests", Parent: "root", Name: "tests", Task: "Run suite"}, turn: subagent.Turn{Status: subagent.Running}}}})
	if len(c.shown()) != 1 || c.sel != 0 {
		t.Fatal("refresh lost fold/selection")
	}
	c.tab = tabWorking
	if got := treeIDs(c.shown()); !reflect.DeepEqual(got, []string{"root", "tests"}) {
		t.Fatalf("working %v", got)
	}
	c = smallCenter()
	c.HandleInput(" ")
	for _, q := range []string{"/root/tests/lint", "Lint all packages", "lint", "review"} {
		c.search = q
		if got := treeIDs(c.shown()); !reflect.DeepEqual(got, []string{"root", "tests", "lint"}) {
			t.Fatalf("search %q: %v", q, got)
		}
	}
	c.search = ""
	c.HandleInput(" ")
	if len(c.shown()) != 7 {
		t.Fatal("space did not expand")
	}
	closed, opened := false, ""
	c.onClose = func() { closed = true }
	c.onOpen = func(id, cwd string) { opened = id }
	c.HandleInput("\x1b[C")
	if !closed || opened != "root" {
		t.Fatal("right must still open, not fold")
	}
	closed = false
	c.HandleInput("\x1b[D")
	if !closed {
		t.Fatal("left must still go back")
	}
}

func TestCenterAgentSnapshot(t *testing.T) {
	c := smallCenter()
	c.sel = 1
	rows := c.RenderScreen(180, 24)
	for i, row := range rows {
		rows[i] = strings.TrimRight(tui.StripEscapes(row), " ")
	}
	got := strings.Join(rows, "\n") + "\n"
	path := filepath.Join("testdata", "agent_center.txt")
	if os.Getenv("UPDATE_CENTER_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("center snapshot differs:\n%s", got)
	}
	for _, size := range [][2]int{{45, 20}, {100, 24}} {
		for _, row := range c.RenderScreen(size[0], size[1]) {
			if tui.VisibleWidth(row) > size[0] {
				t.Fatalf("oversized row: %q", row)
			}
		}
	}
}

func TestCenterOpensAgentTranscript(t *testing.T) {
	for _, locked := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "running"}[locked], func(t *testing.T) {
			a, _ := paneApp(t, false)
			w := session.NewSubagent(a.cwd, a.sess.ID)
			w.Append(session.Entry{Type: session.TypeName, Name: "tests"})
			w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "Run tests"}})
			w.Close()
			var release func()
			if locked {
				var err error
				release, err = session.LockKind(w.Path, session.KindRun)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { release() }()
			}
			fakeCenter(t, nil, []session.Summary{{ID: w.ID, Name: "tests", Cwd: a.cwd, AgentOf: a.sess.ID, Preview: "Run tests"}})
			a.cmdAgents("")
			waitCenter(t, a)
			c := a.modal.(*agentCenter)
			for i, it := range c.shown() {
				if it.id == w.ID {
					c.sel = i
				}
			}
			c.HandleInput("\r")
			if a.sess.ID != w.ID {
				t.Fatalf("opened %s, want %s", a.sess.ID, w.ID)
			}
			if got := a.sess.ReadOnly() != ""; got != locked {
				t.Fatalf("read-only %v, locked %v", got, locked)
			}
			if locked {
				banner := tui.StripEscapes(strings.Join(a.renderReadOnly(160), ""))
				if !strings.Contains(banner, "read-only") || !strings.Contains(banner, "ctrl+r") {
					t.Fatalf("banner %q", banner)
				}
				release()
				a.onInput("\x12")
				if a.sess.ReadOnly() != "" {
					t.Fatal("refresh did not open unlocked agent normally")
				}
			}
		})
	}
}

func TestCenterDaemonOpensAgent(t *testing.T) {
	a, rec := paneApp(t, true)
	fakeCenter(t, nil, []session.Summary{{ID: "agent", Name: "tests", AgentOf: a.sess.ID, Cwd: "/trees/tests"}})
	a.cmdAgents("")
	waitCenter(t, a)
	c := a.modal.(*agentCenter)
	for i, it := range c.shown() {
		if it.id == "agent" {
			c.sel = i
		}
	}
	rec.take()
	c.HandleInput("\r")
	if got := rec.take(); !strings.Contains(got, daemon.MarkerSeq("open", "agent", "/trees/tests")) {
		t.Fatalf("marker %q", got)
	}
}

func TestCenterStateStatusVocabulary(t *testing.T) {
	for _, test := range []struct {
		status subagent.Status
		tab    int
	}{
		{subagent.Idle, tabReady}, {subagent.Queued, tabWorking}, {subagent.Running, tabWorking},
		{subagent.Done, tabReady}, {subagent.Failed, tabInactive}, {subagent.Stopped, tabInactive},
	} {
		c := &agentCenter{}
		c.apply(centerSnapshot{agents: []centerAgent{{state: subagent.State{Session: "child", Parent: "gone", Name: "child"}, turn: subagent.Turn{Status: test.status}}}})
		if c.items[0].tab != test.tab {
			t.Fatalf("%s mapped to %s", test.status, c.items[0].status())
		}
	}
	// A live pane's question overrides agent turn state.
	c := &agentCenter{}
	c.apply(centerSnapshot{panes: []daemon.Pane{{Session: "child", State: "waiting"}}, agents: []centerAgent{{state: subagent.State{Session: "child", Parent: "gone", Name: "child"}, turn: subagent.Turn{Status: subagent.Running}}}})
	if c.items[0].tab != tabNeedsYou {
		t.Fatal("pane waiting state lost")
	}
}

func TestCenterScanIncludesShellNestedAndClosedAgents(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	oldP := listPanes
	listPanes = func() ([]daemon.Pane, error) { return nil, nil }
	t.Cleanup(func() { listPanes = oldP })
	root := session.NewExternal("/project")
	root.Append(session.Entry{Type: session.TypeName, Name: "atto agent (external)"})
	root.Close()
	child := session.NewSubagent("/trees/tests", root.ID)
	child.Append(session.Entry{Type: session.TypeName, Name: "tests"})
	child.Append(session.Entry{Type: session.TypeModel, Provider: "fake", Model: "fast"})
	child.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "Run suite"}})
	child.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "Suite passes"}})
	child.Close()
	nested := session.NewSubagent("/trees/tests", child.ID)
	nested.Append(session.Entry{Type: session.TypeName, Name: "lint"})
	nested.Close()
	for _, st := range []subagent.State{
		{Parent: root.ID, Session: child.ID, Name: "tests", Cwd: "/trees/tests", Task: "Run suite", Preset: "tester", Model: "fake/fast"},
		{Parent: child.ID, Session: nested.ID, Name: "lint", Cwd: "/trees/tests", Task: "Lint suite", Preset: "review", Model: "fake/fast"},
	} {
		if err := subagent.Save(st); err != nil {
			t.Fatal(err)
		}
	}
	c := &agentCenter{}
	c.reload()
	sh := c.shown()
	if got := treeIDs(sh); !reflect.DeepEqual(got, []string{root.ID, child.ID, nested.ID}) {
		t.Fatalf("inventory %v", got)
	}
	if sh[0].title != "agents started from a shell" || sh[2].agentPath != "/root/tests/lint" || sh[1].tab != tabReady || c.lastMessage(child.ID) != "Suite passes" {
		t.Fatalf("rows %+v", sh)
	}
	// Real agent close archives descendants and removes their state.
	for _, w := range []*session.Writer{nested, child, root} {
		if _, err := session.Archive(w.Path); err != nil {
			t.Fatal(err)
		}
	}
	subagent.Remove(child.ID, "lint")
	subagent.Remove(root.ID, "tests")
	c.reload()
	sh = c.shown()
	if got := treeIDs(sh); !reflect.DeepEqual(got, []string{root.ID, child.ID, nested.ID}) {
		t.Fatalf("archived inventory %v", got)
	}
	if sh[1].tab != tabInactive || sh[1].model != "fake/fast" || sh[2].agentPath != "/root/tests/lint" {
		t.Fatalf("closed rows %+v", sh)
	}
	// A truly deleted parent leaves agents at top level, not hidden.
	rootPath, err := session.Find(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(rootPath); err != nil {
		t.Fatal(err)
	}
	c.reload()
	sh = c.shown()
	if len(sh) != 2 || sh[0].id != child.ID || sh[0].depth != 0 || sh[1].depth != 1 {
		t.Fatalf("orphan rows %+v", sh)
	}
}

func TestCenterEmptyParentStillAnchorsAgent(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	oldP := listPanes
	listPanes = func() ([]daemon.Pane, error) { return nil, nil }
	t.Cleanup(func() { listPanes = oldP })
	parent := session.New("/work")
	parent.Append(session.Entry{Type: session.TypeName, Name: "empty parent"})
	parent.Close()
	child := session.NewSubagent("/work", parent.ID)
	child.Append(session.Entry{Type: session.TypeName, Name: "tests"})
	child.Close()
	c := &agentCenter{}
	c.reload()
	sh := c.shown()
	if len(sh) != 2 || sh[0].id != parent.ID || sh[1].depth != 1 {
		t.Fatalf("empty parent tree %+v", sh)
	}
}

func TestCenterPaneIdentityAndAgentViewerState(t *testing.T) {
	c := &agentCenter{}
	c.apply(centerSnapshot{panes: []daemon.Pane{
		{ID: 1, Session: "agent", State: "idle"},
		{ID: 2, Session: "agent", State: "working"},
		{ID: 3}, {ID: 4},
	}, agents: []centerAgent{{state: subagent.State{Session: "agent", Parent: "gone", Name: "tests"}, turn: subagent.Turn{Status: subagent.Running}}}})
	sh := c.shown()
	if len(sh) != 3 || len(c.items) != 3 {
		t.Fatalf("duplicate or missing panes: %+v", sh)
	}
	for _, it := range sh {
		if it.id == "agent" && (it.tab != tabWorking || it.pane.ID != 2) {
			t.Fatalf("wrong agent pane %+v", it)
		}
	}
	// A single idle viewer pane must not downgrade a headless running turn.
	c.apply(centerSnapshot{panes: []daemon.Pane{{ID: 1, Session: "agent", State: "idle"}}, agents: []centerAgent{{state: subagent.State{Session: "agent", Parent: "gone", Name: "tests"}, turn: subagent.Turn{Status: subagent.Running}}}})
	if c.items[0].tab != tabWorking {
		t.Fatal("idle viewer hid working agent")
	}
}

func TestCenterCtrlCClosesWithoutInterruptingSession(t *testing.T) {
	for _, typing := range []bool{false, true} {
		t.Run(map[bool]string{false: "list", true: "search"}[typing], func(t *testing.T) {
			a, _ := paneApp(t, false)
			fakeCenter(t, nil, nil)
			turnCanceled, shellCanceled := false, false
			a.busy = true
			a.cancel = func() { turnCanceled = true }
			runningShell := &shellRun{cancel: func() { shellCanceled = true }}
			a.shell = runningShell
			a.editor.SetText("keep my draft")
			before := a.sess.ID
			a.cmdAgents("")
			waitCenter(t, a)
			c := a.modal.(*agentCenter)
			c.typing, c.search = typing, "find agents"
			if a.onInput("\x03") {
				t.Fatal("session handled a modal's Ctrl+C")
			}
			c.HandleInput("\x03")
			if a.modal != nil || a.ui.Screen != nil {
				t.Fatal("Ctrl+C did not return to the transcript")
			}
			if turnCanceled || shellCanceled || !a.busy || a.shell != runningShell {
				t.Fatal("center interrupted underlying work")
			}
			if a.sess.ID != before || a.editor.Text() != "keep my draft" || quitting(a) {
				t.Fatal("center changed or quit the session")
			}
			a.busy = false
			a.shell = nil
		})
	}
}
