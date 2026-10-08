package server

import (
	"bufio"
	"context"
	"encoding/json"
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
)

// fakeModel answers: first request -> bash tool call, then text.
func fakeModel(t *testing.T) string {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		i := n
		mu.Unlock()
		if i%2 == 1 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_content":"plan"}}]}`+"\n\n")
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"description\":\"Say hi\",\"command\":\"echo hi\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":5}}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":100}}}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func setup(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	url := fakeModel(t)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"fake":{"baseUrl":"`+url+`","models":[{"id":"m","contextWindow":1000}]}}}`), 0o644)
	work := t.TempDir()
	return work
}

type msg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params map[string]any  `json:"params"`
	Result map[string]any  `json:"result"`
	Error  *rpcError       `json:"error"`
}

func TestStdioTurn(t *testing.T) {
	work := setup(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := New("test", work)
	t.Cleanup(s.Close)
	go s.ServeStdio(context.Background(), inR, outW)
	lines := make(chan msg, 100)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m msg
			json.Unmarshal(sc.Bytes(), &m)
			lines <- m
		}
	}()
	send := func(id int, method string, params any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		inW.Write(append(b, '\n'))
	}
	wait := func(pred func(msg) bool) msg {
		t.Helper()
		timeout := time.After(10 * time.Second)
		for {
			select {
			case m := <-lines:
				if pred(m) {
					return m
				}
			case <-timeout:
				t.Fatal("timed out")
			}
		}
	}

	send(1, "initialize", nil)
	if r := wait(func(m msg) bool { return string(m.ID) == "1" }); r.Result["name"] != "atto" {
		t.Fatalf("initialize: %+v", r)
	}
	send(2, "thread/start", map[string]any{})
	th := wait(func(m msg) bool { return string(m.ID) == "2" })
	id := th.Result["threadId"].(string)

	send(3, "turn/start", map[string]any{"threadId": id, "input": "hello"})
	var types []string
	done := wait(func(m msg) bool {
		if m.Method == "item/completed" {
			if kind := m.Params["item"].(map[string]any)["type"].(string); kind != ItemNotice {
				types = append(types, kind)
			}
		}
		return m.Method == "turn/completed"
	})
	if done.Params["status"] != "completed" {
		t.Fatalf("turn: %+v", done.Params)
	}
	want := "userMessage,reasoning,commandExecution,agentMessage"
	if strings.Join(types, ",") != want {
		t.Fatalf("items %v, want %s", types, want)
	}
	usage := done.Params["usage"].(map[string]any)
	if usage["inputTokens"].(float64) != 220 || usage["cachedInputTokens"].(float64) != 100 {
		t.Fatalf("usage %v", usage)
	}

	// Unknown method and bad params.
	send(4, "nope", nil)
	if r := wait(func(m msg) bool { return string(m.ID) == "4" }); r.Error == nil || r.Error.Code != codeMethodNotFound {
		t.Fatalf("want method-not-found: %+v", r)
	}

	// A new server process resumes after the previous writer closes.
	s.Close()
	s2 := New("test", work)
	t.Cleanup(s2.Close)
	resp := s2.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"thread/resume","params":{"threadId":"`+id+`"}}`))
	info := resp.Result.(ThreadInfo)
	var got []string
	for _, it := range info.Items {
		if it.Type != ItemNotice {
			got = append(got, it.Type)
		}
	}
	if strings.Join(got, ",") != want {
		t.Fatalf("resumed items %v", got)
	}
	var cmd Item
	for _, it := range info.Items {
		if it.Type == ItemCommand {
			cmd = it
			break
		}
	}
	if cmd.Command != "echo hi" || cmd.ExitCode == nil || *cmd.ExitCode != 0 || !strings.Contains(cmd.Output, "hi") {
		t.Fatalf("resumed command %+v", cmd)
	}
}

func TestHTTPAndSSE(t *testing.T) {
	work := setup(t)
	srv := New("test", work)
	h := httptest.NewServer(srv.HTTPHandler("secret-token-1234"))
	defer h.Close()
	defer srv.Close()
	call := func(method string, params any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", h.URL+"/rpc", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer secret-token-1234")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m msg
		json.NewDecoder(resp.Body).Decode(&m)
		if m.Error != nil {
			t.Fatalf("%s: %s", method, m.Error.Message)
		}
		return m.Result
	}

	// Auth is required.
	if r, _ := http.Post(h.URL+"/rpc", "application/json", strings.NewReader(`{}`)); r.StatusCode != 401 {
		t.Fatalf("want 401, got %d", r.StatusCode)
	}
	if r, _ := http.Get(h.URL + "/"); r.StatusCode != 200 {
		t.Fatalf("web client: %d", r.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", h.URL+"/events?token=secret-token-1234", nil)
	events, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	id := call("thread/start", map[string]any{})["threadId"].(string)
	call("turn/start", map[string]any{"threadId": id, "input": "hello"})

	sc := bufio.NewScanner(events.Body)
	var lastID string
	deadline := time.Now().Add(10 * time.Second)
	for sc.Scan() && time.Now().Before(deadline) {
		line := sc.Text()
		if after, ok := strings.CutPrefix(line, "id: "); ok {
			lastID = after
		}
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"turn/completed"`) {
			break
		}
	}
	if lastID == "" {
		t.Fatal("no events received")
	}
	cancel()

	// Reconnecting with an earlier Last-Event-ID replays what was missed.
	req2, _ := http.NewRequest("GET", h.URL+"/events?token=secret-token-1234&lastEventId=1", nil)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	resp2, err := http.DefaultClient.Do(req2.WithContext(ctx2))
	if err != nil {
		t.Fatal(err)
	}
	replay, _ := bufio.NewReader(resp2.Body).ReadString('\n')
	if !strings.HasPrefix(replay, "id: 2") {
		t.Fatalf("replay should start after id 1, got %q", replay)
	}
}
