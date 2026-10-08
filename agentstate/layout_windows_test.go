package agentstate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

func TestWindowsKeepsLegacyLayout(t *testing.T) {
	for _, guards := range []bool{false, true} {
		name := "without guards"
		if guards {
			name = "idle guards"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(config.EnvDir, t.TempDir())
			installOldLayout(t)
			if guards {
				for _, path := range []string{"p/.tree.lock", "p/a.lock", "p/slots/0"} {
					write(t, filepath.Join(legacyDir(), path), "")
				}
			}
			if root := stateRoot(); root != legacyDir() {
				t.Fatalf("legacy layout moved to %q", root)
			}
			if s, err := Load("p", "a"); err != nil || s.Session != "child" {
				t.Fatalf("old state: %+v %v", s, err)
			}
			if err := Create(State{Name: "b", Parent: "p", Session: "new"}); err != nil {
				t.Fatal(err)
			}
			if err := SaveTurn("p", "b", Turn{N: 1, Status: Done}); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"p/b.json", "p/b.turn.json"} {
				if _, err := os.Stat(filepath.Join(legacyDir(), path)); err != nil {
					t.Fatal("new state not kept in old layout:", err)
				}
			}
			for _, path := range []string{config.AgentStateDir(), filepath.Join(config.Dir(), ".agent-state-compat")} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("migration left %q: %v", path, err)
				}
			}
		})
	}
}
