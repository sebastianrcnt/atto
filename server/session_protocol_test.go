package server

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
)

// One client drives a session end to end through its connection: input
// that starts, steers and is taken back, images, the panels' lists, model
// changes, rollback and prompt arbitration.
func TestSessionProtocol(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, providertest.Reply{Text: "answer", Gate: gate}, providertest.Reply{Text: "done"})
	read := h.call("thread/read", nil)
	if read["eventId"] == nil || read["hasMore"] == nil || read["before"] == nil {
		t.Fatalf("snapshot %v", read)
	}
	if list := h.call("thread/list", nil)["threads"].([]any); len(list) == 0 {
		t.Fatal("empty thread list")
	}
	if r := h.call("input/submit", map[string]any{"input": "go"}); r["status"] != StatusStarted {
		t.Fatalf("start %v", r)
	}
	h.m.Started(5 * time.Second)
	steer := h.call("input/submit", map[string]any{"input": "also"})
	if steer["status"] != StatusSteered {
		t.Fatalf("steer %v", steer)
	}
	h.call("turn/unsteer", map[string]any{"inputId": steer["inputId"]})
	if _, err := h.try(h.c, "turn/unsteer", map[string]any{"inputId": steer["inputId"]}); err == nil {
		t.Fatal("taken steer accepted")
	}
	if _, err := h.try(h.c, "turn/background", nil); err == nil {
		t.Fatal("background without command accepted")
	}
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	if _, err := h.try(h.c, "turn/start", map[string]any{"input": " "}); err == nil {
		t.Fatal("empty input accepted")
	}
	close(gate)
	h.completed()
	h.call("turn/start", map[string]any{"input": "look", "images": []map[string]any{{"mimeType": "image/png", "data": png}}})
	h.completed()
	reqs := h.m.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[0], "go") || !strings.Contains(reqs[1], "image 1: 1x1 PNG") || strings.Contains(reqs[1], `"content":"also"`) {
		t.Fatalf("requests %v", reqs)
	}
	for _, method := range []string{"job/list", "agent/list"} {
		r := h.call(method, nil)
		key := "jobs"
		if method != "job/list" {
			key = "agents"
		}
		if len(r[key].([]any)) != 0 {
			t.Fatalf("%s %v", method, r)
		}
	}
	for _, alias := range []string{"subagent/list", "subagent/read"} {
		if _, err := h.try(h.c, alias, nil); err == nil {
			t.Fatalf("%s still served", alias)
		}
	}
	if r := h.call("thread/setEffort", map[string]any{"effort": "none"}); r["effort"] != "none" {
		t.Fatalf("effort %v", r)
	}
	if r := h.call("thread/setModel", map[string]any{"model": "fake/m"}); r["model"] != "fake/m" {
		t.Fatalf("model %v", r)
	}
	if r := h.call("thread/rollback", nil); !strings.HasPrefix(fmt.Sprint(r["input"]), "look\n\n[image 1:") {
		t.Fatalf("rollback %v", r)
	}
	// Prompts use first-answer arbitration and retain malformed-answer errors.
	th, _ := h.s.thread(h.id)
	var pid string
	th.call(func() error {
		wire, err := th.openClientPrompt("owner", Prompt{RequestID: "picker", Kind: PromptSelect, Options: []PromptOption{{Label: "yes"}}})
		pid = wire.ID
		return err
	})
	for _, bad := range []map[string]any{{"index": 0}, {"id": pid}, {"id": "missing", "cancel": true}, {"id": pid, "index": 2}} {
		_, err := h.try(h.c, "prompt/answer", bad)
		var re *rpcError
		if !errors.As(err, &re) || re.Code != codeInvalidParams {
			t.Fatalf("answer %v error %v", bad, err)
		}
	}
	h.call("prompt/answer", map[string]any{"id": pid, "index": 0})
	if _, err := h.try(h.c, "prompt/answer", map[string]any{"id": pid, "cancel": true}); err == nil {
		t.Fatal("second answer accepted")
	}
}
