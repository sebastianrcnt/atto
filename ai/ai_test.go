package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// sse serves the given data lines as one SSE response and records the
// request.
type fakeServer struct {
	*httptest.Server
	mu      sync.Mutex
	bodies  []map[string]any
	raw     []string
	headers []http.Header
}

func serve(t *testing.T, status int, lines ...string) *fakeServer {
	f := &fakeServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.raw = append(f.raw, string(b))
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		if status != 200 {
			http.Error(w, lines[0], status)
			return
		}
		for _, l := range lines {
			fmt.Fprintf(w, "data: %s\n\n", l)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) body(i int) map[string]any { f.mu.Lock(); defer f.mu.Unlock(); return f.bodies[i] }
func (f *fakeServer) header(i int) http.Header  { f.mu.Lock(); defer f.mu.Unlock(); return f.headers[i] }

func collect(s *AssistantMessageEventStream) ([]string, *AssistantMessage) {
	var types []string
	for ev := range s.All() {
		types = append(types, ev.Type)
	}
	return types, s.Result()
}

func ctxWithUser(text string) Context {
	return Context{SystemPrompt: "sys", Messages: []Message{&UserMessage{Role: "user", Text: text}}}
}

func TestCompletionsEventsReasoningToolCallsUsage(t *testing.T) {
	srv := serve(t, 200,
		`{"id":"c1","choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"delta":{"content":"Hi "}}]}`,
		`{"choices":[{"delta":{"content":"there"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"bash","arguments":"{\"command\": "}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2}},"timings":{"cache_n":7}}`,
		`[DONE]`)
	model := &Model{ID: "m", Api: ApiOpenAICompletions, Provider: "llama", BaseURL: srv.URL, Input: []string{"text"}}
	types, msg := collect(StreamSimple(model, ctxWithUser("hi"), &SimpleStreamOptions{}))
	want := "start thinking_start thinking_delta text_start text_delta text_delta toolcall_start toolcall_delta toolcall_delta thinking_end text_end toolcall_end done"
	if strings.Join(types, " ") != want {
		t.Fatalf("events\n got %s\nwant %s", strings.Join(types, " "), want)
	}
	if msg.StopReason != StopToolUse || msg.ResponseID != "c1" {
		t.Fatalf("stop %s id %s", msg.StopReason, msg.ResponseID)
	}
	th := msg.Content[0].(*ThinkingContent)
	tx := msg.Content[1].(*TextContent)
	tc := msg.Content[2].(*ToolCall)
	if th.Thinking != "think" || th.ThinkingSignature != "reasoning_content" || tx.Text != "Hi there" {
		t.Fatalf("content %+v %+v", th, tx)
	}
	if tc.ID != "call_a" || tc.Name != "bash" || tc.Arguments["command"] != "ls" || tc.RawArguments != `{"command": "ls"}` {
		t.Fatalf("tool call %+v", tc)
	}
	// llama.cpp's cache_n (7) beats the reported cached_tokens (2).
	if u := msg.Usage; u.Input != 3 || u.CacheRead != 7 || u.Output != 5 || u.TotalTokens != 15 {
		t.Fatalf("usage %+v", u)
	}
	b := srv.body(0)
	if b["max_completion_tokens"] != nil || b["store"] != false || b["messages"].([]any)[0].(map[string]any)["role"] != "system" {
		t.Fatalf("detected compat body %v", b)
	}
}

func TestCompletionsThinkingFormatsAndExtraBody(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name   string
		model  Model
		level  string
		checks map[string]any
	}{
		{"openai effort mapped", Model{Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{"high": str("max")}}, "high", map[string]any{"reasoning_effort": "max"}},
		{"openai off mapped", Model{Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{"off": str("none")}}, "off", map[string]any{"reasoning_effort": "none"}},
		{"deepseek", Model{Reasoning: true, Compat: &Compat{ThinkingFormat: "deepseek"}}, "off", map[string]any{"thinking": map[string]any{"type": "disabled"}}},
		{"chat-template", Model{Reasoning: true, Compat: &Compat{ThinkingFormat: "chat-template", ChatTemplateKwargs: map[string]any{
			"enable_thinking": map[string]any{"$var": "thinking.enabled"}, "reasoning_effort": map[string]any{"$var": "thinking.effort"},
		}}}, "low", map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true, "reasoning_effort": "low"}}},
		{"extraBody placeholders", Model{Efforts: []string{"off", "on"}, ExtraBody: map[string]any{
			"thinking": map[string]any{"type": "$thinkingType"}, "e": "$effort", "t": "$thinking",
		}}, "on", map[string]any{"thinking": map[string]any{"type": "enabled"}, "e": "on", "t": true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := serve(t, 200, `{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}]}`)
			m := c.model
			m.ID, m.Api, m.Provider, m.BaseURL = "m", ApiOpenAICompletions, "p", srv.URL
			_, msg := collect(StreamSimple(&m, ctxWithUser("hi"), &SimpleStreamOptions{Reasoning: c.level}))
			if msg.StopReason != StopStop {
				t.Fatalf("%s: %s", msg.StopReason, msg.ErrorMessage)
			}
			b := srv.body(0)
			for k, v := range c.checks {
				got, _ := json.Marshal(b[k])
				want, _ := json.Marshal(v)
				if string(got) != string(want) {
					t.Errorf("%s = %s, want %s", k, got, want)
				}
			}
		})
	}
}

func TestCompletionsErrors(t *testing.T) {
	model := func(url string) *Model {
		return &Model{ID: "m", Api: ApiOpenAICompletions, Provider: "p", BaseURL: url}
	}
	srv := serve(t, 429, `{"error":{"message":"slow down"}}`)
	_, msg := collect(StreamSimple(model(srv.URL), ctxWithUser("x"), nil))
	if msg.StopReason != StopError || !strings.Contains(msg.ErrorMessage, "429") || !strings.Contains(msg.ErrorMessage, "slow down") {
		t.Fatalf("http error: %+v", msg)
	}
	srv = serve(t, 200, `{"error":{"message":"boom"}}`)
	if _, msg = collect(StreamSimple(model(srv.URL), ctxWithUser("x"), nil)); msg.ErrorMessage != "boom" {
		t.Fatalf("stream error: %q", msg.ErrorMessage)
	}
	srv = serve(t, 200, `{"choices":[{"delta":{"content":"partial"}}]}`)
	if _, msg = collect(StreamSimple(model(srv.URL), ctxWithUser("x"), nil)); msg.ErrorMessage != "Stream ended without finish_reason" || msg.Content[0].(*TextContent).Text != "partial" {
		t.Fatalf("truncated stream: %+v", msg)
	}
	// Cancellation reports aborted.
	block := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer block.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s := StreamSimple(model(block.URL), ctxWithUser("x"), &SimpleStreamOptions{Context: ctx})
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, msg = collect(s); msg.StopReason != StopAborted {
		t.Fatalf("cancel: %+v", msg)
	}
}

func TestResponsesStreamAndBody(t *testing.T) {
	srv := serve(t, 200,
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"plan"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"ENC"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":\"ls\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":64}}}}`)
	off := "none"
	model := &Model{ID: "gpt-5.2", Api: ApiOpenAIResponses, Provider: "openai", BaseURL: srv.URL, Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{"off": &off}, Input: []string{"text"}, MaxTokens: 8}
	types, msg := collect(StreamSimple(model, ctxWithUser("hi"), &SimpleStreamOptions{
		APIKey: "sk-x", SessionID: "sess", Reasoning: "high",
	}))
	if got := strings.Join(types, " "); got != "start thinking_start thinking_delta thinking_end toolcall_start toolcall_delta toolcall_end done" {
		t.Fatalf("events %s", got)
	}
	if msg.StopReason != StopToolUse || msg.ResponseID != "resp_1" || msg.Usage.Input != 36 || msg.Usage.CacheRead != 64 {
		t.Fatalf("msg %+v", msg)
	}
	th := msg.Content[0].(*ThinkingContent)
	if th.Thinking != "plan" || !strings.Contains(th.ThinkingSignature, `"encrypted_content":"ENC"`) {
		t.Fatalf("thinking %+v", th)
	}
	if tc := msg.Content[1].(*ToolCall); tc.ID != "call_1|fc_1" || tc.RawArguments != `{"command":"ls"}` {
		t.Fatalf("tool call %+v", tc)
	}
	b := srv.body(0)
	r, _ := json.Marshal(b["reasoning"])
	if string(r) != `{"effort":"high","summary":"auto"}` || b["max_output_tokens"] != float64(16) || b["prompt_cache_key"] != "sess" || b["store"] != false {
		t.Fatalf("body %v", b)
	}
	if h := srv.header(0); h.Get("session_id") != "sess" || h.Get("x-client-request-id") != "sess" || h.Get("Authorization") != "Bearer sk-x" {
		t.Fatalf("headers %v", h)
	}

	// Replaying the reply sends the reasoning item verbatim and pairs the
	// function call with its fc_ id.
	srv2 := serve(t, 200, `{"type":"response.completed","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`)
	model.BaseURL = srv2.URL
	c := ctxWithUser("hi")
	c.Messages = append(c.Messages, msg, &ToolResultMessage{Role: "toolResult", ToolCallID: "call_1|fc_1", ToolName: "bash", Content: []Content{NewText("out")}})
	_, msg2 := collect(StreamSimple(model, c, &SimpleStreamOptions{APIKey: "chatgpt-token", Reasoning: "off"}))
	if msg2.StopReason != StopLength {
		t.Fatalf("incomplete: %+v", msg2)
	}
	srv2.mu.Lock()
	in := srv2.raw[0]
	srv2.mu.Unlock()
	for _, want := range []string{`"role":"developer","content":"sys"`, `"encrypted_content":"ENC"`, `"type":"function_call","id":"fc_1","call_id":"call_1"`, `"type":"function_call_output","call_id":"call_1","output":"out"`} {
		if !strings.Contains(in, want) {
			t.Errorf("input lacks %s: %s", want, in)
		}
	}
	b2 := srv2.body(0)
	if r, _ := json.Marshal(b2["reasoning"]); string(r) != `{"effort":"none"}` {
		t.Errorf("off reasoning %s", r)
	}
	// Model.BaseURL is not api.openai.com, so this is not ChatGPT sign-in.
	if b2["max_output_tokens"] == nil {
		t.Errorf("max_output_tokens dropped: %v", b2)
	}
	model.BaseURL = "https://api.openai.com/v1"
	if p := buildResponsesParams(model, NormalizeContext(ctxWithUser("x")), &OpenAIResponsesOptions{APIKey: "oauth-token", MaxTokens: 100}, GetResponsesCompat(model)); p["max_output_tokens"] != nil {
		t.Errorf("sign-in sent max_output_tokens")
	}
}

func TestResponsesFailures(t *testing.T) {
	for name, line := range map[string]string{
		"failed": `{"type":"response.failed","response":{"error":{"code":"server_error","message":"overloaded"}}}`,
		"error":  `{"type":"error","code":"rate_limit","message":"slow"}`,
	} {
		srv := serve(t, 200, line)
		model := &Model{ID: "m", Api: ApiOpenAIResponses, Provider: "x", BaseURL: srv.URL}
		if _, msg := collect(StreamSimple(model, ctxWithUser("x"), nil)); msg.StopReason != StopError || msg.ErrorMessage == "" {
			t.Errorf("%s: %+v", name, msg)
		}
	}
	srv := serve(t, 200, `{"type":"response.output_text.delta","output_index":0,"delta":"x"}`)
	model := &Model{ID: "m", Api: ApiOpenAIResponses, Provider: "x", BaseURL: srv.URL}
	if _, msg := collect(StreamSimple(model, ctxWithUser("x"), nil)); !strings.Contains(msg.ErrorMessage, "terminal response event") {
		t.Errorf("unterminated: %+v", msg)
	}
	srv = serve(t, 400, `{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`)
	model = &Model{ID: "m", Api: ApiOpenAIResponses, Provider: "openai", BaseURL: srv.URL}
	if _, msg := collect(StreamSimple(model, ctxWithUser("x"), nil)); !strings.Contains(msg.ErrorMessage, "chatgpt.com/settings/usage") {
		t.Errorf("usage hint: %q", msg.ErrorMessage)
	}
}

func fakeJWT(accountID string) string {
	payload, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID}})
	return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

func TestCodexBodyAndHeaders(t *testing.T) {
	srv := serve(t, 200,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1"}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"ok"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","phase":"final_answer","content":[{"type":"output_text","text":"ok"}]}}`,
		`{"type":"response.done","response":{"status":"completed","end_turn":true}}`)
	model := &Model{ID: "gpt-5.3-codex", Api: ApiOpenAICodexResponses, Provider: "openai-codex", BaseURL: srv.URL, Reasoning: true}
	c := ctxWithUser("hi")
	c.Tools = []Tool{{Name: "bash", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}}
	_, msg := collect(StreamSimple(model, c, &SimpleStreamOptions{APIKey: fakeJWT("acct-1"), SessionID: "s1", Reasoning: "medium"}))
	if msg.StopReason != StopStop || msg.EndTurn == nil || !*msg.EndTurn || msg.Content[0].(*TextContent).TextSignature != `{"v":1,"id":"msg_1","phase":"final_answer"}` {
		t.Fatalf("msg %+v", msg)
	}
	b := srv.body(0)
	tools, _ := json.Marshal(b["tools"])
	if b["instructions"] != "sys" || b["tool_choice"] != "auto" || b["parallel_tool_calls"] != true || b["prompt_cache_key"] != "s1" ||
		!strings.Contains(string(tools), `"strict":null`) {
		t.Fatalf("body %v", b)
	}
	if in, _ := json.Marshal(b["input"]); strings.Contains(string(in), "developer") {
		t.Fatalf("system prompt sent as input: %s", in)
	}
	h := srv.header(0)
	if h.Get("chatgpt-account-id") != "acct-1" || h.Get("OpenAI-Beta") != "responses=experimental" || h.Get("session-id") != "s1" {
		t.Fatalf("headers %v", h)
	}
	if !strings.HasSuffix(resolveCodexURL(srv.URL), "/codex/responses") || resolveCodexURL("https://x/backend-api/codex") != "https://x/backend-api/codex/responses" {
		t.Fatal("codex url")
	}
	if _, msg := collect(StreamSimple(model, c, &SimpleStreamOptions{APIKey: "not-a-jwt"})); !strings.Contains(msg.ErrorMessage, "accountId") {
		t.Fatalf("bad token: %+v", msg)
	}
}

func TestTransformMessagesOrphansAndCrossModel(t *testing.T) {
	target := &Model{ID: "b", Api: ApiOpenAICompletions, Provider: "p"}
	other := &AssistantMessage{Role: "assistant", Provider: "p", Api: ApiOpenAICompletions, Model: "a", StopReason: StopToolUse,
		Content: []Content{&ThinkingContent{Type: "thinking", Thinking: "secret plan", ThinkingSignature: "reasoning_content"}, NewToolCall("x|y", "bash", nil)}}
	out := TransformMessages([]Message{other, &UserMessage{Role: "user", Text: "next"}}, target, func(id string, _ *Model, _ *AssistantMessage) string { return "norm" })
	am := out[0].(*AssistantMessage)
	if t0, ok := am.Content[0].(*TextContent); !ok || t0.Text != "secret plan" {
		t.Fatalf("cross-model thinking not converted: %#v", am.Content[0])
	}
	if am.Content[1].(*ToolCall).ID != "norm" {
		t.Fatal("tool call id not normalized")
	}
	if tr, ok := out[1].(*ToolResultMessage); !ok || tr.ToolCallID != "norm" || !tr.IsError {
		t.Fatalf("orphan not closed: %#v", out[1])
	}
	errored := &AssistantMessage{Role: "assistant", StopReason: StopError, Content: []Content{NewText("half")}}
	if out := TransformMessages([]Message{errored}, target, nil); len(out) != 0 {
		t.Fatalf("errored turn replayed: %v", out)
	}
}

func TestThinkingLevelsAndClamp(t *testing.T) {
	x := "xhigh"
	m := &Model{Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{"minimal": nil, "xhigh": &x}}
	if got := strings.Join(GetSupportedThinkingLevels(m), ","); got != "off,low,medium,high,xhigh" {
		t.Fatalf("levels %s", got)
	}
	if ClampThinkingLevel(m, "minimal") != "low" || ClampThinkingLevel(m, "max") != "xhigh" {
		t.Fatal("clamp")
	}
	if got := GetSupportedThinkingLevels(&Model{}); len(got) != 1 || got[0] != "off" {
		t.Fatalf("non-reasoning %v", got)
	}
	if ClampThinkingLevel(&Model{Efforts: []string{"off", "on"}}, "on") != "on" {
		t.Fatal("atto efforts")
	}
}

func TestParseStreamingJSON(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": 1, "b": "x`:   `{"a":1,"b":"x"}`,
		`{"a": [1, 2`:        `{"a":[1,2]}`,
		`{"a": 1, "b`:        `{"a":1}`,
		`{"a": "line` + "\n": `{"a":"line\n"}`,
		``:                   `{}`,
	} {
		got, _ := json.Marshal(ParseStreamingJSON(in))
		if string(got) != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestShortHashMatchesPi(t *testing.T) {
	// Values computed with pi's src/utils/hash.ts.
	for in, want := range map[string]string{"hello": "1h6qa0qrowduu", "call_abc|fc_ü𝄞": "1o077ahisbeuq"} {
		if got := ShortHash(in); got != want {
			t.Errorf("ShortHash(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRegistryOverride(t *testing.T) {
	defer ResetApiProviders()
	called := false
	RegisterApiProvider(ApiProvider{Api: "custom", Stream: func(*Model, TranscriptContext, any) *AssistantMessageEventStream { return nil },
		StreamSimple: func(m *Model, c TranscriptContext, o *SimpleStreamOptions) *AssistantMessageEventStream {
			called = true
			s := NewAssistantMessageEventStream()
			out := newAssistantOutput(m, m.Api)
			out.StopReason = StopStop
			s.Push(AssistantMessageEvent{Type: EventDone, Reason: StopStop, Message: out})
			return s
		}}, "test")
	msg := CompleteSimple(&Model{ID: "m", Api: "custom"}, ctxWithUser("x"), nil)
	if !called || msg.StopReason != StopStop {
		t.Fatal("custom api not used")
	}
	UnregisterApiProviders("test")
	if GetApiProvider("custom") != nil {
		t.Fatal("not unregistered")
	}
	if msg := CompleteSimple(&Model{ID: "m", Api: "nope"}, ctxWithUser("x"), nil); !strings.Contains(msg.ErrorMessage, "No API provider") {
		t.Fatalf("missing api: %+v", msg)
	}
}

func TestOpenCodeSessionHeader(t *testing.T) {
	srv := serve(t, 200, `{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}]}`)
	model := &Model{ID: "glm", Api: ApiOpenAICompletions, Provider: "opencode-go", BaseURL: srv.URL}
	collect(StreamSimple(model, ctxWithUser("x"), &SimpleStreamOptions{SessionID: "abc"}))
	if srv.header(0).Get("x-opencode-session") != "abc" {
		t.Fatalf("headers %v", srv.header(0))
	}
}
