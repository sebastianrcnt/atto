package agentstate

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/sebastianrcnt/atto/fsutil"
)

func TestLegacyAgentAncestry(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	a := State{Parent: "root", Name: "a", Session: "a-session"}
	b := State{Parent: a.Session, Name: "b", Session: "b-session"}
	for _, s := range []State{a, b} {
		if err := Save(s); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(upPath(s.Session)); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(s)
		if err := fsutil.WriteAtomic(statePath(s.Parent, s.Name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if Depth(b.Session) != 2 || Root(b.Session) != "root" || PathOf(b.Session) != "/root/a/b" {
		t.Fatal("legacy ancestry lost")
	}
	target, err := Resolve(b.Session, "..")
	if err != nil || target.State == nil || target.State.Session != a.Session {
		t.Fatalf("parent: %+v %v", target, err)
	}
	if err := os.Remove(upPath(a.Session)); err != nil {
		t.Fatal(err)
	}
	if err := Save(a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(upPath(a.Session)); err != nil {
		t.Fatal("Save did not repair index:", err)
	}
	if err := Save(a); err != nil {
		t.Fatal("Save is not idempotent:", err)
	}
}
