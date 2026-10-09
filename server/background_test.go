package server

import (
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agentstate"
)

func TestAgentTreeIncludesOnlyRelatedDescendants(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for _, state := range []agentstate.State{
		{Name: "tests", Parent: "root-session", Session: "test-session"},
		{Name: "lint", Parent: "test-session", Session: "lint-session"},
		{Name: "other", Parent: "unrelated", Session: "other-session"},
	} {
		if err := agentstate.Save(state); err != nil {
			t.Fatal(err)
		}
	}
	value, err := background("agent/tree", "test-session", threadParams{})
	if err != nil {
		t.Fatal(err)
	}
	tree := value.(map[string]any)
	agents := tree["agents"].([]Agent)
	if tree["rootThreadId"] != "root-session" || len(agents) != 2 {
		t.Fatalf("tree: %#v", tree)
	}
	if agents[0].Path != "/root/tests" || agents[1].Path != "/root/tests/lint" || agents[1].ParentThreadID != "test-session" {
		t.Fatalf("agents: %+v", agents)
	}
	value, err = background("agent/read", "root-session", threadParams{Name: "/root/tests/lint"})
	if err != nil || value.(map[string]any)["agent"].(Agent).ThreadID != "lint-session" {
		t.Fatalf("nested agent read: %#v %v", value, err)
	}
	if _, err := background("agent/read", "root-session", threadParams{Name: "/root"}); err == nil {
		t.Fatal("root is not an agent")
	}
}

func TestAgentReadSessionIDAddresses(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for _, st := range []agentstate.State{
		{Name: "tests", Parent: "root", Session: "abcdef12"},
		{Name: "lint", Parent: "abcdef12", Session: "12345678"},
		{Name: "unrelated", Parent: "other-root", Session: "abcdef34"},
	} {
		if err := agentstate.Save(st); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{"agent/read", "subagent/read"} {
		for _, tc := range []struct{ sid, addr, want string }{
			{"root", "@abcdef12", "abcdef12"},
			{"root", "@abcdef", "abcdef12"}, // another tree cannot cause ambiguity
			{"12345678", "@abcdef12", "abcdef12"},
			{"abcdef12", "@123456", "12345678"},
		} {
			value, err := background(method, tc.sid, threadParams{Name: tc.addr})
			if err != nil || value.(map[string]any)["agent"].(Agent).ThreadID != tc.want {
				t.Fatalf("%s %s: %#v %v", method, tc.addr, value, err)
			}
			if method == "subagent/read" && value.(map[string]any)["subagent"].(Agent).ThreadID != tc.want {
				t.Fatal("alias did not retain response fields")
			}
		}
		for _, addr := range []string{"@abcdef34", "@fffffff", "@abcde"} {
			_, err := background(method, "root", threadParams{Name: addr})
			if err == nil {
				t.Fatalf("%s accepted %s", method, addr)
			}
			if addr == "@abcdef34" && !strings.Contains(err.Error(), "no such agent") {
				t.Fatal("other-tree lookup leaked agent:", err)
			}
		}
	}
	if err := agentstate.Save(agentstate.State{Name: "second", Parent: "root", Session: "abcdef56"}); err != nil {
		t.Fatal(err)
	}
	_, err := background("agent/read", "root", threadParams{Name: "@abcdef"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "@abcdef12") || !strings.Contains(err.Error(), "@abcdef56") || strings.Contains(err.Error(), "@abcdef34") {
		t.Fatal("scoped ambiguity:", err)
	}
	if err := agentstate.Remove("root", "tests"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"agent/read", "subagent/read"} {
		_, err := background(method, "root", threadParams{Name: "@abcdef12"})
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("%s closed: %v", method, err)
		}
		_, err = background(method, "other-root", threadParams{Name: "@abcdef12"})
		if err == nil || !strings.Contains(err.Error(), "no such agent") {
			t.Fatal("closed agent exposed across trees:", err)
		}
	}
}
