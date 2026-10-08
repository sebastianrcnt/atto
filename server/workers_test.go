package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
)

func workerFacade(t *testing.T, worker *Server) *Server {
	t.Helper()
	gateway := New("frontend", worker.Cwd)
	gateway.Workers = &WorkerRoutes{
		Open: func(ctx context.Context, id, cwd, model, effort string, deferred bool) (*Client, string, error) {
			c := Connect(context.Background(), worker)
			if id == "" {
				var info ThreadInfo
				if err := c.Call(ctx, "thread/start", map[string]any{"cwd": cwd, "model": model, "effort": effort, "deferStart": deferred}, &info); err != nil {
					c.Close()
					return nil, "", err
				}
				id = info.ID
			}
			return c, id, nil
		},
		List: func() ([]WorkerSummary, error) {
			worker.mu.Lock()
			defer worker.mu.Unlock()
			var out []WorkerSummary
			for id, t := range worker.threads {
				out = append(out, WorkerSummary{ID: id, Cwd: t.cwd, Version: worker.Version})
			}
			return out, nil
		},
	}
	t.Cleanup(gateway.Close)
	return gateway
}

func TestWorkerFacadeSharesTurnAndDetaches(t *testing.T) {
	gate := make(chan struct{})
	worker, m := testServer(t, providertest.Reply{Text: "shared answer", Gate: gate})
	gateway := workerFacade(t, worker)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := Connect(ctx, gateway)
	t.Cleanup(func() { c.Close() })
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{2}}, nil); err != nil {
		t.Fatal(err)
	}
	var info ThreadInfo
	if err := c.Call(ctx, "thread/start", nil, &info); err != nil {
		t.Fatal(err)
	}
	id := info.ID
	viewer := Connect(ctx, worker)
	t.Cleanup(func() { viewer.Close() })
	if err := viewer.Call(ctx, "thread/attach", map[string]any{"threadId": id}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "turn/start", map[string]any{"threadId": id, "input": "one runtime"}, nil); err != nil {
		t.Fatal(err)
	}
	if m.Started(5*time.Second) == 0 {
		t.Fatal("no request")
	}
	// Another frontend reaches the same runtime, not another writer.
	other := Connect(ctx, gateway)
	t.Cleanup(func() { other.Close() })
	if err := other.Call(ctx, "thread/resume", map[string]any{"threadId": id}, &info); err != nil || !info.Busy {
		t.Fatalf("resume %+v %v", info, err)
	}
	c.Close()
	close(gate)
	for _, client := range []*Client{viewer, other} {
		seen := 0
		for {
			select {
			case n := <-client.Events():
				if n.ThreadID() != id {
					continue
				}
				if n.Method == "item/completed" && strings.Contains(string(n.Params), "shared answer") {
					seen++
				}
				if n.Method == "turn/completed" {
					if seen != 1 {
						t.Fatalf("answer delivered %d times", seen)
					}
					goto completed
				}
			case <-ctx.Done():
				t.Fatal("turn did not finish")
			}
		}
	completed:
	}
	if err := other.Call(ctx, "thread/read", map[string]any{"threadId": id}, &info); err != nil {
		t.Fatal(err)
	}
	if info.ServerInstance != gateway.instance || info.Busy {
		t.Fatalf("gateway cursor %+v", info)
	}
	gateway.Close()
	if !worker.Loaded(id) {
		t.Fatal("closing frontend stopped its worker")
	}
	if len(m.Requests()) != 1 {
		t.Fatalf("%d requests", len(m.Requests()))
	}
}

func TestWorkerFacadeAddressesRecoveryAndListsEmptySessions(t *testing.T) {
	worker, _ := testServer(t, providertest.Reply{Status: 404})
	gateway := workerFacade(t, worker)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := Connect(ctx, gateway)
	t.Cleanup(func() { c.Close() })
	var init struct {
		Client string `json:"clientId"`
	}
	if err := c.Call(ctx, "initialize", nil, &init); err != nil {
		t.Fatal(err)
	}
	var info ThreadInfo
	if err := c.Call(ctx, "thread/start", nil, &info); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Threads []struct {
			ID     string `json:"threadId"`
			Loaded bool   `json:"loaded"`
		} `json:"threads"`
	}
	if err := c.Call(ctx, "thread/list", nil, &list); err != nil || len(list.Threads) != 1 || list.Threads[0].ID != info.ID || !list.Threads[0].Loaded {
		t.Fatalf("empty live session list: %+v %v", list, err)
	}
	if err := c.Call(ctx, "input/submit", map[string]any{"threadId": info.ID, "input": "recover this"}, nil); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case n := <-c.Events():
			if n.Method != "input/recovered" {
				continue
			}
			var p struct {
				Client string `json:"clientId"`
				Text   string `json:"text"`
			}
			_ = json.Unmarshal(n.Params, &p)
			if p.Client != init.Client || p.Text != "recover this" {
				t.Fatalf("recovery: %+v", p)
			}
			return
		case <-ctx.Done():
			t.Fatal("no recovery")
		}
	}
}

func TestWorkerFacadeScopedGateway(t *testing.T) {
	worker, _ := testServer(t, providertest.Reply{Text: "web answer"})
	gateway := workerFacade(t, worker)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner := Connect(ctx, gateway)
	t.Cleanup(func() { owner.Close() })
	var info ThreadInfo
	if err := owner.Call(ctx, "thread/start", nil, &info); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	local := false
	web := httptest.NewServer(gateway.ScopedHandler("token", Scope{Thread: func() string { return info.ID }, Local: func(text string) bool { mu.Lock(); defer mu.Unlock(); local = text == "/clear"; return local }}))
	defer web.Close()
	call := func(method string, params any) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"id": 1, "method": method, "params": params})
		r, err := web.Client().Post(web.URL+"/rpc?token=token", "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var reply struct {
			Result map[string]any `json:"result"`
			Error  *RPCError      `json:"error"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reply); err != nil || reply.Error != nil {
			t.Fatalf("%s: %v %v", method, reply.Error, err)
		}
		return reply.Result
	}
	if init := call("initialize", nil); init["live"] != true || init["threadId"] != info.ID {
		t.Fatalf("live init %v", init)
	}
	if read := call("thread/read", nil); read["live"] != true {
		t.Fatalf("live read %v", read)
	}
	call("turn/start", map[string]any{"input": "/clear"})
	mu.Lock()
	got := local
	mu.Unlock()
	if !got {
		t.Fatal("remote local command did not reach terminal")
	}
}

func TestWorkerFacadeRefusesOlderProtocol(t *testing.T) {
	gateway := New("frontend", t.TempDir())
	defer gateway.Close()
	gateway.Workers = &WorkerRoutes{Open: func(ctx context.Context, id, cwd, model, effort string, deferred bool) (*Client, string, error) {
		a, b := net.Pipe()
		go func() {
			defer a.Close()
			sc := bufio.NewScanner(a)
			for sc.Scan() {
				var req struct {
					ID json.RawMessage `json:"id"`
				}
				_ = json.Unmarshal(sc.Bytes(), &req)
				_ = json.NewEncoder(a).Encode(map[string]any{"id": req.ID, "result": map[string]any{"protocolVersion": 1, "serverInstanceId": "old-worker", "clientId": "old-client"}})
			}
		}()
		return NewClient(b), id, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := Connect(ctx, gateway)
	defer c.Close()
	err := c.Call(ctx, "thread/resume", map[string]any{"threadId": "old-session"}, nil)
	var rpc *RPCError
	if !errors.As(err, &rpc) || rpc.Data == nil || rpc.Data.Reason != ReasonUnsupportedProtocol || !strings.Contains(err.Error(), "atto daemon stop -force") {
		t.Fatalf("unclear version refusal: %v", err)
	}
}

func TestWorkerFacadeHTTPStreamIsInteractive(t *testing.T) {
	worker, _ := testServer(t)
	gateway := workerFacade(t, worker)
	web := httptest.NewServer(gateway.HTTPHandler("token"))
	defer web.Close()
	body := `{"id":1,"method":"thread/start"}`
	resp, err := web.Client().Post(web.URL+"/rpc?token=token", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if worker.interactiveClients() != 0 {
		t.Fatal("bare HTTP request counted as interactive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", web.URL+"/events?token=token", nil)
	stream, err := web.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	for until := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if worker.interactiveClients() == 1 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("SSE capabilities did not reach worker")
		}
	}
	cancel()
	for until := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if worker.interactiveClients() == 0 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("SSE disconnect did not detach worker client")
		}
	}
}
