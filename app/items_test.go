package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/tui"
)

// transcriptLines renders the conversation's blocks, without atto's own
// notices (which differ: "Worked for" live, "Resumed session" after).
func transcriptLines(a *App) string {
	var out []string
	for _, c := range a.ui.Body.Children {
		g, ok := c.(gap)
		if !ok {
			continue
		}
		if _, notice := g.Component.(*noticeBlock); notice {
			continue
		}
		for _, l := range g.Render(80) {
			out = append(out, strings.TrimRight(tui.StripEscapes(l), " "))
		}
	}
	return strings.Join(out, "\n")
}

// TestLiveBlocksMatchResume runs a turn against a scripted model, then
// resumes its session in another App: the transcript looks the same.
func TestLiveBlocksMatchResume(t *testing.T) {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		i := n
		mu.Unlock()
		if i == 1 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Let me look."}}]}`+"\n\n")
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"description\":\"Say hi\",\"command\":\"echo hi; exit 4\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"It exited with 4."},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	a := treeApp(t)
	a.agent.SetModel(config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: srv.URL}, Model: config.Model{ID: "m", ContextWindow: 100000}})
	a.ui.Do(func() { a.startTurn("why does it fail?", nil) })
	deadline := time.Now().Add(10 * time.Second)
	for {
		var busy bool
		a.ui.Do(func() { busy = a.turns.Busy })
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var live string
	a.ui.Do(func() { live = transcriptLines(a) })
	for _, want := range []string{"why does it fail?", "• Let me look.", "✗ Say hi", "exit 4", "└ hi", "• It exited with 4."} {
		if !strings.Contains(live, want) {
			t.Fatalf("live transcript lacks %q:\n%s", want, live)
		}
	}

	b := &App{ui: tui.New(nullTerm{}), agent: agent.New(config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}, "", a.cwd),
		cwd: a.cwd, quit: make(chan struct{})}
	b.build()
	b.newSession("")
	b.resume(a.sess.Path)
	t.Cleanup(b.closeSession)
	if got := transcriptLines(b); got != live {
		t.Fatalf("resumed:\n%s\nlive:\n%s", got, live)
	}
}
