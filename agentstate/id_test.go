package agentstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSessionID(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	states := []State{
		{Parent: "project-one", Name: "tests", Session: "abcdef12"},
		{Parent: "abcdef12", Name: "lint", Session: "12345678"},
		{Parent: "project-two", Name: "tests", Session: "abcdef34"},
	}
	for _, s := range states {
		if err := Save(s); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, from, addr, want, err string }{
		{"full", "project-one", "@abcdef12", "abcdef12", ""},
		{"prefix", "project-one", "@abcdef", "abcdef12", ""},
		{"nested", "abcdef12", "@123456", "12345678", ""},
		{"ancestor", "12345678", "@abcdef12", "abcdef12", ""},
		{"outside across projects", "", "@abcdef34", "abcdef34", ""},
		{"inside other tree", "project-one", "@abcdef34", "", "no such agent"},
		{"missing", "", "@ffffff", "", "see atto agent list"},
		{"short", "", "@abcde", "", "at least 6 characters"},
		{"empty", "", "@", "", "at least 6 characters"},
		{"ambiguous", "", "@abcdef", "", "ambiguous"},
		{"name unchanged", "project-one", "tests", "abcdef12", ""},
		{"path unchanged", "project-one", "/root/tests/lint", "12345678", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := Resolve(tc.from, tc.addr)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("%+v: %v", target, err)
				}
				if tc.name == "ambiguous" && (!strings.Contains(err.Error(), "@abcdef12") || !strings.Contains(err.Error(), "@abcdef34")) {
					t.Fatal("candidates missing:", err)
				}
				if tc.name == "inside other tree" && !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || target.Session != tc.want || target.State == nil || target.State.Session != tc.want {
				t.Fatalf("%+v: %v", target, err)
			}
		})
	}
}

func TestClosedIDKeepsOriginalTreeAndDoesNotAliasReusedName(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	a := State{Parent: "root", Name: "tests", Session: "abcdef12"}
	b := State{Parent: a.Session, Name: "lint", Session: "12345678"}
	for _, s := range []State{a, b} {
		if err := Save(s); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []State{b, a} {
		if err := Remove(s.Parent, s.Name); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(State{Parent: "root", Name: "tests", Session: "abcdef34"}); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"@abcdef12", "@12345678", "@123456"} {
		for _, from := range []string{"", "root"} {
			if _, err := Resolve(from, addr); !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), "archived transcript") {
				t.Fatalf("%s from %q: %v", addr, from, err)
			}
		}
		if _, err := Resolve("other", addr); !errors.Is(err, ErrNotFound) {
			t.Fatal("closed agent exposed outside tree:", err)
		}
	}
	if target, err := Resolve("root", "tests"); err != nil || target.Session != "abcdef34" {
		t.Fatalf("reused name: %+v %v", target, err)
	}
	if _, err := Resolve("", "@abcdef"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatal("closed ID must count toward prefix ambiguity:", err)
	}
	if len(ListAll()) != 1 {
		t.Fatalf("closed records leaked into inventory: %+v", ListAll())
	}
	if _, _, ok := ParentOf(a.Session); ok {
		t.Fatal("removed reverse index retained")
	}
}

func TestExactIDWinsOverPrefix(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for _, s := range []State{{Parent: "root", Name: "a", Session: "abcdef12"}, {Parent: "root", Name: "b", Session: "abcdef123456"}} {
		if err := Save(s); err != nil {
			t.Fatal(err)
		}
	}
	target, err := Resolve("root", "@abcdef12")
	if err != nil || target.Session != "abcdef12" {
		t.Fatalf("%+v %v", target, err)
	}
	if ShortID("abcdef123456") != "abcdef12" || ShortID("abcdef12") != "abcdef12" {
		t.Fatal("short ID")
	}
}

func TestRemoveRetainsStateWhenClosedRecordCannotBeWritten(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	s := State{Parent: "root", Name: "a", Session: "abcdef12"}
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot(), "_closed"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Remove(s.Parent, s.Name); err == nil {
		t.Fatal("Remove hid recording error")
	}
	if _, err := Load(s.Parent, s.Name); err != nil {
		t.Fatal("state removed without closed record:", err)
	}
}
