package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
)

type rpcClient struct {
	t     *testing.T
	url   string
	token string
}

func (c rpcClient) call(method string, params any) (map[string]any, *rpcError) {
	c.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", c.url+"/rpc", strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m msg
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return m.Result, m.Error
}

func (c rpcClient) must(method string, params any) map[string]any {
	c.t.Helper()
	r, err := c.call(method, params)
	if err != nil {
		c.t.Fatalf("%s: %s", method, err.Message)
	}
	return r
}

// sse follows a test server's events.
func sse(t *testing.T, url string) (<-chan msg, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan msg, 100)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var m msg
				_ = json.Unmarshal([]byte(data), &m)
				ch <- m
			}
		}
	}()
	return ch, func() { cancel(); resp.Body.Close() }
}

func next(t *testing.T, ch <-chan msg, method string) msg {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed waiting for %s", method)
			}
			if m.Method == method {
				return m
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", method)
		}
	}
}

func TestLiveSessionProtocol(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, providertest.Reply{Text: "answer", Gate: gate}, providertest.Reply{Text: "done"})
	var selected atomic.Value
	selected.Store(h.id)
	clients := make(chan int, 10)
	h.s.OnClients = func(n int) { clients <- n }
	web := httptest.NewServer(h.s.ScopedHandler("live-token-123456", Scope{Thread: func() string { return selected.Load().(string) }}))
	defer web.Close()
	c := rpcClient{t, web.URL, "live-token-123456"}
	for _, token := range []string{"", "wrong"} {
		req, _ := http.NewRequest("POST", web.URL+"/rpc", strings.NewReader(`{"method":"initialize"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatalf("token %q status %d", token, r.StatusCode)
		}
	}
	init := c.must("initialize", nil)
	if init["live"] != true || init["threadId"] != h.id {
		t.Fatalf("init %v", init)
	}
	if _, ok := init["settings"].(map[string]any)["toolGroups"]; !ok {
		t.Fatal("settings missing")
	}
	if list := c.must("thread/list", nil)["threads"].([]any); len(list) != 1 {
		t.Fatalf("list %v", list)
	}
	read := c.must("thread/read", nil)
	if read["live"] != true || read["eventId"] == nil {
		t.Fatalf("snapshot %v", read)
	}
	stream, stop := sse(t, web.URL+"/events?token=live-token-123456&lastEventId="+fmt.Sprint(read["eventId"]))
	defer stop()
	if n := <-clients; n != 1 {
		t.Fatalf("clients %d", n)
	}
	if r := c.must("turn/start", map[string]any{"input": "go"}); r["status"] != StatusStarted {
		t.Fatalf("start %v", r)
	}
	h.m.Started(5 * time.Second)
	if r := c.must("turn/steer", map[string]any{"input": "also"}); r["status"] != StatusSteered {
		t.Fatalf("steer %v", r)
	}
	c.must("turn/unsteer", map[string]any{"input": "also"})
	if _, err := c.call("turn/unsteer", map[string]any{"input": "taken"}); err == nil {
		t.Fatal("taken steer accepted")
	}
	if _, err := c.call("turn/background", nil); err == nil {
		t.Fatal("background without command accepted")
	}
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	c.must("turn/start", map[string]any{"input": "look", "images": []map[string]any{{"mimeType": "image/png", "data": png}}})
	if _, err := c.call("turn/start", map[string]any{"input": " "}); err == nil {
		t.Fatal("empty input accepted")
	}
	close(gate)
	next(t, stream, "turn/completed")
	next(t, stream, "turn/completed")
	reqs := h.m.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[0], "go") || !strings.Contains(reqs[1], "image 1: 1x1 PNG") || strings.Contains(reqs[1], `"content":"also"`) {
		t.Fatalf("requests %v", reqs)
	}
	for _, method := range []string{"job/list", "agent/list", "subagent/list"} {
		r := c.must(method, nil)
		key := "jobs"
		if method != "job/list" {
			key = "agents"
		}
		if len(r[key].([]any)) != 0 {
			t.Fatalf("%s %v", method, r)
		}
		if method == "subagent/list" && len(r["subagents"].([]any)) != 0 {
			t.Fatal("legacy agents missing")
		}
	}
	if _, err := c.call("thread/start", nil); err == nil {
		t.Fatal("scoped thread/start accepted")
	}
	if r := c.must("thread/setEffort", map[string]any{"effort": "none"}); r["effort"] != "none" {
		t.Fatalf("effort %v", r)
	}
	if r := c.must("thread/setModel", map[string]any{"model": "fake/m"}); r["model"] != "fake/m" {
		t.Fatalf("model %v", r)
	}
	if r := c.must("thread/rollback", nil); !strings.HasPrefix(fmt.Sprint(r["input"]), "look\n\n[image 1:") {
		t.Fatalf("rollback %v", r)
	}
	// Prompts use first-answer arbitration and retain malformed-answer errors.
	th, _ := h.s.thread(h.id)
	var pid string
	th.call(func() error {
		wire, err := th.openClientPrompt("owner", Prompt{RequestID: "picker", Kind: PromptSelect, Options: []PromptOption{{Label: "yes"}}})
		pid = wire.ID
		return err
	})
	for _, bad := range []map[string]any{{"index": 0}, {"id": pid}, {"id": "missing", "cancel": true}, {"id": pid, "index": 2}} {
		if _, err := c.call("prompt/answer", bad); err == nil || err.Code != codeInvalidParams {
			t.Fatalf("answer %v error %v", bad, err)
		}
	}
	c.must("prompt/answer", map[string]any{"id": pid, "index": 0})
	if _, err := c.call("prompt/answer", map[string]any{"id": pid, "cancel": true}); err == nil {
		t.Fatal("second answer accepted")
	}
	var other ThreadInfo
	if err := h.c.Call(context.Background(), "thread/start", nil, &other); err != nil {
		t.Fatal(err)
	}
	selected.Store(other.ID)
	h.s.Switched(other.ID, h.id)
	for {
		m := next(t, stream, "thread/switched")
		if m.Params["threadId"] == other.ID {
			break
		}
	}
	for _, method := range []string{"thread/read", "turn/start"} {
		if _, err := c.call(method, map[string]any{"threadId": h.id, "input": "x"}); err == nil {
			t.Fatalf("old thread accepted by %s", method)
		}
	}
	stop()
	if n := <-clients; n != 0 {
		t.Fatalf("clients %d", n)
	}
}

func TestWebClientServedFromEmbed(t *testing.T) {
	s := New("test", t.TempDir())
	defer s.Close()
	h := httptest.NewServer(s.HTTPHandler("tok-tok-tok-tok-tok"))
	defer h.Close()
	get := func(path string) (string, *http.Response) {
		resp, err := http.Get(h.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), resp
	}
	page, resp := get("/")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, asset := range []struct{ ref, typ string }{{`src="app.js?v=`, "javascript"}, {`href="app.css?v=`, "text/css"}} {
		i := strings.Index(page, asset.ref)
		if i < 0 {
			t.Fatalf("index.html does not reference %s:\n%s", asset.ref, page)
		}
		url := page[i+strings.Index(asset.ref, `"`)+1:]
		url = url[:strings.Index(url, `"`)]
		body, resp := get("/" + url)
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), asset.typ) || len(body) < 1000 {
			t.Fatalf("%s: %d %s, %d bytes", url, resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
		}
	}
	if _, resp := get("/nope.js"); resp.StatusCode != 404 {
		t.Fatalf("missing file: %d", resp.StatusCode)
	}
}
