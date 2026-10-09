package server

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

func TestStatusLineAndDebugRPC(t *testing.T) {
	h := newHarness(t)
	if got := h.call("thread/statusLine", nil)["configured"]; got != false {
		t.Fatalf("unconfigured status: %v", got)
	}
	if err := config.UpdateSettings(map[string]any{"statusLine": config.StatusLine{Command: "echo custom-status", RefreshInterval: 3}}); err != nil {
		t.Fatal(err)
	}
	out := h.call("thread/statusLine", nil)
	lines := out["lines"].([]any)
	if len(lines) != 1 || strings.TrimSpace(lines[0].(string)) != "custom-status" || out["refreshInterval"] != float64(3) {
		t.Fatalf("custom status: %v", out)
	}
	profiles := h.call("thread/debug", nil)
	if heap, err := base64.StdEncoding.DecodeString(profiles["heap"].(string)); err != nil || len(heap) == 0 {
		t.Fatalf("heap profile: %v", err)
	}
	if profiles["goroutines"] == "" || profiles["memory"] == nil {
		t.Fatal("incomplete debug profiles")
	}
	writer := &boundedOutput{limit: 4}
	_, _ = writer.Write([]byte("abc"))
	if writer.truncated {
		t.Fatal("short status incorrectly marked truncated")
	}
	_, _ = writer.Write([]byte("defgh"))
	if writer.String() != "abcd" || !writer.truncated {
		t.Fatalf("bounded output: %q / %v", writer.String(), writer.truncated)
	}
}
