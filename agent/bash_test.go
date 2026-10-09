//go:build !windows

package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRunBashTimeoutKillsGroup(t *testing.T) {
	start := time.Now()
	// The background child would keep the pipe open without the group kill.
	res := RunBash(context.Background(), t.TempDir(), nil, BashArgs{Command: "sleep 30 & sleep 30", Timeout: 1}, nil)
	if !res.TimedOut {
		t.Fatalf("expected timeout, got %+v", res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s", d)
	}
	if out := res.ForModel(BashArgs{Timeout: 1}); !strings.Contains(out, "timed out after 1s") {
		t.Fatalf("model output %q", out)
	}
}

func TestRunBashExitCodeAndStreaming(t *testing.T) {
	var chunks strings.Builder
	res := RunBash(context.Background(), t.TempDir(), nil, BashArgs{Command: "echo out; echo err >&2; exit 3"}, func(s string) { chunks.WriteString(s) })
	if res.ExitCode != 3 || res.Output != "out\nerr\n" || chunks.String() != res.Output {
		t.Fatalf("got %+v, streamed %q", res, chunks.String())
	}
	if out := res.ForModel(BashArgs{}); !strings.HasSuffix(out, "[exit code 3]") {
		t.Fatalf("model output %q", out)
	}
}

func TestRunBashCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	res := RunBash(ctx, t.TempDir(), nil, BashArgs{Command: "sleep 30"}, nil)
	if !res.Canceled {
		t.Fatalf("expected canceled, got %+v", res)
	}
}

func TestTruncateMiddle(t *testing.T) {
	var b strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	out := truncateMiddle(b.String())
	if !strings.HasPrefix(out, "[output truncated: 20001 lines") || !strings.Contains(out, "full output: ") {
		t.Fatalf("header: %q", out[:120])
	}
	if !strings.Contains(out, "\nline 0\n") || !strings.Contains(out, "line 19999") || !strings.Contains(out, "lines omitted") {
		t.Fatal("start and end must both survive")
	}
	if _, body, _ := strings.Cut(out, "]\n"); len(body) > int(maxOutputBytes.Load())+100 {
		t.Fatalf("kept %d bytes", len(body))
	}
	if s := "short\n"; truncateMiddle(s) != s {
		t.Fatal("short output is untouched")
	}
}

func TestTidy(t *testing.T) {
	got := tidy("\r\nUptime  Free\r\n------  ----\r\n3h      3.9\r\n        \r\n        \r\n\r\n\r")
	if got != "\nUptime  Free\n------  ----\n3h      3.9" {
		t.Fatalf("%q", got)
	}
}

func TestToolOutputTokenLimit(t *testing.T) {
	defer SetToolOutputTokenLimit(0)
	SetToolOutputTokenLimit(100) // 400 bytes
	out := truncateMiddle(strings.Repeat("line of output\n", 200))
	if _, body, _ := strings.Cut(out, "]\n"); !strings.Contains(out, "[output truncated:") || len(body) > 400+100 {
		t.Fatalf("not cut to the limit: %d bytes", len(body))
	}
	SetToolOutputTokenLimit(0)
	if maxOutputBytes.Load() != DefaultToolOutputTokens*4 {
		t.Fatal("0 is the default")
	}
}
