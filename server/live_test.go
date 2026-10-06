package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
)

// fakeLive is a Live front end that records what it is asked.
type fakeLive struct {
	mu     sync.Mutex
	id     string
	busy   bool
	model  string
	effort string
	sent   []string
	images int
	stops  int
	items  []Item
	// answers to the open prompt "p1"
	answers []PromptAnswer
	// steers taken back, and turns rolled back
	unsteered []string
	rolled    int
}

func (f *fakeLive) Unsteer(input string, queued bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if input != "also" {
		return errors.New("that message is no longer pending")
	}
	f.unsteered = append(f.unsteered, fmt.Sprintf("%s %v", input, queued))
	return nil
}

func (f *fakeLive) Rollback(n int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy {
		return "", errors.New("a turn is running")
	}
	f.rolled += n
	return "go", nil
}

func (f *fakeLive) Answer(id string, ans PromptAnswer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != "p1" {
		return errors.New("prompt " + id + " is not open")
	}
	f.answers = append(f.answers, ans)
	return nil
}

func (f *fakeLive) Thread(items bool, at func()) (ThreadInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info := ThreadInfo{ID: f.id, Cwd: "/w", Model: f.model, Effort: f.effort, Efforts: []string{"low", "high"}, Busy: f.busy}
	if items {
		info.Items = append(info.Items, f.items...)
	}
	if at != nil {
		at()
	}
	return info, nil
}

func (f *fakeLive) Model() config.ModelRef {
	return config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m", Input: []string{"text", "image"}}}
}

func (f *fakeLive) Send(input string, imgs []provider.Image) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, input)
	f.images += len(imgs)
	if f.busy {
		return "steered", "", nil
	}
	f.busy = true
	return "started", f.id + "-t1", nil
}

func (f *fakeLive) Interrupt() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	was := f.busy
	f.busy = false
	return was
}

func (f *fakeLive) Background() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy // as if a command ran whenever a turn does
}

func (f *fakeLive) SetModel(id string) (ThreadInfo, error) {
	f.mu.Lock()
	f.model = id
	f.mu.Unlock()
	return f.Thread(false, nil)
}

func (f *fakeLive) SetEffort(level string) (ThreadInfo, error) {
	f.mu.Lock()
	f.effort = level
	f.mu.Unlock()
	return f.Thread(false, nil)
}

// rpcClient calls a test HTTP server with a token.
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
	t.Setenv("ATTO_DIR", t.TempDir())
	f := &fakeLive{id: "sess1", model: "t/m", effort: "low", items: []Item{{ID: "sess1-i1", Type: ItemUser, Text: "hi"}}}
	s := NewLive("test", f)
	defer s.Close()
	clients := make(chan int, 10)
	s.OnClients = func(n int) { clients <- n }
	h := httptest.NewServer(s.HTTPHandler("live-token-123456"))
	defer h.Close()
	c := rpcClient{t, h.URL, "live-token-123456"}

	wrong, _ := http.NewRequest("POST", h.URL+"/rpc", strings.NewReader(`{"method":"initialize"}`))
	wrong.Header.Set("Authorization", "Bearer live-token-654321")
	if r, _ := http.DefaultClient.Do(wrong); r.StatusCode != 401 {
		t.Fatalf("wrong token: %d", r.StatusCode)
	}
	if r, _ := http.Post(h.URL+"/rpc", "application/json", strings.NewReader(`{"method":"initialize"}`)); r.StatusCode != 401 {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	init := c.must("initialize", nil)
	if init["live"] != true || init["threadId"] != "sess1" {
		t.Fatalf("initialize %v", init)
	}
	list := c.must("thread/list", nil)["threads"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["threadId"] != "sess1" {
		t.Fatalf("thread/list %v", list)
	}

	// Replay: thread/read has the items and where to follow from.
	s.Publish("item/completed", map[string]any{"threadId": "sess1", "item": Item{ID: "x"}})
	read := c.must("thread/read", map[string]any{})
	if items := read["items"].([]any); len(items) != 1 || read["live"] != true || read["eventId"].(float64) != 1 {
		t.Fatalf("thread/read %v", read)
	}

	events, stop := sse(t, h.URL+"/events?token=live-token-123456&lastEventId=1")
	defer stop()
	if n := <-clients; n != 1 {
		t.Fatalf("clients %d", n)
	}

	// Sending while idle starts a turn; while busy it is the front end's to
	// steer or queue; turn/steer goes the same way.
	if r := c.must("turn/start", map[string]any{"threadId": "sess1", "input": "go"}); r["status"] != "started" || r["turnId"] != "sess1-t1" {
		t.Fatalf("turn/start %v", r)
	}
	if r := c.must("turn/steer", map[string]any{"threadId": "sess1", "input": "also"}); r["status"] != "steered" {
		t.Fatalf("turn/steer %v", r)
	}
	c.must("turn/background", map[string]any{})
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	c.must("turn/start", map[string]any{"input": "look", "images": []map[string]any{{"mimeType": "image/png", "data": png}}})
	f.mu.Lock()
	sent, imgs := strings.Join(f.sent, "|"), f.images
	f.mu.Unlock()
	if !strings.HasPrefix(sent, "go|also|look\n\n[image 1: 1x1 PNG]") || imgs != 1 {
		t.Fatalf("sent %q, %d images", sent, imgs)
	}
	if _, err := c.call("turn/start", map[string]any{"input": " "}); err == nil {
		t.Fatal("empty input accepted")
	}

	c.must("turn/interrupt", map[string]any{})
	if _, err := c.call("turn/background", map[string]any{}); err == nil || !strings.Contains(err.Message, "no command") {
		t.Fatalf("turn/background: %v", err)
	}
	if r := c.must("thread/setModel", map[string]any{"model": "t/other"}); r["model"] != "t/other" {
		t.Fatalf("setModel %v", r)
	}
	if r := c.must("thread/setEffort", map[string]any{"effort": "high"}); r["effort"] != "high" {
		t.Fatalf("setEffort %v", r)
	}
	if _, err := c.call("thread/start", nil); err == nil {
		t.Fatal("thread/start should be refused")
	}
	// Pending input goes back to the front end to take back.
	c.must("turn/unsteer", map[string]any{"input": "also", "queued": true})
	if _, err := c.call("turn/unsteer", map[string]any{"input": "taken"}); err == nil || !strings.Contains(err.Message, "no longer") {
		t.Fatalf("turn/unsteer of a taken steer: %v", err)
	}
	// Rollback goes back as /tree does, and gives the message back.
	if r := c.must("thread/rollback", map[string]any{}); r["input"] != "go" || r["threadId"] != "sess1" {
		t.Fatalf("thread/rollback %v", r)
	}
	f.mu.Lock()
	if strings.Join(f.unsteered, ",") != "also true" || f.rolled != 1 {
		t.Fatalf("unsteered %v, rolled back %d", f.unsteered, f.rolled)
	}
	f.mu.Unlock()
	// Jobs and subagents are the session's, read from files.
	if r := c.must("job/list", map[string]any{}); len(r["jobs"].([]any)) != 0 {
		t.Fatalf("job/list %v", r)
	}
	if r := c.must("subagent/list", map[string]any{}); len(r["subagents"].([]any)) != 0 {
		t.Fatalf("subagent/list %v", r)
	}
	if _, ok := init["settings"].(map[string]any)["toolGroups"]; !ok {
		t.Fatalf("initialize without settings: %v", init)
	}

	// Prompt answers go to the front end; malformed ones do not.
	c.must("prompt/answer", map[string]any{"id": "p1", "index": 2})
	c.must("prompt/answer", map[string]any{"id": "p1", "text": "hi"})
	c.must("prompt/answer", map[string]any{"id": "p1", "cancel": true})
	for _, bad := range []map[string]any{{"index": 0}, {"id": "p1"}, {"id": "p9", "cancel": true}} {
		if _, err := c.call("prompt/answer", bad); err == nil || err.Code != codeInvalidParams {
			t.Fatalf("prompt/answer %v: %v", bad, err)
		}
	}
	f.mu.Lock()
	if a := f.answers; len(a) != 3 || *a[0].Index != 2 || *a[1].Text != "hi" || !a[2].Cancel {
		t.Fatalf("answers %+v", a)
	}
	f.mu.Unlock()

	// The front end switched sessions: clients hear it, and the old ID is
	// refused.
	f.mu.Lock()
	f.id = "sess2"
	f.mu.Unlock()
	s.Publish("thread/switched", map[string]any{"threadId": "sess2", "previousThreadId": "sess1"})
	if m := next(t, events, "thread/switched"); m.Params["threadId"] != "sess2" {
		t.Fatalf("switched %v", m.Params)
	}
	if _, err := c.call("turn/start", map[string]any{"threadId": "sess1", "input": "x"}); err == nil || !strings.Contains(err.Message, "no longer") {
		t.Fatalf("old thread: %v", err)
	}
	if _, err := c.call("thread/read", map[string]any{"threadId": "sess1"}); err == nil {
		t.Fatal("thread/read of the old session")
	}
	stop()
	if n := <-clients; n != 0 {
		t.Fatalf("clients after leaving %d", n)
	}
}

func TestWebClientServedFromEmbed(t *testing.T) {
	s := NewLive("test", &fakeLive{id: "s"})
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
