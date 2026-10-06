package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
)

// fakeServer replies with the given SSE chunks per request, in order, and
// records request message lists.
func fakeServer(t *testing.T, replies ...[]string) (*httptest.Server, func() [][]map[string]any) {
	var mu sync.Mutex
	var seen [][]map[string]any
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body.Messages)
		i := n
		n++
		mu.Unlock()
		if i >= len(replies) {
			t.Errorf("unexpected request %d", i)
			return
		}
		for _, c := range replies[i] {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]map[string]any { mu.Lock(); defer mu.Unlock(); return seen }
}

func toolCall(cmd string) []string { return toolCallFinish(cmd, "tool_calls") }

func toolCallFinish(cmd, reason string) []string {
	args, _ := json.Marshal(map[string]string{"description": "test", "command": cmd})
	a, _ := json.Marshal(string(args))
	return []string{
		fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":%s}}]},"finish_reason":%q}]}`, a, reason),
	}
}

func text(s string) []string {
	return []string{fmt.Sprintf(`{"choices":[{"delta":{"content":%q},"finish_reason":"stop"}]}`, s)}
}

func newTestAgent(url string) *Agent {
	return New(config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: url}, Model: config.Model{ID: "m"}}, "", os.TempDir())
}

func TestRequestUsesOneModelSnapshot(t *testing.T) {
	one := config.ModelRef{ProviderName: "p", Provider: config.Provider{BaseURL: "http://one"}, Model: config.Model{ID: "one", MaxTokens: 100}}
	two := config.ModelRef{ProviderName: "p", Provider: config.Provider{BaseURL: "http://two"}, Model: config.Model{ID: "two", MaxTokens: 200}}
	a := New(one, "", os.TempDir())

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50000 {
			a.SetModel(two)
			a.SetModel(one)
		}
	}()

	for {
		streamer, req := a.request()
		client, ok := streamer.(*provider.Client)
		if !ok {
			t.Fatalf("streamer %T, want *provider.Client", streamer)
		}
		if req.Model != client.Model.ID {
			t.Fatalf("mixed request snapshot: request model %q, client model %q", req.Model, client.Model.ID)
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

func TestLengthTruncatedToolCallDoesNotRun(t *testing.T) {
	srv, seen := fakeServer(t, toolCallFinish("echo should-not-run", "length"), text("done"))
	a := newTestAgent(srv.URL)
	starts := 0
	if err := a.Run(context.Background(), "go", func(ev any) {
		if _, ok := ev.(ToolStart); ok {
			starts++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if starts != 0 {
		t.Fatalf("truncated tool call started %d times", starts)
	}
	reqs := seen()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want the model to get one retry step", len(reqs))
	}
	found := false
	for _, m := range reqs[1] {
		if m["role"] == "tool" && strings.Contains(fmt.Sprint(m["content"]), "output token limit") {
			found = true
		}
	}
	if !found {
		t.Fatalf("second request has no truncation tool result: %v", reqs[1])
	}
}

func TestSteerDeliveredAfterToolCall(t *testing.T) {
	srv, seen := fakeServer(t, toolCall(sleepCmd), text("done"))
	a := newTestAgent(srv.URL)
	var committed []string
	err := a.Run(context.Background(), "go", func(ev any) {
		switch e := ev.(type) {
		case ToolStart:
			go func() { time.Sleep(50 * time.Millisecond); a.Steer("also check X") }()
		case SteerCommitted:
			committed = e.Texts
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(committed) != 1 || committed[0] != "also check X" {
		t.Fatalf("committed %v", committed)
	}
	reqs := seen()
	last := reqs[1][len(reqs[1])-1]
	if last["role"] != "user" || last["content"] != "also check X" {
		t.Fatalf("second request ends with %v", last)
	}
	if prev := reqs[1][len(reqs[1])-2]; prev["role"] != "tool" {
		t.Fatalf("steer should follow the tool result, got %v", prev)
	}
}

func TestSteerContinuesTurnWhenModelStops(t *testing.T) {
	srv, seen := fakeServer(t, text("first"), text("second"))
	a := newTestAgent(srv.URL)
	a.Steer("one more thing")
	if err := a.Run(context.Background(), "hi", func(any) {}); err != nil {
		t.Fatal(err)
	}
	if n := len(seen()); n != 2 {
		t.Fatalf("expected the turn to continue with a second request, got %d", n)
	}
}

func TestAutoCompactMidTurn(t *testing.T) {
	tc := toolCall("echo hi")
	// Report a large prompt so the context crosses the limit after the tool.
	tc = append(tc, `{"choices":[],"usage":{"prompt_tokens":950,"completion_tokens":10}}`)
	srv, seen := fakeServer(t, tc, text("NOTES: did echo"), text("finished"))
	a := New(config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: srv.URL},
		Model: config.Model{ID: "m", ContextWindow: 1000}}, "", os.TempDir())
	var rec []session.Entry
	a.Record = func(e session.Entry) { rec = append(rec, e) }
	var started, ended int
	err := a.Run(context.Background(), "go", func(ev any) {
		switch ev.(type) {
		case CompactStart:
			started++
		case CompactEnd:
			ended++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if started != 1 || ended != 1 {
		t.Fatalf("compaction events %d/%d", started, ended)
	}
	reqs := seen()
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	// Third request: system, then the kept user message and the notes (both
	// user messages, merged on send).
	third := reqs[2]
	if len(third) != 2 || !strings.HasPrefix(third[1]["content"].(string), "go\n\n"+SummaryPrefix) {
		t.Fatalf("post-compaction history: %v", third)
	}

	// Replaying the recorded entries reproduces the history.
	b := newTestAgent(srv.URL)
	b.Restore(rec)
	if len(b.messages) != len(a.messages) || b.messages[1].Content != a.messages[1].Content {
		t.Fatalf("restore mismatch:\n%v\n%v", b.messages, a.messages)
	}
}

func TestSystemPromptStableAcrossDays(t *testing.T) {
	a := newTestAgent("http://x")
	start := time.Date(2026, 1, 2, 23, 59, 0, 0, time.Local)
	a.SetStart(start)
	first := a.system
	a.SetStart(start) // e.g. after a resume the next day
	if a.system != first || !strings.Contains(first, "2026-01-02") {
		t.Fatalf("system prompt changed or lacks the start date:\n%s", first)
	}
}

// A Responses-API model runs a tool turn end to end; the encrypted reasoning
// item survives the session record/restore round trip and is sent back.
func TestResponsesModelToolTurnAndRestore(t *testing.T) {
	var mu sync.Mutex
	var inputs [][]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []any `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		inputs = append(inputs, body.Input)
		n := len(inputs)
		mu.Unlock()
		if n == 1 {
			args, _ := json.Marshal(map[string]string{"description": "t", "command": "echo hi"})
			a, _ := json.Marshal(string(args))
			fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"ENC"}}`)
			fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"c1","name":"bash","arguments":`+string(a)+`}}`)
		} else {
			fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.output_text.delta","delta":"fin"}`)
		}
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":1}}}`)
	}))
	defer srv.Close()

	ref := config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: srv.URL, API: "openai-responses"}, Model: config.Model{ID: "gpt-x", Efforts: []string{"low", "high"}}}
	a := New(ref, "low", "/tmp")
	var rec []session.Entry
	a.Record = func(e session.Entry) { rec = append(rec, e) }
	if err := a.Run(context.Background(), "go", func(any) {}); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 2 || !strings.Contains(fmt.Sprint(inputs[1]), "ENC") || !strings.Contains(fmt.Sprint(inputs[1]), "function_call_output") {
		t.Fatalf("second request input: %v", inputs)
	}

	// Persist and restore, then continue: the reasoning item must still be sent.
	data, _ := json.Marshal(rec)
	var back []session.Entry
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	b := New(ref, "low", "/tmp")
	b.Restore(back)
	if err := b.Run(context.Background(), "again", func(any) {}); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 3 || !strings.Contains(fmt.Sprint(inputs[2]), "ENC") {
		t.Fatalf("restored request lost reasoning: %v", inputs[len(inputs)-1])
	}
}

// sleepCmd sleeps 300ms in the platform shell (PowerShell's Start-Sleep
// rounds fractional seconds down to zero).
var sleepCmd = func() string {
	if shell.Default().Kind == shell.PowerShell {
		return "Start-Sleep -Milliseconds 300"
	}
	return "sleep 0.3"
}()

func TestUnsteer(t *testing.T) {
	a := New(config.ModelRef{}, "", t.TempDir())
	a.Steer("a")
	a.Steer("b")
	a.Steer("a")
	if !a.Unsteer("a") || a.Unsteer("c") {
		t.Fatal("unsteer")
	}
	if s := a.DrainSteers(); len(s) != 2 || s[0] != "a" || s[1] != "b" {
		t.Fatalf("left %q", s)
	}
}

func TestStopAtBoundaryEndsTheTurnWithoutError(t *testing.T) {
	srv, seen := fakeServer(t, toolCall("echo hi"), text("never asked for"))
	a := newTestAgent(srv.URL)
	err := a.Run(context.Background(), "go", func(ev any) {
		if _, ok := ev.(ToolStart); ok {
			a.StopAtBoundary()
			a.Steer("later")
		}
	})
	if err != nil {
		t.Fatalf("a stop is a normal end: %v", err)
	}
	if n := len(seen()); n != 1 {
		t.Fatalf("%d requests; the turn should end after the tool calls", n)
	}
	if left := a.DrainSteers(); len(left) != 1 || left[0] != "later" {
		t.Fatalf("an uncommitted steer stays for the front end: %v", left)
	}
}

func TestStopRequestDoesNotOutliveItsTurn(t *testing.T) {
	srv, seen := fakeServer(t, text("one"), toolCall("echo hi"), text("two"))
	a := newTestAgent(srv.URL)
	if err := a.Run(context.Background(), "a", func(any) {}); err != nil {
		t.Fatal(err)
	}
	a.StopAtBoundary() // after the turn: no boundary to take it
	if err := a.Run(context.Background(), "b", func(any) {}); err != nil {
		t.Fatal(err)
	}
	if n := len(seen()); n != 3 {
		t.Fatalf("the second turn ran %d requests, want its two", n-1)
	}
}

func TestInputNoteGoesWithTheNextUserMessageOnly(t *testing.T) {
	srv, seen := fakeServer(t, text("one"), text("two"))
	a := newTestAgent(srv.URL)
	a.SetInputNote("<note>")
	for _, in := range []string{"first", "second"} {
		if err := a.Run(context.Background(), in, func(any) {}); err != nil {
			t.Fatal(err)
		}
	}
	reqs := seen()
	if got := reqs[0][len(reqs[0])-1]["content"]; got != "first\n\n<note>" {
		t.Fatalf("first: %q", got)
	}
	if got := reqs[1][len(reqs[1])-1]["content"]; got != "second" {
		t.Fatalf("second: %q", got)
	}
	if got := reqs[1][1]["content"]; got != "first\n\n<note>" {
		t.Fatalf("history keeps the note, so the prefix stays the same: %q", got)
	}
}

// Steers typed during one step are committed as messages of their own, in
// order, so what the user said keeps its boundaries; SteerCommitted still
// lists the plain texts, and SteerNote's text rides at the end of a message.
// The request joins them (adjacent user messages are merged on send) with
// blank lines between, while the stored history keeps them apart.
func TestSteersAreSeparateMessages(t *testing.T) {
	srv, seen := fakeServer(t, text("first"), text("second"))
	a := newTestAgent(srv.URL)
	a.SteerNote = func(s string) string {
		if s == "b?" {
			return "NOTE"
		}
		return ""
	}
	a.Steer("a")
	a.Steer("b?")
	a.Steer("c")
	var committed []string
	if err := a.Run(context.Background(), "hi", func(ev any) {
		if e, ok := ev.(SteerCommitted); ok {
			committed = e.Texts
		}
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(committed, "|") != "a|b?|c" {
		t.Fatalf("committed %q", committed)
	}
	msgs := seen()[1]
	var users []string
	for _, m := range msgs {
		if m["role"] == "user" {
			users = append(users, m["content"].(string))
		}
	}
	if want := "a\n\nb?\n\nNOTE\n\nc"; len(users) != 2 || users[0] != "hi" || users[1] != want {
		t.Fatalf("user messages %q, want hi and %q", users, want)
	}
	var stored []string
	for _, m := range a.messages {
		if m.Role == "user" {
			stored = append(stored, m.Content)
		}
	}
	if want := "hi|a|b?\n\nNOTE|c"; strings.Join(stored, "|") != want {
		t.Fatalf("stored user messages %q, want %q", stored, want)
	}
}
