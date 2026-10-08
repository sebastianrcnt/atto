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
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestDetachOutlivesTree(t *testing.T) {
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
