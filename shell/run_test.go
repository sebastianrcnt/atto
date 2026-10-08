package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRunHelper(t *testing.T) {
	switch os.Getenv("ATTO_RUN_TEST") {
	case "ok":
		fmt.Fprintln(os.Stdout, "out")
		fmt.Fprintln(os.Stderr, "err")
		os.Exit(0)
	case "fail":
		os.Exit(7)
	case "sleep":
		if err := os.WriteFile(os.Getenv("ATTO_RUN_READY"), nil, 0o600); err != nil {
			os.Exit(1)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func runTestCommand(t *testing.T, ctx context.Context, role string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestRunHelper$")
	cmd.Env = append(os.Environ(), "ATTO_RUN_TEST="+role)
	return cmd
}

func TestRunOutputAndExit(t *testing.T) {
	cmd := runTestCommand(t, t.Context(), "ok")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := Run(cmd); err != nil || stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("%q %q: %v", stdout.String(), stderr.String(), err)
	}
	cmd = runTestCommand(t, t.Context(), "fail")
	var exit *exec.ExitError
	if err := Run(cmd); !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatal(err)
	}
}

func TestRunStartFailureAndWaitDelay(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), filepath.Join(t.TempDir(), "missing"))
	cmd.WaitDelay = 100 * time.Millisecond
	if err := Run(cmd); err == nil {
		t.Fatal("missing command ran")
	}
	if cmd.WaitDelay != 100*time.Millisecond {
		t.Fatal("caller wait delay replaced")
	}
}

func TestRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := runTestCommand(t, ctx, "sleep")
	ready := filepath.Join(t.TempDir(), "ready")
	cmd.Env = append(cmd.Env, "ATTO_RUN_READY="+ready)
	done := make(chan error, 1)
	go func() { done <- Run(cmd) }()
	deadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("command ended before cancellation: %v", err)
		case <-deadline:
			t.Fatal("command did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled command succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not stop the command")
	}
}
