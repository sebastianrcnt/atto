package agent

import (
	"math"
	"testing"
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
