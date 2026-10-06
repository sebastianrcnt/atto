package session

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBackgroundHandoffHelper(t *testing.T) {
	mode := os.Getenv("ATTO_TEST_HANDOFF_MODE")
	if mode == "" {
		return
	}
	path := os.Getenv("ATTO_TEST_HANDOFF_PATH")
	switch mode {
	case "parent":
		release, err := LockTUI(path)
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestBackgroundHandoffHelper$")
		child.Env = append(os.Environ(), "ATTO_TEST_HANDOFF_MODE=child")
		child.Stdout, child.Stderr = os.Stderr, os.Stderr
		if err := StartBackground(path, child); err != nil {
			t.Fatal(err)
		}
		// Old TUI releases must not unlock the child's shared file description.
		release()
		release()
		fmt.Println(child.Process.Pid)
		os.Exit(0) // deliberately skip cleanup and exit the handing-off process
	case "child":
		release, err := AdoptBackgroundLock(path)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if err := os.WriteFile(path+".adopted", []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "reject":
		if release, err := AdoptBackgroundLock(path + ".wrong"); err == nil {
			release()
			t.Fatal("adopted wrong session")
		}
	case "ordinary":
		// Models the old cli/bgrun.go behavior: a separate open cannot acquire
		// a background parent's OS lease, even if metadata already names child.
		release, err := LockKind(path, KindBackground)
		if release != nil {
			release()
		}
		if !errors.Is(err, ErrLocked) {
			t.Fatalf("ordinary background acquisition: %v", err)
		}
	}
}

func TestOldBackgroundSpawnCannotAcquireParentLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	release, err := LockFor(path, 12345)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBackgroundHandoffHelper$")
	cmd.Env = append(os.Environ(), "ATTO_TEST_HANDOFF_MODE=ordinary", "ATTO_TEST_HANDOFF_PATH="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func TestBackgroundHandoffSurvivesParentExitAndChildKill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	parent := exec.Command(os.Args[0], "-test.run=^TestBackgroundHandoffHelper$")
	parent.Env = append(os.Environ(), "ATTO_TEST_HANDOFF_MODE=parent", "ATTO_TEST_HANDOFF_PATH="+path)
	output, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	parent.Stderr = os.Stderr
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Process.Kill(); _ = parent.Wait() })
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("child PID %q: %v", line, err)
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Kill(); _ = child.Release() })
	if err := parent.Wait(); err != nil {
		t.Fatal(err)
	}
	// Wait for final adoption, not just the first guard acknowledgement.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path + ".adopted"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not finish adoption")
		}
		time.Sleep(time.Millisecond)
	}
	l, ok := LockedBy(path)
	if !ok || l.PID != pid || l.Kind != KindBackground {
		t.Fatalf("after parent exit: %+v %v", l, ok)
	}
	if _, err := Lock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("child lease: %v", err)
	}
	if err := child.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		if _, ok := LockedBy(path); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("killed background child still holds lease")
		}
		time.Sleep(time.Millisecond)
	}
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestBackgroundStartFailureRetainsTUILease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	release, err := LockTUI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cmd := exec.Command(filepath.Join(t.TempDir(), "missing-executable"))
	if err := StartBackground(path, cmd); err == nil {
		t.Fatal("missing executable started")
	}
	if l, ok := LockedBy(path); !ok || l.PID != os.Getpid() || l.Kind != KindTUI {
		t.Fatalf("TUI lease lost: %+v %v", l, ok)
	}
	if _, err := Lock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("failed handoff allowed another writer: %v", err)
	}
}

func TestBackgroundAdoptionFailureRetainsTUILease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	release, err := LockTUI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBackgroundHandoffHelper$")
	cmd.Env = append(os.Environ(), "ATTO_TEST_HANDOFF_MODE=reject", "ATTO_TEST_HANDOFF_PATH="+path)
	cmd.Stderr = os.Stderr
	if err := StartBackground(path, cmd); err == nil {
		t.Fatal("child adopted wrong session")
	}
	if l, ok := LockedBy(path); !ok || l.PID != os.Getpid() || l.Kind != KindTUI {
		t.Fatalf("failed adoption lost TUI lease: %+v %v", l, ok)
	}
	if _, err := Lock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("failed adoption allowed another writer: %v", err)
	}
}
