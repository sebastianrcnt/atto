package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atto2/agent"
	"atto2/cortex"
	"atto2/model"
)

func TestBaseline(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, ok := body["tools"]; ok {
			t.Error("baseline has tools")
		}
		var messages []model.Message
		if err := json.Unmarshal(body["messages"], &messages); err != nil {
			t.Error(err)
		}
		if len(messages) != 1 || messages[0].Content != "question" {
			t.Error(messages)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": model.Message{Content: "42"}}}, "usage": model.Usage{PromptTokens: 12, CompletionTokens: 3}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	a := &agent.Agent{Cortex: cortex.New("unused"), Model: &model.Client{BaseURL: server.URL}}
	got, err := run(context.Background(), a, "question", true)
	if err != nil || got != "42" || calls != 1 || a.Steps != 1 || a.Cortex.Tokens != 12 || a.CompletionTokens != 3 {
		t.Fatalf("%q %v %+v", got, err, a)
	}
}
