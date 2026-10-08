package agent

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestBashBudgetClampsBeforeMultiplication(t *testing.T) {
	defer SetToolOutputTokenLimit(0)
	SetToolOutputTokenLimit(math.MaxInt)
	if got := maxOutputBytes.Load(); got != maxCaptureBytes {
		t.Fatalf("output bytes: %d", got)
	}
	if got, _, _, cut := cutMiddle("short output"); got != "short output" || cut {
		t.Fatalf("cut: %q, %v", got, cut)
	}
	if got := (BashArgs{Timeout: math.MaxInt}).TimeLimit(); got != MaxBashTimeout {
		t.Fatalf("timeout: %s", got)
	}
	for _, n := range []int{-1, 0} {
		if got := (BashArgs{Timeout: n}).TimeLimit(); got != DefaultBashTimeout {
			t.Fatalf("default timeout: %s", got)
		}
	}
}

func TestForegroundWait(t *testing.T) {
	for _, tt := range []struct {
		timeout int
		want    time.Duration
	}{
		{-1, DefaultShellWait},
		{0, DefaultShellWait},
		{1, time.Second},
		{20, 20 * time.Second},
		{30, MaxShellWait},
		{60, MaxShellWait},
		{math.MaxInt, MaxShellWait},
	} {
		if got := (BashArgs{Timeout: tt.timeout}).foregroundWait(); got != tt.want {
			t.Errorf("timeout %d: %s, want %s", tt.timeout, got, tt.want)
		}
	}
}

func TestWaitLimitNeedsHostAndSession(t *testing.T) {
	old := ShellHost
	defer func() { ShellHost = old }()
	for _, hosted := range []bool{false, true} {
		ShellHost = hosted
		for _, session := range []string{"", "s"} {
			for _, timeout := range []int{0, 120, math.MaxInt} {
				args := BashArgs{Timeout: timeout}
				want := args.timeout()
				if hosted && session != "" {
					want = args.foregroundWait()
				}
				if got := args.waitLimit(session); got != want {
					t.Errorf("host %v, session %q, timeout %d: %s, want %s", hosted, session, timeout, got, want)
				}
			}
		}
	}
}

func TestForModelUsesActualWaitLimit(t *testing.T) {
	args := BashArgs{Timeout: 120}
	res := BashResult{Job: 1, Background: BackgroundTimeout, WaitLimit: MaxShellWait}
	if got := res.ForModel(args); !strings.Contains(got, "still running after 30s;") {
		t.Fatalf("background result: %q", got)
	}
	res = BashResult{TimedOut: true, WaitLimit: MaxShellWait, Note: "job limit reached"}
	if got := res.ForModel(args); !strings.Contains(got, "timed out after 30s and killed:") {
		t.Fatalf("failed detach: %q", got)
	}
	if got := (BashResult{Job: 1, Background: BackgroundTimeout}).ForModel(BashArgs{}); !strings.Contains(got, "still running after 10s;") {
		t.Fatalf("default background result: %q", got)
	}
}

func TestShellSchemaDescribesForegroundWait(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(bashSchema, &schema); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"default 10, maximum 30", "background job", "atto job wait", "Without a shell host or session", "default 60, maximum 1800"} {
		if got := schema.Properties["timeout"].Description; !strings.Contains(got, want) {
			t.Errorf("timeout description %q lacks %q", got, want)
		}
	}
	for _, want := range []string{"immediately", "atto job wait", "exit event"} {
		if got := schema.Properties["run_in_background"].Description; !strings.Contains(got, want) {
			t.Errorf("background description %q lacks %q", got, want)
		}
	}
}
