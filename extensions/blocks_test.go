package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
)

// sideServer is a fake model server for atto.complete. The user message
// picks what it does: "slow" hangs until the client goes away, "500" fails,
// "wait" answers after 300ms; anything else is answered with "re:<prompt>".
type sideServer struct {
	*httptest.Server
	mu        sync.Mutex
	bodies    []map[string]any
	inflight  atomic.Int32
	maxFlight atomic.Int32
	canceled  atomic.Int32
}

func newSideServer(t *testing.T) *sideServer {
	s := &sideServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		n := s.inflight.Add(1)
		defer s.inflight.Add(-1)
		for {
			m := s.maxFlight.Load()
			if n <= m || s.maxFlight.CompareAndSwap(m, n) {
				break
			}
		}
		msgs, _ := body["messages"].([]any)
		last, _ := msgs[len(msgs)-1].(map[string]any)
		prompt, _ := last["content"].(string)
		switch prompt {
		case "slow":
			select {
			case <-r.Context().Done():
				s.canceled.Add(1)
			case <-time.After(10 * time.Second):
			}
			return
		case "500":
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		case "wait":
			time.Sleep(300 * time.Millisecond)
		}
		reply, _ := json.Marshal("re:" + prompt)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", reply)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *sideServer) requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.bodies...)
}

// sideModels configures s/m at url and d/m at a server that is not there.
func sideModels(t *testing.T, url string) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	write(t, config.ModelsPath(), `{"providers":{
	  "s":{"baseUrl":"`+url+`","models":[{"id":"m","contextWindow":100000}]},
	  "d":{"baseUrl":"`+deadURL+`","models":[{"id":"m","contextWindow":100000}]}}}`)
}

func TestBlockEventsFireOnceAndNeverWait(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "b.ts"), `
export default function (atto: any) {
  atto.on("message_end", async (e: any, ctx: any) => {
    atto.log("msg", e.blockId, e.text, e.model);
    await new Promise((r) => setTimeout(r, 2000));
    ctx.ui.setBlockStatus(e.blockId, "slow done");
  });
  atto.on("reasoning_end", (e: any, ctx: any) => {
    ctx.ui.setBlockDisplay(e.blockId, e.text.toUpperCase());
    ctx.ui.setBlockStatus(e.blockId, "up");
  });
  atto.on("reasoning_end", () => { throw new Error("handler broke"); });
}
`)
	h := newHost(true)
	m := load(t, cwd, h)
	start := time.Now()
	m.BlockEnd("text", "sid.e1:text", "hello", "p/m")
	m.BlockEnd("reasoning", "sid.e1:reasoning", "think", "p/m")
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("BlockEnd waited %s for a handler sleeping 2s", d)
	}
	eventually(t, "the reasoning display", func() bool {
		s := h.snapshot()
		return h.blockDisplayOf("b/sid.e1:reasoning") == "THINK" && h.blockStatusOf("b/sid.e1:reasoning") == "up" && len(s.notices) == 1
	})
	if n := h.snapshot().notices; !strings.Contains(n[0], "reasoning_end") || !strings.Contains(n[0], "handler broke") {
		t.Fatalf("handler errors are reported: %v", n)
	}
	eventually(t, "the slow handler to finish alone", func() bool { return h.blockStatusOf("b/sid.e1:text") == "slow done" })
	// null clears.
	write(t, filepath.Join(dir, "c.ts"), `export default (atto: any) => atto.registerCommand("c", { handler: (a: string, ctx: any) => { ctx.ui.setBlockDisplay("x", null); ctx.ui.setBlockStatus("x", null); } })`)
	m.Reload()
	h.mu.Lock()
	h.blockDisplay["c/x"], h.blockStatus["c/x"] = "stale", "stale"
	h.mu.Unlock()
	m.RunCommand("c", "")
	eventually(t, "null to clear", func() bool { return h.blockDisplayOf("c/x") == "" && h.blockStatusOf("c/x") == "" })
}

func (h *fakeHost) blockDisplayOf(k string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.blockDisplay[k]
}

func (h *fakeHost) blockStatusOf(k string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.blockStatus[k]
}

func TestCompleteSuccessAndFailures(t *testing.T) {
	dir, cwd := env(t)
	srv := newSideServer(t)
	sideModels(t, srv.URL)
	write(t, filepath.Join(dir, "c.ts"), `
export default function (atto: any) {
  atto.on("user_prompt", async (e: any) => {
    const out: string[] = [];
    const go = async (opts: any) => {
      try { out.push((await atto.complete(opts)).text); } catch (err: any) { out.push("ERR " + err.message); }
    };
    await go({ model: "s/m", prompt: "hi", system: "be brief", maxTokens: 50, reasoningEffort: "low" });
    await go({ model: "d/m", prompt: "hi" });
    await go({ model: "s/m", prompt: "500" });
    await go({ model: "s/m", prompt: "slow", timeoutMs: 300 });
    await go({ model: "nope/x", prompt: "hi" });
    await go({ prompt: "hi" });
    return out.join("\n");
  });
}
`)
	m := load(t, cwd, newHost(false))
	m.mu.Lock()
	m.to = 20 * time.Second // the handler makes several requests in a row
	m.mu.Unlock()
	start := time.Now()
	o := m.UserPrompt(context.Background(), "go")
	lines := strings.Split(o.Context, "\n")
	if len(lines) != 6 {
		t.Fatalf("%q (%v)", o.Context, o.Notices)
	}
	if lines[0] != "re:hi" {
		t.Errorf("success: %q", lines[0])
	}
	for i, want := range []string{"connection refused", "500", "timed out after 300ms", `unknown model "nope/x"`, "pass {model"} {
		if !strings.HasPrefix(lines[i+1], "ERR ") || !strings.Contains(lines[i+1], want) {
			t.Errorf("failure %d: %q, want %q", i+1, lines[i+1], want)
		}
	}
	t.Logf("took %s\n%s", time.Since(start), o.Context)

	// The request: the system prompt and the prompt only, no tools, the
	// maxTokens and effort asked for.
	reqs := srv.requests()
	body := reqs[0]
	msgs := body["messages"].([]any)
	if len(msgs) != 2 || body["tools"] != nil || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "hi" {
		t.Errorf("request %v", body)
	}
	if body["reasoning_effort"] != "low" || body["max_tokens"] != float64(50) {
		t.Errorf("effort and maxTokens: %v", body)
	}

	// A request that timed out was cancelled: the server saw the
	// connection go.
	eventually(t, "the hung request to be cancelled", func() bool { return srv.canceled.Load() == 1 })

	// Counted per model, failures too.
	var calls []CompleteStat
	for _, in := range m.Report() {
		if in.Source != Builtin {
			calls = in.Completes
		}
	}
	got := map[string]CompleteStat{}
	for _, c := range calls {
		got[c.Model] = c
	}
	if got["s/m"].Calls != 3 || got["s/m"].Failed != 2 || got["d/m"].Calls != 1 || got["d/m"].Failed != 1 {
		t.Errorf("counts %+v", calls)
	}
}

func TestCompleteConcurrencyLimit(t *testing.T) {
	dir, cwd := env(t)
	srv := newSideServer(t)
	sideModels(t, srv.URL)
	write(t, filepath.Join(dir, "c.ts"), `
export default function (atto: any) {
  atto.registerCommand("one", { handler: async () => {
    await Promise.all([1, 2, 3].map((i) => atto.complete({ model: "s/m", prompt: "wait" })));
    atto.log("done one");
  } });
  atto.registerCommand("two", { handler: async () => {
    atto.setCompleteConcurrency(2);
    await Promise.all([1, 2, 3, 4].map((i) => atto.complete({ model: "s/m", prompt: "wait" })));
    atto.log("done two");
  } });
}
`)
	m := load(t, cwd, newHost(false))
	count := func() int {
		for _, in := range m.Report() {
			for _, c := range in.Completes {
				return c.Calls
			}
		}
		return 0
	}
	m.RunCommand("one", "")
	eventually(t, "three requests", func() bool { return count() == 3 })
	if n := srv.maxFlight.Load(); n != 1 {
		t.Fatalf("default limit: %d in flight at once", n)
	}
	m.RunCommand("two", "")
	eventually(t, "seven requests", func() bool { return count() == 7 })
	if n := srv.maxFlight.Load(); n != 2 {
		t.Fatalf("limit 2: %d in flight at once", n)
	}
}

func TestCompleteCancelledOnReload(t *testing.T) {
	dir, cwd := env(t)
	srv := newSideServer(t)
	sideModels(t, srv.URL)
	write(t, filepath.Join(dir, "c.ts"), `
export default function (atto: any) {
  atto.registerCommand("go", { handler: async () => { await atto.complete({ model: "s/m", prompt: "slow", timeoutMs: 20000 }); } });
}
`)
	h := newHost(false)
	m := load(t, cwd, h)
	m.RunCommand("go", "")
	eventually(t, "the request to arrive", func() bool { return len(srv.requests()) == 1 })
	m.Reload()
	eventually(t, "the request to be cancelled by the reload", func() bool { return srv.canceled.Load() == 1 })
	// A request left in the queue when the session ends is dropped too.
	m.RunCommand("go", "")
	eventually(t, "another request", func() bool { return len(srv.requests()) == 2 })
	m.SessionEnd("clear")
	eventually(t, "the request to be cancelled by session end", func() bool { return srv.canceled.Load() == 2 })
	time.Sleep(100 * time.Millisecond)
}
