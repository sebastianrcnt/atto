package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
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
			{ID: "check", Name: "check", Cwd: "/shell", Agent: &session.AgentMeta{Version: 1, RootSessionID: "check", Path: "/root", Origin: session.OriginExternal}},
			{ID: "orphan", Name: "orphan", AgentOf: "gone", Cwd: "/other"},
		},
		agents: []centerAgent{
			{state: agentstate.State{Session: "tests", Parent: "root", Name: "tests", Preset: "tester", Model: "fake/fast", Branch: "atto/tests", Task: "Run the suite\nThen review failures"}, turn: agentstate.Turn{Status: agentstate.Running, PromptTokens: 120, CachedTokens: 20, OutputTokens: 30, Started: time.Unix(1, 0), Ended: time.Unix(3, 0)}},
			{state: agentstate.State{Session: "lint", Parent: "tests", Name: "lint", Preset: "review", Model: "fake/fast", Task: "Lint all packages"}, turn: agentstate.Turn{Status: agentstate.Idle}},
			{state: agentstate.State{Session: "docs", Parent: "root", Name: "docs", Preset: "writer", Model: "fake/fast", Task: "Update README"}, turn: agentstate.Turn{Status: agentstate.Done}},
			{state: agentstate.State{Session: "check", Name: "check", Origin: session.OriginExternal, Project: "/shell", Cwd: "/shell", Preset: "general", Model: "fake/fast", Task: "Check shell agents"}, turn: agentstate.Turn{Status: agentstate.Failed}},
		},
	})
	return c
}

func TestCenterTree(t *testing.T) {
	c := smallCenter()
	tree := c.shown()
	want := []string{"root", "tests", "lint", "docs", "shell:/shell", "check", "orphan"}
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
	if got := treeIDs(c.shown()); !reflect.DeepEqual(got, []string{"root", "shell:/shell", "check", "orphan"}) {
		t.Fatalf("fold %v", got)
	}
	// Refresh keeps folds and selection, and tabs reveal working descendants.
	c.apply(centerSnapshot{saved: []session.Summary{{ID: "root", Cwd: "/work"}, {ID: "tests", Name: "tests", AgentOf: "root", Cwd: "/work"}}, agents: []centerAgent{{state: agentstate.State{Session: "tests", Parent: "root", Name: "tests", Task: "Run suite"}, turn: agentstate.Turn{Status: agentstate.Running}}}})
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
		t.Fatalf("space did not expand: %v", treeIDs(c.shown()))
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
			a, _ := recordedApp(t)
			w := session.NewAgent(a.cwd, a.threadID)
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
			fakeCenter(t, nil, []session.Summary{{ID: w.ID, Name: "tests", Cwd: a.cwd, AgentOf: a.threadID, Preview: "Run tests"}})
			a.cmdAgents("")
			waitCenter(t, a)
			c := a.modal.(*agentCenter)
			for i, it := range c.shown() {
				if it.id == w.ID {
					c.sel = i
				}
			}
			a.ui.Do(func() { c.HandleInput("\r") })
			within(t, a, "the agent's transcript", func() bool { return a.threadID == w.ID })
			settle(a)
			if got := a.readOnly != ""; got != locked {
				t.Fatalf("read-only %v, locked %v", got, locked)
			}
			if locked {
				banner := tui.StripEscapes(strings.Join(a.renderReadOnly(160), ""))
				if !strings.Contains(banner, "read-only") || !strings.Contains(banner, "ctrl+r") {
					t.Fatalf("banner %q", banner)
				}
				release()
				a.ui.Do(func() { a.onInput("\x12") })
				within(t, a, "the agent opened normally", func() bool { return a.readOnly == "" })
			}
		})
	}
}

func TestCenterStateStatusVocabulary(t *testing.T) {
	for _, test := range []struct {
		status agentstate.Status
		tab    int
	}{
		{agentstate.Idle, tabReady}, {agentstate.Queued, tabWorking}, {agentstate.Running, tabWorking},
		{agentstate.Done, tabReady}, {agentstate.Failed, tabInactive}, {agentstate.Stopped, tabInactive},
	} {
		c := &agentCenter{}
		c.apply(centerSnapshot{agents: []centerAgent{{state: agentstate.State{Session: "child", Parent: "gone", Name: "child"}, turn: agentstate.Turn{Status: test.status}}}})
		if c.items[0].tab != test.tab {
			t.Fatalf("%s mapped to %s", test.status, c.items[0].status())
		}
	}

}

func TestCenterScanIncludesShellNestedAndClosedAgents(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	oldP := listWorkers
	listWorkers = func() ([]daemon.Worker, error) { return nil, nil }
	t.Cleanup(func() { listWorkers = oldP })
	// An agent started from a shell is a root; one started below it is nested.
	root := session.NewManaged("/trees/tests", func(id string) session.AgentMeta {
		return session.AgentMeta{Version: 1, RootSessionID: id, Path: "/root", Name: "tests", Project: "/project", Origin: session.OriginExternal}
	})
	root.Append(session.Entry{Type: session.TypeName, Name: "tests"})
	root.Append(session.Entry{Type: session.TypeModel, Provider: "fake", Model: "fast"})
	root.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "Run suite"}})
	root.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "Suite passes"}})
	root.Close()
	nested := session.NewAgent("/trees/tests", root.ID)
	nested.Append(session.Entry{Type: session.TypeName, Name: "lint"})
	nested.Close()
	for _, st := range []agentstate.State{
		{Session: root.ID, Name: "tests", Cwd: "/trees/tests", Project: "/project", Origin: session.OriginExternal, Task: "Run suite", Preset: "tester", Model: "fake/fast", Created: time.Unix(1, 0)},
		{Parent: root.ID, Session: nested.ID, Name: "lint", Cwd: "/trees/tests", Task: "Lint suite", Preset: "review", Model: "fake/fast", Created: time.Unix(2, 0)},
	} {
		if err := agentstate.Save(st); err != nil {
			t.Fatal(err)
		}
	}
	c := &agentCenter{}
	c.reload()
	sh := c.shown()
	heading := "shell:/project"
	if got := treeIDs(sh); !reflect.DeepEqual(got, []string{heading, root.ID, nested.ID}) {
		t.Fatalf("inventory %v", got)
	}
	if sh[0].title != "agents started from a shell" || !sh[0].virtual || sh[2].agentPath != "/root/tests/lint" || sh[1].tab != tabReady || c.lastMessage(root.ID) != "Suite passes" {
		t.Fatalf("rows %+v", sh)
	}
	// Real agent close archives descendants and keeps closed records.
	for _, w := range []*session.Writer{nested, root} {
		if _, err := session.Archive(w.Path); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{nested.ID, root.ID} {
		if err := agentstate.MarkClosed(id, ""); err != nil {
			t.Fatal(err)
		}
	}
	c.reload()
	sh = c.shown()
	if got := treeIDs(sh); !reflect.DeepEqual(got, []string{heading, root.ID, nested.ID}) {
		t.Fatalf("archived inventory %v", got)
	}
	if sh[1].tab != tabInactive || sh[1].model != "fake/fast" || sh[2].agentPath != "/root/tests/lint" {
		t.Fatalf("closed rows %+v", sh)
	}
	// A truly deleted ordinary parent leaves its agents at top level, not hidden.
	parent := session.New("/work")
	parent.Append(session.Entry{Type: session.TypeName, Name: "parent"})
	parent.Close()
	child := session.NewAgent("/work", parent.ID)
	child.Append(session.Entry{Type: session.TypeName, Name: "kid"})
	child.Close()
	if err := agentstate.Save(agentstate.State{Parent: parent.ID, Session: child.ID, Name: "kid", Cwd: "/work", Created: time.Unix(3, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(parent.Path); err != nil {
		t.Fatal(err)
	}
	c.reload()
	var kid *centerItem
	for _, it := range c.shown() {
		if it.id == child.ID {
			kid = &it
		}
	}
	if kid == nil || kid.depth != 0 {
		t.Fatalf("orphan row %+v", kid)
	}
}

func TestCenterEmptyParentStillAnchorsAgent(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	oldP := listWorkers
	listWorkers = func() ([]daemon.Worker, error) { return nil, nil }
	t.Cleanup(func() { listWorkers = oldP })
	parent := session.New("/work")
	parent.Append(session.Entry{Type: session.TypeName, Name: "empty parent"})
	parent.Close()
	child := session.NewAgent("/work", parent.ID)
	child.Append(session.Entry{Type: session.TypeName, Name: "tests"})
	child.Close()
	c := &agentCenter{}
	c.reload()
	sh := c.shown()
	if len(sh) != 2 || sh[0].id != parent.ID || sh[1].depth != 1 {
		t.Fatalf("empty parent tree %+v", sh)
	}
}

func TestCenterCtrlCClosesWithoutInterruptingSession(t *testing.T) {
	for _, typing := range []bool{false, true} {
		t.Run(map[bool]string{false: "list", true: "search"}[typing], func(t *testing.T) {
			a, _ := recordedApp(t)
			fakeCenter(t, nil, nil)
			a.busy = true
			a.editor.SetText("keep my draft")
			before := a.threadID
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
			if !a.busy {
				t.Fatal("center interrupted underlying work")
			}
			if a.threadID != before || a.editor.Text() != "keep my draft" || quitting(a) {
				t.Fatal("center changed or quit the session")
			}
			a.busy = false
		})
	}
}
