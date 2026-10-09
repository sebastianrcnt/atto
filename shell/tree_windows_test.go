//go:build windows

package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestDetachedTreeHelper(t *testing.T) {
	role := os.Getenv("ATTO_TEST_DETACH_ROLE")
	if role == "" {
		return
	}
	dir := os.Getenv("ATTO_TEST_DETACH_DIR")
	if role == "child" {
		if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(1)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "assigned")); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestDetachedTreeHelper$")
	cmd.Env = append(os.Environ(), "ATTO_TEST_DETACH_ROLE=child")
	if os.Getenv("ATTO_TEST_ISOLATE") == "1" {
		Isolate(cmd)
	} else {
		Detach(cmd)
	}
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestDetachOutlivesTree(t *testing.T) { testOutlivesTree(t) }

func TestIsolateOutlivesTree(t *testing.T) {
	t.Setenv("ATTO_TEST_ISOLATE", "1")
	testOutlivesTree(t)
}

func testOutlivesTree(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestDetachedTreeHelper$")
	cmd.Env = append(os.Environ(), "ATTO_TEST_DETACH_ROLE=parent", "ATTO_TEST_DETACH_DIR="+dir)
	tree := NewTree(cmd)
	defer tree.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tree.Started()
	if err := os.WriteFile(filepath.Join(dir, "assigned"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	tree.Close() // ordinary children die here; a detached supervisor must not.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(dir, "pid")); err == nil {
			pid, err := strconv.Atoi(string(b))
			if err != nil {
				t.Fatal(err)
			}
			defer Terminate(pid)
			if !Alive(pid) {
				t.Fatal("detached child died with its parent's tree")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("detached child did not start")
}

// Killing a tree takes the command's descendants with it, whether or not the
// shell started them inside the job (Windows PowerShell 5.1 does not).
func TestTreeKillTakesDescendants(t *testing.T) {
	sh := Default()
	script := "ping -n 60 127.0.0.1 > $null"
	if sh.Kind == Cmd {
		script = "ping -n 60 127.0.0.1 > nul"
	}
	cmd := sh.Command(t.Context(), script)
	cmd.Dir = t.TempDir()
	tree := NewTree(cmd)
	defer tree.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tree.Started()
	var kids []uint32
	for deadline := time.Now().Add(10 * time.Second); len(kids) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		kids = descendants(uint32(cmd.Process.Pid))
	}
	if len(kids) == 0 {
		t.Fatal("the shell started nothing")
	}
	tree.Kill()
	_ = cmd.Wait()
	for _, pid := range kids {
		for deadline := time.Now().Add(5 * time.Second); Alive(int(pid)); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d outlived its tree", pid)
			}
		}
	}
}
