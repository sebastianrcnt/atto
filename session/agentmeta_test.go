package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
)

func TestAgentMetaValidation(t *testing.T) {
	root := AgentMeta{Version: 1, RootSessionID: "r1", Path: "/root", Name: "tests", Origin: OriginExternal}
	child := AgentMeta{Version: 1, ParentSessionID: new("r1"), RootSessionID: "r1", Depth: 1, Path: "/root/lint", Name: "lint"}
	for _, c := range []struct {
		name string
		id   string
		m    AgentMeta
		err  string
	}{
		{"root", "r1", root, ""},
		{"child", "c1", child, ""},
		{"no version", "r1", AgentMeta{RootSessionID: "r1", Path: "/root"}, "no version"},
		{"newer version", "r1", AgentMeta{Version: 9, RootSessionID: "r1", Path: "/root"}, "newer"},
		{"root that is not its own root", "r2", root, "own root"},
		{"root with depth", "r1", func() AgentMeta { m := root; m.Depth = 1; return m }(), "depth 0"},
		{"self parent", "c1", func() AgentMeta { m := child; m.ParentSessionID = new("c1"); return m }(), "own parent"},
		{"child that is its own root", "r1", func() AgentMeta { m := child; m.RootSessionID = "r1"; m.ParentSessionID = new("x"); return m }(), "own root"},
		{"child at depth 0", "c1", func() AgentMeta { m := child; m.Depth = 0; return m }(), "depth 0"},
		{"path not from /root", "c1", func() AgentMeta { m := child; m.Path = "/x/lint"; return m }(), "/root"},
		{"path without its name", "c1", func() AgentMeta { m := child; m.Path = "/root/other"; return m }(), "name"},
	} {
		err := c.m.Validate(c.id)
		if c.err == "" && err != nil || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestAgentHeaderRoundTripsAndHidesRoots(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := NewManaged("/work", func(id string) AgentMeta {
		return AgentMeta{Version: 1, RootSessionID: id, Path: "/root", Name: "tests", Role: "general", SpawnCwd: "/work/pkg", Project: "/work", Origin: OriginExternal}
	})
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "task"}})
	w.Close()
	h, _, err := Load(w.Path)
	if err != nil || h.Agent == nil || !h.IsAgent() || !h.Agent.IsRoot() || h.Agent.RootSessionID != w.ID || h.AgentOf != "" || h.External {
		t.Fatalf("header %+v %v", h, err)
	}
	b, err := os.ReadFile(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	if !strings.Contains(line, `"parentSessionId":null`) {
		t.Fatalf("a root's parent is null in the header: %s", line)
	}
	// A parentless agent is not an ordinary session: lists leave it out.
	if l, _ := List("", false); len(l) != 0 {
		t.Fatalf("listed %+v", l)
	}
	all, _ := ListAll("", false)
	if len(all) != 1 || all[0].Agent == nil || all[0].Agent.Project != "/work" {
		t.Fatalf("all %+v", all)
	}
	if _, ok := Latest("/work"); ok {
		t.Fatal("continue would pick a managed agent")
	}
	// A header from before the agent object is still an agent by AgentOf.
	old := json.RawMessage(`{"type":"session","id":"old1","cwd":"/work","agentOf":"p"}`)
	var e Entry
	if err := json.Unmarshal(old, &e); err != nil || !e.IsAgent() {
		t.Fatalf("old header %+v %v", e, err)
	}
}
