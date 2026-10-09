package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
)

func TestCenterShowsShellAgentsUnderOneVirtualHeadingPerProject(t *testing.T) {
	items := []centerItem{
		{id: "agent-b", title: "panes", shell: true, isAgent: true, projectRoot: "/project", cwd: "/worktree/b", tab: tabWorking},
		{id: "agent-a", title: "panes", shell: true, isAgent: true, projectRoot: "/project", cwd: "/worktree/a", tab: tabReady},
		{id: "nested", title: "lint", parent: "agent-a", isAgent: true, cwd: "/worktree/a"},
		{id: "other", title: "panes", shell: true, isAgent: true, projectRoot: "/other", cwd: "/other"},
		{id: "model-parent", cwd: "/project"},
		{id: "model-agent", title: "panes", parent: "model-parent", isAgent: true, cwd: "/project"},
	}
	original := append([]centerItem(nil), items...)
	tree := centerTree(items)
	if !reflect.DeepEqual(items, original) {
		t.Fatal("display grouping modified original items")
	}
	// The headings have no session behind them; the agents keep their IDs.
	if got := treeIDs(tree); !reflect.DeepEqual(got, []string{"shell:/project", "agent-b", "agent-a", "nested", "shell:/other", "other", "model-parent", "model-agent"}) {
		t.Fatalf("tree: %v", got)
	}
	for _, it := range tree {
		switch it.id {
		case "shell:/project", "shell:/other":
			if !it.virtual || it.title != "agents started from a shell" || it.depth != 0 {
				t.Fatalf("heading: %+v", it)
			}
		case "agent-a", "agent-b":
			if it.depth != 1 || it.parent != "shell:/project" || it.project != "/project" || it.agentPath != "/root/panes" {
				t.Fatalf("shell agent: %+v", it)
			}
		case "nested":
			if it.depth != 2 || it.agentPath != "/root/panes/lint" {
				t.Fatalf("nested tree lost: %+v", it)
			}
		}
	}
	c := &agentCenter{items: items, collapsed: map[string]bool{"shell:/project": true}}
	if got := treeIDs(c.shown()); !reflect.DeepEqual(got, []string{"shell:/project", "shell:/other", "other", "model-parent", "model-agent"}) {
		t.Fatalf("shell group collapse: %v", got)
	}
	c.tab = tabWorking
	if got := treeIDs(c.shown()); !reflect.DeepEqual(got, []string{"shell:/project", "agent-b"}) {
		t.Fatalf("filter should retain the heading: %v", got)
	}
	// A heading is not a session: entering it opens nothing.
	opened := ""
	c = &agentCenter{items: items, onClose: func() {}, onOpen: func(id, cwd string) { opened = id }}
	c.HandleInput("\r")
	if opened != "" {
		t.Fatalf("opened %q", opened)
	}
}

func TestCenterListsShellRootsOfSeveralSpawnsAndArchivedAgentsUnderOneHeading(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	project := t.TempDir()
	var roots []string
	for range 2 {
		w := session.NewManaged(project, func(id string) session.AgentMeta {
			return session.AgentMeta{Version: 1, RootSessionID: id, Path: "/root", Name: "panes", Project: project, Origin: session.OriginExternal}
		})
		w.Append(session.Entry{Type: session.TypeName, Name: "panes"})
		w.Close()
		roots = append(roots, w.ID)
		if err := agentstate.Save(agentstate.State{Session: w.ID, Name: "panes", Cwd: project, Project: project, Origin: session.OriginExternal, Created: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	// Read real inventory without relying on a running daemon.
	c := &agentCenter{}
	client, release := centerClient()
	defer release()
	inventory := func() []server.ThreadSummary {
		rows, err := listCenter(client)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	check := func(archived bool) {
		c.apply(inventory())
		if archived {
			for id := range c.collapsed {
				c.collapsed[id] = false
			}
		}
		tree := c.shown()
		if len(tree) != 3 || !tree[0].virtual || tree[0].title != "agents started from a shell" || tree[1].depth != 1 || tree[2].depth != 1 {
			t.Fatalf("shell forest: %+v", tree)
		}
		for _, it := range tree[1:] {
			if it.archived != archived || it.parent != tree[0].id || !it.shell {
				t.Fatalf("root agent: %+v", it)
			}
		}
	}
	check(false)
	for _, id := range roots {
		path, err := session.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.Archive(path); err != nil {
			t.Fatal(err)
		}
		if err := agentstate.MarkClosed(id, ""); err != nil {
			t.Fatal(err)
		}
	}
	check(true)
}

func TestCenterKeepsAnAgentRootOutOfTheResumePicker(t *testing.T) {
	c := &agentCenter{scope: "/work", resume: true}
	c.applyFixture(centerSnapshot{
		saved: []session.Summary{
			{ID: "plain", Cwd: "/work", Preview: "hello"},
			{ID: "root-agent", Cwd: "/work", Preview: "task", Agent: &session.AgentMeta{Version: 1, RootSessionID: "root-agent", Path: "/root"}},
		},
	})
	if got := treeIDs(c.shown()); !reflect.DeepEqual(got, []string{"plain"}) {
		t.Fatalf("picker: %v", got)
	}
}
