package agentstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

// installOldLayout models ~/.atto/subagents/ from older installs.
func installOldLayout(t *testing.T) {
	t.Helper()
	for _, name := range []string{"a.json", "a.turn.json"} {
		data, err := os.ReadFile(filepath.Join("testdata", "old-layout", "p", name))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(legacyDir(), "p", name), string(data))
	}
}

func TestOldLayoutMigration(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	installOldLayout(t)
	s, err := Load("p", "a")
	if err != nil || s.Session != "child" {
		t.Fatalf("old state: %+v %v", s, err)
	}
	if turn, ok := LoadTurn("p", "a"); !ok || turn.Status != Done {
		t.Fatalf("old turn: %+v %v", turn, ok)
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(filepath.Join(config.AgentStateDir(), "p", "a.json")); err != nil {
			t.Fatal("state was not moved:", err)
		}
		// An old daemon can still write through its original path.
		write(t, filepath.Join(legacyDir(), "p", "a.turn.json"), `{"turn":2,"status":"running"}`)
		if turn, _ := LoadTurn("p", "a"); turn.N != 2 {
			t.Fatalf("old writer's update: %+v", turn)
		}
	}
	if PathOf("child") != "/root/a" || len(ListAll()) != 1 {
		t.Fatal("old state not projected into tree/list")
	}
	if config.AgentsDir() == stateRoot() {
		t.Fatal("state confused with roles")
	}
}

func TestLayoutMigrationDefersBusyLocks(t *testing.T) {
	for _, lock := range []string{"run/daemon.lock", "p/.tree.lock", "p/a.lock", "p/slots/0"} {
		t.Run(lock, func(t *testing.T) {
			t.Setenv(config.EnvDir, t.TempDir())
			installOldLayout(t)
			path := filepath.Join(legacyDir(), lock)
			if lock == "run/daemon.lock" {
				path = filepath.Join(config.Dir(), lock)
			}
			release, err := lockFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if _, err := Load("p", "a"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(config.AgentStateDir()); !os.IsNotExist(err) {
				t.Fatalf("moved a busy layout: %v", err)
			}
		})
	}
}

func TestBothLayoutsPreferNewAndKeepOldOnlyIDs(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	installOldLayout(t)
	write(t, filepath.Join(config.AgentStateDir(), "p", "a.json"), `{"name":"a","parent":"p","session":"new"}`)
	write(t, filepath.Join(legacyDir(), "p", "b.json"), `{"name":"b","parent":"p","session":"old-only"}`)
	if s, _ := Load("p", "a"); s.Session != "new" {
		t.Fatalf("new state lost: %+v", s)
	}
	if len(List("p")) != 2 || len(ListAll()) != 2 || PathOf("old-only") != "/root/b" {
		t.Fatal("merged inventory/tree lost old ids or duplicated new ids")
	}
	s, err := Load("p", "b")
	if err != nil {
		t.Fatal(err)
	}
	s.Task = "updated"
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if err := SaveTurn("p", "b", Turn{N: 2, Status: Done}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(legacyDir(), "p", "b.turn.json")); err != nil {
		t.Fatal("turn not kept beside old record:", err)
	}
	if err := Create(s); err == nil {
		t.Fatal("created duplicate old id")
	}
	Remove("p", "a")
	if len(List("p")) != 1 {
		t.Fatal("deleted id resurrected from old layout")
	}
}

func TestConcurrentLayoutMigration(t *testing.T) {
	if name := os.Getenv("ATTO_LAYOUT_CHILD"); name != "" {
		if _, err := Load("p", "a"); err != nil {
			t.Fatal(err)
		}
		if err := Create(State{Name: name, Parent: "p", Session: name}); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Setenv(config.EnvDir, t.TempDir())
	installOldLayout(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var commands []*exec.Cmd
	for _, name := range []string{"b", "c", "d", "e"} {
		cmd := exec.Command(exe, "-test.run=^TestConcurrentLayoutMigration$")
		cmd.Env = append(os.Environ(), "ATTO_LAYOUT_CHILD="+name)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if len(List("p")) != 5 {
		t.Fatalf("concurrent migration lost state: %+v", List("p"))
	}
}
