package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/ai"
)

func serveSSE(t *testing.T, lines ...string) (*httptest.Server, *[]string, *[]http.Header) {
	var bodies []string
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		headers = append(headers, r.Header.Clone())
		for _, l := range lines {
			fmt.Fprintf(w, "data: %s\n\n", l)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies, &headers
}

func TestClientCompletionsRoundTrip(t *testing.T) {
	srv, bodies, headers := serveSSE(t,
		`{"choices":[{"delta":{"reasoning":"hmm"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"bash","arguments":"{\"command\": \"ls\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":6}}}`)
	c := &Client{
		Model:   ai.Model{ID: "m", Api: ai.ApiOpenAICompletions, Provider: "local", BaseURL: srv.URL, Input: []string{"text"}},
		KeyFunc: func(context.Context) (string, error) { return "fresh", nil },
		Headers: map[string]string{"X-Session": "$session", "X-None": "$session"},
	}
	var reasoning string
	var started []string
	res, err := c.Stream(context.Background(), Request{SessionID: "s1", Messages: []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}},
		Handler{OnReasoning: func(s string) { reasoning += s }, OnToolCall: func(i int, id, name string) { started = append(started, fmt.Sprint(i, id, name)) }})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Message
	// No id from the server: atto names it.
	if reasoning != "hmm" || len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != "call_0" || m.ToolCalls[0].Function.Arguments != `{"command": "ls"}` {
		t.Fatalf("message %+v reasoning %q", m, reasoning)
	}
	if m.Provider != "local" || m.API != ai.ApiOpenAICompletions || m.Model != "m" || m.ThinkingSignature != "reasoning" || res.FinishReason != "tool_calls" {
		t.Fatalf("provenance/signature %+v %s", m, res.FinishReason)
	}
	if res.Usage != (Usage{PromptTokens: 10, CompletionTokens: 4, CachedTokens: 6}) || len(started) != 1 {
		t.Fatalf("usage %+v started %v", res.Usage, started)
	}
	if h := (*headers)[0]; h.Get("Authorization") != "Bearer fresh" || h.Get("X-Session") != "s1" || h.Get("User-Agent") != UserAgent {
		t.Fatalf("headers %v", h)
	}

	// Replay: the reasoning goes back in the field it came from.
	msgs := []Message{{Role: "user", Content: "hi"}, m, {Role: "tool", ToolCallID: "call_0", Content: "out"}}
	if _, err := c.Stream(context.Background(), Request{Messages: msgs}, Handler{}); err != nil {
		t.Fatal(err)
	}
	if b := (*bodies)[1]; !strings.Contains(b, `"reasoning":"hmm"`) || !strings.Contains(b, `"tool_call_id":"call_0"`) {
		t.Fatalf("replay %s", b)
	}
	if h := (*headers)[1]; h.Get("X-Session") != "" {
		t.Fatalf("$session without a session must be skipped: %v", h)
	}
}

func TestClientResponsesKeepsItemsAcrossSessionJSON(t *testing.T) {
	srv, bodies, _ := serveSSE(t,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_9","call_id":"call_9","name":"bash","arguments":"{}"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`)
	c := &Client{Model: ai.Model{ID: "gpt-x", Api: ai.ApiOpenAIResponses, Provider: "openai", BaseURL: srv.URL, Reasoning: true}, APIKey: "sk-1"}
	res, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "go"}}, Effort: "low"}, Handler{})
	if err != nil {
		t.Fatal(err)
	}
	// Survive the session file.
	data, _ := json.Marshal(res.Message)
	var back Message
	json.Unmarshal(data, &back)
	if back.Reasoning == nil || back.ToolCalls[0].ID != "call_9|fc_9" {
		t.Fatalf("restored %s", data)
	}
	msgs := []Message{{Role: "user", Content: "go"}, back, {Role: "tool", ToolCallID: back.ToolCalls[0].ID, Content: "ok"}}
	if _, err := c.Stream(context.Background(), Request{Messages: msgs, Effort: "low"}, Handler{}); err != nil {
		t.Fatal(err)
	}
	b := (*bodies)[1]
	for _, want := range []string{`"encrypted_content":"ENC"`, `"id":"fc_9","call_id":"call_9"`, `"type":"function_call_output","call_id":"call_9","output":"ok"`} {
		if !strings.Contains(b, want) {
			t.Errorf("replay lacks %s: %s", want, b)
		}
	}
	// Another model's items are not replayed.
	c.Model.ID = "gpt-y"
	if _, err := c.Stream(context.Background(), Request{Messages: msgs, Effort: "low"}, Handler{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains((*bodies)[2], "ENC") || strings.Contains((*bodies)[2], `"id":"fc_9"`) {
		t.Fatalf("foreign items replayed: %s", (*bodies)[2])
	}
}

func TestClientErrorKeepsPartialText(t *testing.T) {
	srv, _, _ := serveSSE(t, `{"choices":[{"delta":{"content":"par"}}]}`)
	c := &Client{Model: ai.Model{ID: "m", Api: ai.ApiOpenAICompletions, Provider: "p", BaseURL: srv.URL}}
	res, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}}, Handler{})
	if err == nil || res.Message.Content != "par" {
		t.Fatalf("%v %+v", err, res)
	}
	c.KeyFunc = func(context.Context) (string, error) { return "", fmt.Errorf("not logged in") }
	if _, err := c.Stream(context.Background(), Request{}, Handler{}); err == nil || err.Error() != "not logged in" {
		t.Fatalf("key error %v", err)
	}
}

func TestImagesToParts(t *testing.T) {
	m := toUserMessage(Message{Role: "user", Content: "look", Images: []Image{{MIME: "image/png", Data: []byte{1}}, {MIME: "image/png"}}})
	if m.IsText() || len(m.Parts) != 3 || m.Parts[2].(*ai.TextContent).Text != ImageMissing {
		t.Fatalf("parts %+v", m.Parts)
	}
	if u := toUserMessage(Message{Role: "user", Content: "plain"}); !u.IsText() || u.Text != "plain" {
		t.Fatalf("text %+v", u)
	}
}

// A tool result's images (atto view) go in the function_call_output
// itself on the Responses API (Codex shares the conversion).
func TestToolResultImagesResponses(t *testing.T) {
	srv, bodies, _ := serveSSE(t, `{"type":"response.completed","response":{"status":"completed"}}`)
	c := &Client{Model: ai.Model{ID: "gpt-x", Api: ai.ApiOpenAIResponses, Provider: "openai", BaseURL: srv.URL, Input: []string{"text", "image"}}, APIKey: "sk-1"}
	msgs := []Message{
		{Role: "user", Content: "look"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: FunctionCall{Name: "bash", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "call_1", Content: "attached shot.png", Images: []Image{{MIME: "image/png", Data: []byte{1, 2}}}},
	}
	if _, err := c.Stream(context.Background(), Request{Messages: msgs}, Handler{}); err != nil {
		t.Fatal(err)
	}
	b := (*bodies)[0]
	for _, want := range []string{`"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"attached shot.png"}`, `"type":"input_image","detail":"auto","image_url":"data:image/png;base64,AQI="`} {
		if !strings.Contains(b, want) {
			t.Errorf("request lacks %s: %s", want, b)
		}
	}
}

// A server that takes only tool_choice "auto" gets the request again
// without it, and that model is sent none afterwards.
func TestClientDropsRefusedToolChoice(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if strings.Contains(string(b), `"tool_choice"`) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"only \"auto\" is supported for `+"`tool_choice`"+`","param":"tool_choice","type":"invalid_request_error"}}`)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"notes\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	t.Cleanup(srv.Close)
	c := &Client{Model: ai.Model{ID: "auto-only", Api: ai.ApiOpenAICompletions, Provider: "p-auto", BaseURL: srv.URL}}
	req := Request{ToolChoice: "none", Messages: []Message{{Role: "user", Content: "hi"}}}
	res, err := c.Stream(context.Background(), req, Handler{})
	if err != nil || res.Message.Content != "notes" || len(bodies) != 2 {
		t.Fatalf("res %+v err %v bodies %d", res.Message, err, len(bodies))
	}
	if _, err := c.Stream(context.Background(), req, Handler{}); err != nil || len(bodies) != 3 || strings.Contains(bodies[2], "tool_choice") {
		t.Fatalf("second request: err %v, %d bodies: %s", err, len(bodies), bodies[len(bodies)-1])
	}
}
