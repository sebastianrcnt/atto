package server

import (
	"context"
	"testing"
	"time"
)

func TestClientPromptFirstAnswerWins(t *testing.T) {
	h := newHarness(t)
	other := h.connect()
	p := h.call("prompt/clientOpen", map[string]any{"prompt": Prompt{Kind: PromptSelect, RequestID: "picker-1", Title: "Pick", Options: []PromptOption{{Label: "one"}, {Label: "two"}}}})
	id := p["id"].(string)
	if p["origin"] != "client" || p["clientId"] == "" {
		t.Fatalf("prompt %v", p)
	}
	if _, err := h.try(other, "prompt/answer", map[string]any{"id": id, "index": 1}); err != nil {
		t.Fatal(err)
	}
	answer := h.wait("prompt/clientAnswered", nil)
	if answer["clientId"] != p["clientId"] || answer["requestId"] != "picker-1" || answer["answer"].(map[string]any)["index"] != float64(1) {
		t.Fatalf("answer %v", answer)
	}
	if _, err := h.try(h.c, "prompt/answer", map[string]any{"id": id, "index": 0}); err == nil {
		t.Fatal("second answer accepted")
	}
	if h.call("thread/read", nil)["prompt"] != nil {
		t.Fatal("answered prompt remains open")
	}
}

func TestClientPromptOwnerDetachWithdrawsWithoutAnswer(t *testing.T) {
	h := newHarness(t)
	other := h.connect()
	if err := other.Call(context.Background(), "thread/attach", map[string]any{"threadId": h.id}, nil); err != nil {
		t.Fatal(err)
	}
	h.call("prompt/clientOpen", map[string]any{"prompt": Prompt{Kind: PromptInput, RequestID: "picker-1", Title: "Name"}})
	h.c.Close()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		read, err := h.try(other, "thread/read", nil)
		if err != nil {
			t.Fatal(err)
		}
		if read["prompt"] == nil {
			for {
				select {
				case n := <-h.ev:
					if n.Method == "prompt/clientAnswered" {
						t.Fatal("detaching the picker owner supplied an answer")
					}
				default:
					return
				}
			}
		}
	}
	t.Fatal("detached client's picker remained open")
}

func TestClientMultiSelectPrompt(t *testing.T) {
	h := newHarness(t)
	other := h.connect()
	prompt := h.call("prompt/clientOpen", map[string]any{"prompt": Prompt{Kind: PromptMultiSelect, RequestID: "multi", Title: "Pick several", Options: []PromptOption{{Label: "one"}, {Label: "two"}, {Label: "three"}}}})
	for _, indexes := range [][]int{{0, 0}, {-1}, {3}} {
		if _, err := h.try(other, "prompt/answer", map[string]any{"id": prompt["id"], "indexes": indexes}); err == nil {
			t.Fatalf("invalid indexes accepted: %v", indexes)
		}
	}
	if _, err := h.try(other, "prompt/answer", map[string]any{"id": prompt["id"], "indexes": []int{0, 2}}); err != nil {
		t.Fatal(err)
	}
	answer := h.wait("prompt/clientAnswered", nil)["answer"].(map[string]any)["indexes"].([]any)
	if len(answer) != 2 || answer[0] != float64(0) || answer[1] != float64(2) {
		t.Fatalf("multi-select answer: %v", answer)
	}
	if _, err := h.try(h.c, "prompt/answer", map[string]any{"id": prompt["id"], "indexes": []int{1}}); err == nil {
		t.Fatal("second multi-select answer accepted")
	}
}
