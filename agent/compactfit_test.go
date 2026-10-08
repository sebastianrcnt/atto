package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func msgs(roles ...string) []provider.Message {
	var out []provider.Message
	for i, r := range roles {
		out = append(out, provider.Message{Role: r, Content: strings.Repeat("x", 4000) + fmt.Sprint(i)})
	}
	return out
}

// The case from a real session: 232.4k tokens of conversation on a 262,144
// window whose maxTokens (32,768) no longer fit with it.
func TestFitCompactionLowersMaxTokens(t *testing.T) {
	m := config.Model{ContextWindow: 262144, MaxTokens: 32768}
	req := provider.Request{Messages: msgs("system", "user", "assistant", "user", "user"), MaxTokens: m.MaxTokens}
	if dropped, kept := fitCompaction(&req, m, 232400, compactRoom); dropped != 0 || len(kept) != 0 {
		t.Fatalf("dropped %d, kept %d with room to spare", dropped, len(kept))
	}
	if req.MaxTokens >= 32768 || req.MaxTokens < compactRoom || 232400+req.MaxTokens > 262144 {
		t.Fatalf("max tokens %d", req.MaxTokens)
	}
}

// With too little room left, whole oldest turns go, a tool call keeping its
// result, and the system prompt and the compaction prompt stay.
func TestFitCompactionDropsOldestTurns(t *testing.T) {
	m := config.Model{ContextWindow: 10000, MaxTokens: 4000}
	all := msgs("system", "user", "assistant", "tool", "user", "assistant", "user", "user")
	req := provider.Request{Messages: append([]provider.Message(nil), all...), MaxTokens: m.MaxTokens}
	dropped, kept := fitCompaction(&req, m, 8500, 3000)
	if dropped != 3 || len(kept) != 0 {
		t.Fatalf("dropped %d, kept %d", dropped, len(kept))
	}
	got := req.Messages
	if got[0].Role != "system" || got[1].Role != "user" || got[1].Content != all[4].Content || got[len(got)-1].Content != all[7].Content {
		t.Fatalf("kept %v", got)
	}
}

func TestContextExceeded(t *testing.T) {
	for _, s := range []string{
		"prompt (232400 tokens) + max tokens (32768) exceeds the context (262144); requests are never truncated",
		"This model's maximum context length is 128000 tokens",
		"context_length_exceeded",
	} {
		if !contextExceeded(errors.New(s)) {
			t.Errorf("not recognized: %q", s)
		}
	}
	if contextExceeded(errors.New("connection refused")) {
		t.Error("connection refused is not a context error")
	}
}

// A server that rejects a request too large for its window gets a smaller
// one: the compaction trims more and retries instead of failing for good.
func TestCompactionRetriesWhenTheServerSaysTooLong(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sizes = append(sizes, len(body.Messages))
		first := len(sizes) == 1
		mu.Unlock()
		if first {
			http.Error(w, `{"error":{"message":"prompt (9000 tokens) + max tokens (2000) exceeds the context (10000); requests are never truncated"}}`, http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"NOTES\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	a := New(config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: srv.URL},
		Model: config.Model{ID: "m", ContextWindow: 40000}}, "", os.TempDir())
	for range 6 {
		a.messages = append(a.messages,
			provider.Message{Role: "user", Content: strings.Repeat("u", 8000)},
			provider.Message{Role: "assistant", Content: strings.Repeat("a", 8000)})
	}
	a.LastUsage = provider.Usage{PromptTokens: 24000}
	var trimmed []int
	err := a.compact(context.Background(), func(ev any) {
		if e, ok := ev.(CompactTrimmed); ok {
			trimmed = append(trimmed, e.Messages)
		}
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 2 || sizes[1] >= sizes[0] || len(trimmed) == 0 {
		t.Fatalf("request sizes %v, trimmed %v", sizes, trimmed)
	}
}

// Notes that are cut off are written again, once; cut off twice, the
// compaction fails and the conversation stays.
func TestCompactionRewritesCutNotes(t *testing.T) {
	for _, c := range []struct {
		name    string
		answers []string // content, finish_reason
		fails   bool
	}{
		{"length, then whole", []string{"# Notes\n- a", "length", "# Notes\n- a\n- b", "stop"}, false},
		{"heading, then whole", []string{"# Notes\n## Remain", "stop", "# Notes\n## Remain\n- b", "stop"}, false},
		{"cut twice", []string{"# Notes\n- a", "length", "# Notes\n## Remain", "stop"}, true},
	} {
		var mu sync.Mutex
		n := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			i := n
			n++
			mu.Unlock()
			text, _ := json.Marshal(c.answers[2*i])
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", text, c.answers[2*i+1])
		}))
		a := New(config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: srv.URL},
			Model: config.Model{ID: "m", ContextWindow: 40000}}, "", os.TempDir())
		a.messages = []provider.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
		var saved []string
		a.Record = func(e session.Entry) { saved = append(saved, e.Notes+"|"+e.Finish) }
		err := a.compact(context.Background(), func(any) {}, true)
		srv.Close()
		switch {
		case n != 2:
			t.Errorf("%s: %d requests", c.name, n)
		case c.fails && (err == nil || !strings.Contains(err.Error(), "cut off") || !ai.IsPermanent(err) || len(a.messages) != 2 || len(saved) != 0):
			t.Errorf("%s: err %v, messages %d, saved %v", c.name, err, len(a.messages), saved)
		case !c.fails && (err != nil || len(saved) != 1 || saved[0] != c.answers[2]+"|stop"):
			t.Errorf("%s: err %v, saved %q", c.name, err, saved)
		}
	}
}

// The newest turn has two tool steps. Only the latest call (including all
// its results) should stay outside the summary request.
func oversizedToolHistory(results int) []provider.Message {
	call := func(ids ...string) provider.Message {
		m := provider.Message{Role: "assistant"}
		for _, id := range ids {
			m.ToolCalls = append(m.ToolCalls, provider.ToolCall{ID: id, Type: "function",
				Function: provider.FunctionCall{Name: "bash", Arguments: `{"command":"cat big.txt"}`}})
		}
		return m
	}
	out := []provider.Message{
		{Role: "user", Content: "old history"},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "read the large file"},
		call("earlier"),
		{Role: "tool", ToolCallID: "earlier", Content: "found big.txt"},
	}
	ids := []string{"latest"}
	if results == 2 {
		ids = append(ids, "other")
	}
	out = append(out, call(ids...))
	for _, id := range ids {
		out = append(out, provider.Message{Role: "tool", ToolCallID: id, Content: "small result"})
	}
	out[len(out)-1].Content = strings.Repeat("x", 24000)
	return out
}

func TestFitCompactionRetainsOversizedToolGroup(t *testing.T) {
	for _, results := range []int{1, 2} {
		t.Run(fmt.Sprint(results), func(t *testing.T) {
			m := config.Model{ContextWindow: 12000, MaxTokens: compactRoom}
			history := oversizedToolHistory(results)
			all := append([]provider.Message{{Role: "system", Content: "system"}}, history...)
			all = append(all, provider.Message{Role: "user", Content: "write notes"})
			req := provider.Request{Messages: all, MaxTokens: m.MaxTokens}
			est := 0
			for _, msg := range all {
				est += messageChars(msg) / 4
			}
			dropped, kept := fitCompaction(&req, m, est, compactRoom)
			if dropped != 0 || !reflect.DeepEqual(kept, history[5:]) {
				t.Fatalf("dropped %d, kept %v", dropped, kept)
			}
			want := append(append([]provider.Message(nil), all[:6]...), all[len(all)-1])
			if !reflect.DeepEqual(req.Messages, want) {
				t.Fatalf("summary request %v, want %v", req.Messages, want)
			}
			for _, msg := range kept {
				est -= messageChars(msg) / 4
			}
			if req.MaxTokens < compactRoom || est+req.MaxTokens+256 > m.ContextWindow {
				t.Fatalf("prompt %d + output %d exceeds window %d", est, req.MaxTokens, m.ContextWindow)
			}
			if !reflect.DeepEqual(all[1:len(all)-1], history) {
				t.Fatal("fitting mutated the original history")
			}
		})
	}
}

func TestFitCompactionKeepsFittingLastToolTurn(t *testing.T) {
	history := oversizedToolHistory(1)
	history[len(history)-1].Content = "small result"
	history[0].Content = strings.Repeat("x", 24000)
	all := append([]provider.Message{{Role: "system", Content: "system"}}, history...)
	all = append(all, provider.Message{Role: "user", Content: "write notes"})
	req := provider.Request{Messages: all, MaxTokens: compactRoom}
	est := 0
	for _, msg := range all {
		est += messageChars(msg) / 4
	}
	dropped, kept := fitCompaction(&req, config.Model{ContextWindow: 12000}, est, compactRoom)
	if dropped != 2 || len(kept) != 0 || !reflect.DeepEqual(req.Messages[1:len(req.Messages)-1], history[2:]) {
		t.Fatalf("dropped %d, kept %v, request %v", dropped, kept, req.Messages)
	}
}

func TestCompactionSummarizesPrefixOfOversizedToolTurn(t *testing.T) {
	for _, c := range []struct {
		name    string
		replies [][]string
		fails   bool
	}{
		{"whole notes", [][]string{text("notes")}, false},
		{"rewrite cut notes", [][]string{lengthReply(100), text("notes")}, false},
		{"cut twice", [][]string{lengthReply(100), lengthReply(100)}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			replies := append(append([][]string(nil), c.replies...), text("done"))
			srv, seen := fakeServer(t, replies...)
			a := newTestAgent(srv.URL)
			a.model.Model.ContextWindow, a.model.Model.MaxTokens = 12000, 1000
			a.system, a.sinceUsage = "system", len("system")
			history := oversizedToolHistory(2)
			for _, msg := range history {
				a.appendMessage(msg, session.Entry{})
			}
			var recorded []session.Entry
			a.Record = func(e session.Entry) { recorded = append(recorded, e) }
			assertFits := func() {
				var body struct {
					Messages            json.RawMessage `json:"messages"`
					Tools               json.RawMessage `json:"tools"`
					MaxTokens           int             `json:"max_tokens"`
					MaxCompletionTokens int             `json:"max_completion_tokens"`
				}
				if err := json.Unmarshal(a.LastRequest(), &body); err != nil {
					t.Fatal(err)
				}
				output := max(body.MaxTokens, body.MaxCompletionTokens)
				if output <= 0 || (len(body.Messages)+len(body.Tools))/4+output+256 > 12000 {
					t.Fatalf("provider request does not fit: prompt bytes %d, output %d", len(body.Messages)+len(body.Tools), output)
				}
			}
			err := a.compact(context.Background(), func(any) {}, true)
			if (err != nil) != c.fails || len(seen()) != len(c.replies) {
				t.Fatalf("err %v, requests %d", err, len(seen()))
			}
			assertFits()
			for _, req := range seen() {
				if len(req) != 7 || req[1]["content"] != "old history" || req[3]["content"] != "read the large file" || req[5]["tool_call_id"] != "earlier" {
					t.Fatalf("did not summarize the earlier prefix: %v", req)
				}
			}
			if c.fails {
				if !reflect.DeepEqual(a.messages, history) || len(recorded) != 0 {
					t.Fatal("failed compaction changed or recorded the conversation")
				}
				return
			}
			want := []provider.Message{history[0], history[2], {Role: "user", Content: SummaryPrefix + "notes"}}
			want = append(want, history[5:]...)
			if !reflect.DeepEqual(a.messages, want) || len(recorded) != 1 || !reflect.DeepEqual(recorded[0].Replacement, want) {
				t.Fatalf("compaction did not retain the tool-call/result group: %v", a.messages)
			}
			restored := newTestAgent(srv.URL)
			restored.Restore(recorded)
			if !reflect.DeepEqual(restored.messages, want) {
				t.Fatal("restored compaction lost the tool-call/result group")
			}
			if err := a.Continue(context.Background(), func(any) {}); err != nil {
				t.Fatal(err)
			}
			assertFits()
			last := seen()[len(c.replies)]
			// Providers may merge consecutive user messages; the trailing
			// assistant call and both tool results must still be separate.
			if len(last) < 4 || last[len(last)-3]["role"] != "assistant" ||
				last[len(last)-2]["tool_call_id"] != "latest" ||
				last[len(last)-1]["tool_call_id"] != "other" ||
				last[len(last)-1]["content"] != history[len(history)-1].Content {
				t.Fatal("the continuation did not receive the retained tool-call/result group")
			}
			calls, ok := last[len(last)-3]["tool_calls"].([]any)
			if !ok || len(calls) != 2 || calls[0].(map[string]any)["id"] != "latest" || calls[1].(map[string]any)["id"] != "other" {
				t.Fatalf("retained tool results lost their calls: %v", calls)
			}
		})
	}
}

func TestCompactionRetainsToolGroupsAcrossFitRetries(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(fmt.Sprint("fails=", fails), func(t *testing.T) {
			reply := sse(text("notes")...)
			if fails {
				reply = status(401, "invalid api key")
			}
			srv, count := scriptedServer(t,
				status(400, "This model's maximum context length is 1000 tokens"), reply)
			a := newTestAgent(srv.URL)
			a.model.Model.ContextWindow, a.model.Model.MaxTokens = 12000, 1000
			a.system, a.sinceUsage = "system", len("system")
			history := oversizedToolHistory(2)
			for _, msg := range history {
				a.appendMessage(msg, session.Entry{})
			}
			err := a.compact(context.Background(), func(any) {}, true)
			if (err != nil) != fails || count() != 2 {
				t.Fatalf("err %v, requests %d", err, count())
			}
			if fails {
				if !reflect.DeepEqual(a.messages, history) {
					t.Fatal("failed compaction changed the conversation")
				}
				return
			}
			// The second fit needs more room and retains the earlier tool
			// group too. Both groups must stay in chronological order.
			want := []provider.Message{history[0], history[2], {Role: "user", Content: SummaryPrefix + "notes"}}
			want = append(want, history[3:]...)
			if !reflect.DeepEqual(a.messages, want) {
				t.Fatal("fit retries lost or reordered a retained tool group")
			}
		})
	}
}

func TestFitCompactionDoesNotRetainToolsWhenPrefixCannotFit(t *testing.T) {
	history := oversizedToolHistory(1)
	history[2].Content = strings.Repeat("u", 24000)
	all := append([]provider.Message{{Role: "system", Content: "system"}}, history...)
	all = append(all, provider.Message{Role: "user", Content: "write notes"})
	req := provider.Request{Messages: all, MaxTokens: 2000}
	est := 0
	for _, msg := range all {
		est += messageChars(msg) / 4
	}
	dropped, kept := fitCompaction(&req, config.Model{ContextWindow: 4000}, est, compactRoom)
	if dropped != 2 || len(kept) != 0 || !reflect.DeepEqual(req.Messages[1:len(req.Messages)-1], history[2:]) {
		t.Fatal("retained tools even though the turn prefix cannot fit")
	}
}
