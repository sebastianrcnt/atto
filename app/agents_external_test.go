package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
)

func TestCenterCoalescesShellParentsWithoutChangingRealAncestry(t *testing.T) {
	items := []centerItem{
		{id: "parent-b", title: "agents started from a shell", external: true, cwd: "/project"},
		{id: "agent-b", title: "panes", parent: "parent-b", cwd: "/worktree/b", tab: tabWorking},
		{id: "parent-a", title: "agents started from a shell", external: true, cwd: "/project"},
		{id: "agent-a", title: "panes", parent: "parent-a", cwd: "/worktree/a", tab: tabReady},
		{id: "nested", title: "lint", parent: "agent-a", cwd: "/worktree/a"},
		{id: "other-parent", title: "agents started from a shell", external: true, cwd: "/other"},
		{id: "other", title: "panes", parent: "other-parent", cwd: "/other"},
		{id: "model-parent", cwd: "/project"},
		{id: "model-agent", title: "panes", parent: "model-parent", cwd: "/project"},
	}
	original := append([]centerItem(nil), items...)
	tree := centerTree(items)
	if !reflect.DeepEqual(items, original) {
		t.Fatal("display grouping modified original items")
	}
	if got := treeIDs(tree); !reflect.DeepEqual(got, []string{"parent-a", "agent-b", "agent-a", "nested", "other-parent", "other", "model-parent", "model-agent"}) {
		t.Fatalf("tree: %v", got)
	}
	for _, it := range tree {
		if it.id == "agent-a" || it.id == "agent-b" {
			if it.depth != 1 || it.parent != "parent-a" || it.project != "/project" || it.agentPath != "/root/panes" {
				t.Fatalf("flat shell child: %+v", it)
			}
		}
		if it.id == "nested" && (it.depth != 2 || it.agentPath != "/root/panes/lint") {
			t.Fatalf("nested tree lost: %+v", it)
		}
	}
	c := &agentCenter{items: items, collapsed: map[string]bool{"parent-a": true}}
	if got := treeIDs(c.shown()); !reflect.DeepEqual(got, []string{"parent-a", "other-parent", "other", "model-parent", "model-agent"}) {
		t.Fatalf("shell group collapse: %v", got)
	}
	c.tab = tabWorking
	if got := treeIDs(c.shown()); !reflect.DeepEqual(got, []string{"parent-a", "agent-b"}) {
		t.Fatalf("filter should retain shared heading: %v", got)
	}
}

func TestCenterFreshExternalParentsAndArchivedAgentsShareOneHeading(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	project := t.TempDir()
	var parents, children []string
	for range 2 {
		w := session.NewExternal(project)
		w.Append(session.Entry{Type: session.TypeName, Name: "atto agent (external)"})
		w.Close()
		parents = append(parents, w.ID)
		child := session.NewAgent(project, w.ID)
		child.Append(session.Entry{Type: session.TypeName, Name: "panes"})
		child.Close()
		children = append(children, child.ID)
		if err := agentstate.Save(agentstate.State{Parent: w.ID, Session: child.ID, Name: "panes", Cwd: project, Created: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	// Read real inventory without relying on a running daemon.
	c := &agentCenter{}
	inventory := func() centerSnapshot {
		snapshot := scanCenter()
		snapshot.workers = nil
		return snapshot
	}
	check := func(archived bool) {
		c.apply(inventory())
		tree := c.shown()
		if len(tree) != 3 || !tree[0].external || tree[0].title != "agents started from a shell" || tree[1].depth != 1 || tree[2].depth != 1 {
			t.Fatalf("shell forest: %+v", tree)
		}
		for _, it := range tree[1:] {
			if it.archived != archived {
				t.Fatalf("archive status: %+v", it)
			}
		}
		for i, id := range children {
			for _, it := range c.items {
				if it.id == id && it.parent != parents[i] {
					t.Fatal("real parent lost")
				}
			}
		}
	}
	check(false)
	for i, id := range children {
		for _, sessionID := range []string{id, parents[i]} {
			path, err := session.Find(sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.Archive(path); err != nil {
				t.Fatal(err)
			}
		}
		if err := agentstate.Remove(parents[i], "panes"); err != nil {
			t.Fatal(err)
		}
	}
	check(true)
}

func TestCenterShellGroupPreservesTheCurrentParent(t *testing.T) {
	items := []centerItem{
		{id: "parent-a", external: true, cwd: "/project"},
		{id: "agent-a", parent: "parent-a", title: "panes", cwd: "/project"},
		{id: "parent-b", external: true, current: true, cwd: "/project"},
		{id: "agent-b", parent: "parent-b", title: "panes", cwd: "/project"},
	}
	tree := centerTree(items)
	if len(tree) != 3 || tree[0].id != "parent-b" || !tree[0].current {
		t.Fatalf("current shell parent hidden: %+v", tree)
	}
}
