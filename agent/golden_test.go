package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/shell"
)

// The golden tests pin the exact request bodies atto sends for
// representative histories. The prefix cache of a model server only
// survives when the bytes of earlier turns never change, so any difference
// here must be deliberate. Regenerate with: go test ./agent -run Golden -update
var updateGolden = flag.Bool("update", false, "rewrite golden request bodies")

type goldenCase struct {
	name    string
	ref     func(url string) config.ModelRef
	effort  string
	history []provider.Message
	compact bool
}

// goldenServer records each request body and answers with a minimal
// stream in the API the path asks for.
func goldenServer(t *testing.T) (*httptest.Server, func() ([]byte, http.Header)) {
	var mu sync.Mutex
	var body []byte
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		if body == nil {
			body, hdr = b, r.Header.Clone()
		}
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/responses") {
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"ok\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() ([]byte, http.Header) { mu.Lock(); defer mu.Unlock(); return body, hdr }
}

// llamaRef is a local llama.cpp server configured the atto way: effort
// travels in chat_template_kwargs through extraBody placeholders.
func llamaRef(images bool) func(string) config.ModelRef {
	return func(url string) config.ModelRef {
		m := config.Model{
			ID: "orca-local", Efforts: []string{"off", "low", "medium", "high"},
			ContextWindow: 262144, MaxTokens: 32768,
		}
		if images {
			m.Input = []string{"text", "image"}
		}
		return config.ModelRef{
			ProviderName: "llama-cpp",
			Provider: config.Provider{
				BaseURL: url + "/v1", API: "openai-completions",
				ExtraBody: map[string]any{"chat_template_kwargs": map[string]any{"reasoning_effort": "$effort", "enable_thinking": "$thinking"}},
			},
			Model: m,
		}
	}
}

// opencodeRef mirrors what the catalog builds for an OpenCode Go model.
func opencodeRef(url string) config.ModelRef {
	return config.ModelRef{
		ProviderName: "opencode-go",
		Provider: config.Provider{
			Name: "OpenCode Go", BaseURL: url + "/zen/go/v1", API: "openai-completions",
			MaxTokensField: "max_tokens", Headers: map[string]string{"x-opencode-session": "$session"},
		},
		Model: config.Model{
			ID: "glm-5.3-flash", Efforts: []string{"off", "low", "medium", "high"},
			EffortMap: map[string]*string{"off": new("none")}, ExtraBody: map[string]any{"reasoning_effort": "$effort"},
			ContextWindow: 200000, MaxTokens: 32768,
		},
	}
}

func kimiRef(url string) config.ModelRef {
	ref := opencodeRef(url)
	ref.Model = config.Model{
		ID: "kimi-k2.6", Efforts: []string{"off", "on"}, EffortMap: map[string]*string{},
		ExtraBody:     map[string]any{"reasoning_effort": nil, "thinking": map[string]any{"type": "$thinkingType"}},
		ContextWindow: 262144, MaxTokens: 32768,
	}
	return ref
}

func responsesRef(images bool) func(string) config.ModelRef {
	return func(url string) config.ModelRef {
		m := config.Model{
			ID: "gpt-5.2", Efforts: []string{"off", "low", "medium", "high", "xhigh"},
			EffortMap: map[string]*string{"off": new("none")}, ContextWindow: 400000, MaxTokens: 32768,
		}
		if images {
			m.Input = []string{"text", "image"}
		}
		return config.ModelRef{
			ProviderName: "openai",
			Provider: config.Provider{
				Name: "OpenAI", BaseURL: url + "/v1", API: "openai-responses",
				Headers: map[string]string{"session_id": "$session", "x-client-request-id": "$session"},
			},
			Model: m,
		}
	}
}

func bashCall(id, cmd, desc string) provider.ToolCall {
	// Spacing and key order as a model might emit them: replays must keep
	// these bytes.
	return provider.ToolCall{ID: id, Type: "function", Function: provider.FunctionCall{
		Name: "bash", Arguments: fmt.Sprintf(`{"description": %q, "command": %q}`, desc, cmd),
	}}
}

var (
	histText = []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "Hi! What can I do?"},
	}
	histTools = []provider.Message{
		{Role: "user", Content: "list the files"},
		{Role: "assistant", ReasoningContent: "I should run ls.", ToolCalls: []provider.ToolCall{bashCall("call_1", "ls", "List files")}},
		{Role: "tool", ToolCallID: "call_1", Content: "a.txt\nb.txt\n<exit code 0>"},
		{Role: "assistant", Content: "There are two files.", ReasoningContent: "Two results."},
		{Role: "user", Content: "show both & compare <them>"},
		{Role: "assistant", Content: "Reading them.", ToolCalls: []provider.ToolCall{
			bashCall("call_2", "cat a.txt", "Read a.txt"), bashCall("call_3", "cat b.txt", "Read b.txt"),
		}},
		{Role: "tool", ToolCallID: "call_2", Content: "alpha"},
		{Role: "tool", ToolCallID: "call_3", Content: "beta"},
		{Role: "assistant", Content: "They differ."},
	}
	histImages = []provider.Message{
		{Role: "user", Content: "what is in this picture?", Images: []provider.Image{
			{File: "aa.png", MIME: "image/png", Width: 1, Height: 1, Data: []byte("\x89PNG fake")},
			{File: "gone.png", MIME: "image/png"}, // file missing: no data
		}},
		{Role: "assistant", Content: "A tiny image."},
		{Role: "user", Images: []provider.Image{{File: "bb.jpg", MIME: "image/jpeg", Data: []byte("jpeg")}}},
		{Role: "assistant", Content: "Another one."},
	}
	histResponses = []provider.Message{
		{Role: "user", Content: "list the files"},
		{
			Role: "assistant", ReasoningContent: "**Listing**\n\nRun ls.",
			Reasoning: &provider.ReasoningState{Model: "gpt-5.2", Items: []json.RawMessage{
				json.RawMessage(`{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"**Listing**\n\nRun ls."}],"encrypted_content":"ENC1"}`),
			}},
			ToolCalls: []provider.ToolCall{bashCall("call_1", "ls", "List files")},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "a.txt"},
		{
			Role: "assistant", Content: "One file.",
			Reasoning: &provider.ReasoningState{Model: "gpt-4.1", Items: []json.RawMessage{
				json.RawMessage(`{"id":"rs_2","type":"reasoning","summary":[],"encrypted_content":"OTHER"}`),
			}},
		},
		{Role: "user", Content: "thanks"},
		{Role: "assistant"}, // an empty reply (e.g. cancelled before any output)
	}
)

var goldenCases = []goldenCase{
	{name: "llama_text_medium", ref: llamaRef(false), effort: "medium", history: histText},
	{name: "llama_tools_off", ref: llamaRef(false), effort: "off", history: histTools},
	{name: "llama_images", ref: llamaRef(true), effort: "high", history: histImages},
	{name: "llama_images_unsupported", ref: llamaRef(false), effort: "high", history: histImages},
	{name: "llama_compact", ref: llamaRef(false), effort: "low", history: histTools, compact: true},
	{name: "opencode_off", ref: opencodeRef, effort: "off", history: histTools},
	{name: "opencode_high", ref: opencodeRef, effort: "high", history: histText},
	{name: "kimi_on", ref: kimiRef, effort: "on", history: histText},
	{name: "kimi_off", ref: kimiRef, effort: "off", history: histText},
	{name: "responses_tools_high", ref: responsesRef(false), effort: "high", history: histResponses},
	{name: "responses_text_off", ref: responsesRef(false), effort: "off", history: histText},
	{name: "responses_images", ref: responsesRef(true), effort: "low", history: histImages},
	{name: "responses_compact", ref: responsesRef(false), effort: "medium", history: histTools, compact: true},
}

func TestGoldenRequestBodies(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	for _, c := range goldenCases {
		t.Run(c.name, func(t *testing.T) {
			srv, got := goldenServer(t)
			ref := c.ref(srv.URL)
			ref.APIKey = "test-key"
			a := New(ref, c.effort, "/work")
			a.Shell = shell.Shell{Kind: shell.Bash, Path: "/bin/bash"}
			a.system = "You are atto. (golden test system prompt)"
			a.SetSession("sess-0123456789", nil)
			a.messages = append([]provider.Message(nil), c.history...)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var err error
			if c.compact {
				err = a.Compact(ctx, func(any) {})
			} else {
				err = a.Run(ctx, "next step", func(any) {})
			}
			if err != nil {
				t.Fatal(err)
			}
			body, hdr := got()
			path := filepath.Join("testdata", "golden", c.name+".json")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create)", err)
			}
			if !bytes.Equal(body, want) {
				t.Errorf("request body changed\n got: %s\nwant: %s", body, want)
			}
			if h := hdr.Get("Authorization"); h != "Bearer test-key" {
				t.Errorf("Authorization = %q", h)
			}
			// Session routing headers keep turns on one server's cache.
			wantHdr := map[string]string{}
			switch ref.ProviderName {
			case "opencode-go":
				wantHdr["x-opencode-session"] = "sess-0123456789"
			case "openai":
				wantHdr["session_id"] = "sess-0123456789"
				wantHdr["x-client-request-id"] = "sess-0123456789"
			}
			for k, v := range wantHdr {
				if got := hdr.Get(k); got != v {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}
		})
	}
}
