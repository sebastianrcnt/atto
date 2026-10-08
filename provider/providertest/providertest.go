// Package providertest is a scripted, OpenAI-compatible streaming model
// for tests: each request gets the next reply of a script (the last one
// repeats), and the request bodies are kept for byte-level assertions.
// Replies can wait on a gate, so a test can act while a request is in
// flight, and stream slowly.
package providertest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Reply is one streamed response.
type Reply struct {
	Reasoning string
	Text      string
	// Command, when set, makes the reply a bash tool call (Description is
	// its description) instead of a stop.
	Command, Description string
	// Words streams Text in this many pieces, Delay apart.
	Words int
	Delay time.Duration
	// Gate, when set, is waited on before anything is sent; closing it
	// releases every request waiting on it.
	Gate chan struct{}
	// Status, when not 0 or 200, fails the request with that status.
	Status int
	// Error replaces the default error message of a failed reply.
	Error string
	// Headers are sent before the reply, for retry and transport tests.
	Headers map[string]string
	// Usage is the reply's prompt/completion/cached tokens.
	Prompt, Completion, Cached int
}

// Model is a running fake model.
type Model struct {
	*httptest.Server
	mu      sync.Mutex
	script  []Reply
	bodies  []string
	started chan int // the number of each request as it arrives
}

// New starts a model answering with script; the last reply repeats.
func New(t testing.TB, script ...Reply) *Model {
	if len(script) == 0 {
		script = []Reply{{Text: "ok"}}
	}
	m := &Model{script: script, started: make(chan int, 1024)}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Close)
	return m
}

// Install writes models.json into dir (an ATTO_DIR) with provider "fake",
// model "m" served by m, and returns "fake/m".
func (m *Model) Install(t testing.TB, dir string) string {
	t.Helper()
	cfg := `{"providers":{"fake":{"baseUrl":"` + m.URL + `","models":[{"id":"m","contextWindow":100000,"input":["text","image"]}]}}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return "fake/m"
}

// Requests are the request bodies so far.
func (m *Model) Requests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.bodies...)
}

// Started returns the number (from 1) of the next request to arrive, or 0
// after timeout.
func (m *Model) Started(timeout time.Duration) int {
	select {
	case n := <-m.started:
		return n
	case <-time.After(timeout):
		return 0
	}
}

// SetScript replaces the replies of the requests still to come.
func (m *Model) SetScript(script ...Reply) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.script = append(m.script[:0:0], script...)
	m.bodies = m.bodies[:0:0]
}

func (m *Model) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.bodies = append(m.bodies, string(raw))
	n := len(m.bodies)
	rep := m.script[min(n, len(m.script))-1]
	m.mu.Unlock()
	select {
	case m.started <- n:
	default:
	}
	if rep.Gate != nil {
		select {
		case <-rep.Gate:
		case <-r.Context().Done():
			return
		}
	}
	for name, value := range rep.Headers {
		w.Header().Set(name, value)
	}
	if rep.Status != 0 && rep.Status != http.StatusOK {
		message := rep.Error
		if message == "" {
			message = "scripted failure"
		}
		body, _ := json.Marshal(map[string]any{"error": map[string]string{"message": message}})
		http.Error(w, string(body), rep.Status)
		return
	}
	fl, _ := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	delta := func(d map[string]any, finish any) map[string]any {
		return map[string]any{"choices": []any{map[string]any{"delta": d, "finish_reason": finish}}}
	}
	if rep.Reasoning != "" {
		send(delta(map[string]any{"reasoning_content": rep.Reasoning}, nil))
	}
	for _, piece := range split(rep.Text, rep.Words) {
		if rep.Delay > 0 {
			select {
			case <-time.After(rep.Delay):
			case <-r.Context().Done():
				return
			}
		}
		send(delta(map[string]any{"content": piece}, nil))
	}
	usage := map[string]any{"prompt_tokens": rep.Prompt, "completion_tokens": rep.Completion,
		"prompt_tokens_details": map[string]any{"cached_tokens": rep.Cached}}
	last := delta(map[string]any{}, "stop")
	if rep.Command != "" {
		args, _ := json.Marshal(map[string]string{"description": rep.Description, "command": rep.Command})
		last = delta(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("c%d", n), "type": "function",
			"function": map[string]any{"name": "bash", "arguments": string(args)}}}}, "tool_calls")
	}
	last["usage"] = usage
	send(last)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// split cuts s into n pieces of about equal length (one when n < 2).
func split(s string, n int) []string {
	if s == "" {
		return nil
	}
	r := []rune(s)
	if n < 2 || n > len(r) {
		return []string{s}
	}
	var out []string
	for i := range n {
		out = append(out, string(r[i*len(r)/n:(i+1)*len(r)/n]))
	}
	return out
}
