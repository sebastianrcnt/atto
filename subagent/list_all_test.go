package subagent

import "testing"

func TestListAllIncludesOrphansAndNestedAgents(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for _, s := range []State{{Name: "tests", Parent: "root", Session: "tests"}, {Name: "lint", Parent: "tests", Session: "lint"}, {Name: "orphan", Parent: "missing", Session: "orphan"}} {
		if err := Save(s); err != nil {
			t.Fatal(err)
		}
		if err := SaveTurn(s.Parent, s.Name, Turn{N: 1, Status: Done}); err != nil {
			t.Fatal(err)
		}
	}
	all := ListAll()
	if len(all) != 3 {
		t.Fatalf("all: %+v", all)
	}
	// Turn and reverse-index files are not mistaken for agents.
	for _, s := range all {
		if s.Name == "" || s.Session == "" {
			t.Fatalf("invalid inventory row: %+v", s)
		}
	}
}
