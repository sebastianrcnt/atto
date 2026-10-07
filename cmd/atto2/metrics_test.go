package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"atto2/agent"
	"atto2/cortex"
	"atto2/kernel"
	"atto2/machine"
)

func metricsAgent() *agent.Agent {
	return &agent.Agent{
		Steps:   3,
		Cortex:  &cortex.Cortex{Tokens: 123},
		Machine: &machine.Machine{Kernel: &kernel.Kernel{Log: []kernel.Entry{{Name: "exit"}}, PureRuns: 1, ImpureRuns: 2}},
	}
}

func TestWriteMetrics(t *testing.T) {
	for _, runErr := range []error{nil, errors.New("life failed")} {
		path := filepath.Join(t.TempDir(), "metrics.json")
		if err := writeMetrics(path, metricsAgent(), "report", runErr); err != runErr {
			t.Fatalf("error: %v, want %v", err, runErr)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"report": "report", "steps": float64(3), "prompt_tokens": float64(123), "syscalls": float64(1), "pure_runs": float64(1), "impure_runs": float64(2)}
		if runErr != nil {
			want["error"] = runErr.Error()
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("metrics: %v, want %v", got, want)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode: %v, %v", info, err)
		}
	}
}

func TestMetricsWriteErrorPriority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "metrics.json")
	a := metricsAgent()
	if err := writeMetrics(path, a, "report", nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing write error: %v", err)
	}
	runErr := errors.New("life failed")
	if err := writeMetrics(path, a, "report", runErr); err != runErr {
		t.Fatalf("replaced run error: %v", err)
	}
	if err := writeMetrics("", a, "report", runErr); err != runErr {
		t.Fatalf("disabled metrics replaced run error: %v", err)
	}
}
