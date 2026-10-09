//go:build !race

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func TestHeadlessSessionDisplayMemory(t *testing.T) {
	if os.Getenv("ATTO_HEADLESS_MEMORY") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHeadlessSessionDisplayMemory$", "-test.v")
		cmd.Env = append(os.Environ(), "ATTO_HEADLESS_MEMORY=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("headless worker: %v\n%s", err, out)
		}
		t.Log(string(out))
		return
	}
	s, _ := testServer(t)
	started, err := s.startThread("", threadParams{DeferStart: true})
	if err != nil {
		t.Fatal(err)
	}
	id := started.(ThreadInfo).ID
	th, _ := s.thread(id)
	output := strings.Repeat("synthetic tool result output\n", 1200)
	var entries []session.Entry
	var samples []memoryResult
	for turn := 1; turn <= 2000; turn++ {
		if err := th.call(func() error {
			if turn%50 == 1 {
				e := session.Entry{Type: session.TypeCompaction, Replacement: []provider.Message{{Role: "user", Content: "synthetic notes"}}, Notes: "synthetic notes"}
				th.sess.Append(e)
				entries = []session.Entry{e}
				th.feed(agent.CompactStart{})
				th.feed(agent.CompactEnd{Notes: "synthetic notes"})
			}
			question := fmt.Sprintf("synthetic question %d", turn)
			th.feed(transcript.Input{Text: question})
			user := session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: question}}
			th.sess.Append(user)
			th.feed(agent.UserMessageSaved{EntryID: th.sess.Leaf()})
			entries = append(entries, user)
			call := fmt.Sprintf("call-%d", turn)
			assistant := session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: call, Function: provider.FunctionCall{Name: "bash", Arguments: `{"description":"Synthetic tool","command":"echo test"}`}}}}}
			th.sess.Append(assistant)
			entries = append(entries, assistant)
			th.feed(agent.ToolStart{ID: call, Args: agent.BashArgs{Description: "Synthetic tool", Command: "echo test"}})
			th.feed(agent.ToolOutput{ID: call, Chunk: output})
			tool := session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "tool", ToolCallID: call, Content: output}, Tool: &session.ToolMeta{Description: "Synthetic tool"}}
			th.sess.Append(tool)
			entries = append(entries, tool)
			th.feed(agent.ToolEnd{ID: call, Text: output})
			answer := fmt.Sprintf("synthetic answer %d", turn)
			th.feed(agent.TextDelta{Text: answer})
			final := session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: answer}}
			th.sess.Append(final)
			th.feed(agent.MessageSaved{EntryID: th.sess.Leaf()})
			th.feed(agent.StepEnd{})
			entries = append(entries, final)
			th.tr.EndTurn()
			th.tr.ForgetCompleted()
			th.agent.Restore(entries)
			if len(th.items) != 0 || len(th.tr.Items()) != 0 || len(th.blocks) != 0 || len(th.itemOrder) != 0 {
				return fmt.Errorf("headless worker retained display state at turn %d: %d/%d/%d/%d", turn, len(th.items), len(th.tr.Items()), len(th.blocks), len(th.itemOrder))
			}
			return th.sess.Err()
		}); err != nil {
			t.Fatal(err)
		}
		if turn == 100 || turn == 1000 || turn == 2000 {
			debug.FreeOSMemory()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			sample := memoryResult{Operation: fmt.Sprintf("headless-%d", turn), HeapInuse: m.HeapInuse, RSS: currentRSS(), MaxRSS: maxRSS()}
			raw, _ := json.Marshal(sample)
			t.Logf("MEMORY %s", raw)
			samples = append(samples, sample)
		}
	}
	if samples[2].HeapInuse > samples[0].HeapInuse+12<<20 {
		t.Fatal("headless display heap grows with session length")
	}
	c := Connect(context.Background(), s)
	defer c.Close()
	if err := c.Call(context.Background(), "initialize", map[string]any{"protocolVersions": []int{3}}, nil); err != nil {
		t.Fatal(err)
	}
	var tail ThreadInfo
	if err := c.Call(context.Background(), "thread/attach", map[string]any{"threadId": id}, &tail); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range tail.Items {
		if it.Text == "synthetic answer 2000" {
			found = true
		}
	}
	if !found || !tail.HasMore || len(tail.Items) > 200 {
		t.Fatalf("reattach lost headless history: found=%v hasMore=%v items=%d", found, tail.HasMore, len(tail.Items))
	}
	if err := c.Call(context.Background(), "thread/detach", map[string]any{"threadId": id}, nil); err != nil {
		t.Fatal(err)
	}
	_ = th.call(func() error {
		if len(th.items) != 0 || len(th.tr.Items()) != 0 {
			t.Error("last detach retained tail")
		}
		return nil
	})
}
