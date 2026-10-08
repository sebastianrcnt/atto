package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// summaryServer answers every request with reply, keeping the last
// message of each request. block makes it wait for the client to go away
// instead (to test canceling).
func summaryServer(t *testing.T, reply string, block bool) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var last []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(data, &body)
		mu.Lock()
		if n := len(body.Messages); n > 0 {
			last = append(last, body.Messages[n-1].Content)
		}
		mu.Unlock()
		if block {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\n", reply)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), last...) }
}

// summaryApp is a treeApp talking to url, with a conversation of two
// exchanges loaded into the agent.
func summaryApp(t *testing.T, url string) *App {
	a := treeApp(t)
	a.agent.SetModel(config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: url}, Model: config.Model{ID: "m", ContextWindow: 100000}})
	a.record("user", "u1")
	a.record("assistant", "a1")
	a.record("user", "u2")
	a.record("assistant", "a2")
	a.showBranch(a.loadSession())
	return a
}

func waitIdle(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var busy bool
		a.ui.Do(func() { busy = a.turns.Busy })
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pickSummary answers the "Summarize branch?" question with choice.
func pickSummary(t *testing.T, a *App, choice string) {
	t.Helper()
	sel, ok := a.modal.(*tui.SelectList)
	if !ok || sel.Title != "Summarize branch?" {
		t.Fatalf("expected the summary question, got %T", a.modal)
	}
	for i, it := range sel.Items {
		if it.Value == choice {
			sel.Selected = i
		}
	}
	sel.HandleInput("\r")
}

func TestBranchSummaryOnNavigate(t *testing.T) {
	srv, last := summaryServer(t, "## Goal\nTried u2.", false)
	a := summaryApp(t, srv.URL)
	u2, a1, a2 := entryID(t, a, "u2"), entryID(t, a, "a1"), entryID(t, a, "a2")

	a.ui.Do(func() {
		a.selectTreeEntry(u2)
		pickSummary(t, a, summaryCustom)
		in := a.modal.(labelModal)
		for _, k := range "focus on tests" {
			in.HandleInput(string(k))
		}
		in.HandleInput("\r")
	})
	waitIdle(t, a)

	reqs := last()
	if len(reqs) != 1 || !strings.Contains(reqs[0], `the user message that begins "u2"`) || !strings.Contains(reqs[0], "Additional focus: focus on tests") {
		t.Fatalf("summary request: %q", reqs)
	}
	entries := a.loadSession()
	e := entries[len(entries)-1]
	if e.Type != session.TypeBranchSummary || e.Summary != "## Goal\nTried u2." || e.Parent != a1 || e.FromID != a2 {
		t.Fatalf("last entry %+v", e)
	}
	if a.editor.Text() != "u2" {
		t.Fatalf("editor %q", a.editor.Text())
	}
	if bd := a.agent.Breakdown(); bd.Messages != 3 {
		t.Fatalf("agent has %d messages, want u1, a1 and the summary", bd.Messages)
	}
	var live string
	a.ui.Do(func() { live = transcriptLines(a) })
	if !strings.Contains(live, "⎇ Branch summary") || strings.Contains(live, "a2") {
		t.Fatalf("transcript:\n%s", live)
	}

	// The next turn carries the summary to the model, and so does a resume.
	a.ui.Do(func() { a.editor.SetText(""); a.startTurn("u3", nil) })
	waitIdle(t, a)
	b := treeApp(t)
	b.resume(a.sess.Path)
	if got := transcriptLines(b); !strings.Contains(got, "⎇ Branch summary") {
		t.Fatalf("resumed transcript:\n%s", got)
	}
	_, saved, _ := session.Load(a.sess.Path)
	b.agent.Restore(session.Active(saved))
	if bd := b.agent.Breakdown(); bd.Messages != 5 {
		t.Fatalf("resumed agent has %d messages", bd.Messages)
	}
}

func TestBranchSummaryCanceled(t *testing.T) {
	srv, last := summaryServer(t, "", true)
	a := summaryApp(t, srv.URL)
	before := len(a.loadSession())
	a.ui.Do(func() {
		a.selectTreeEntry(entryID(t, a, "u2"))
		pickSummary(t, a, summaryPlain)
	})
	for deadline := time.Now().Add(10 * time.Second); len(last()) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no summary request")
		}
	}
	a.ui.Do(func() { a.onInput("\x1b") }) // esc cancels
	waitIdle(t, a)
	if n := len(a.loadSession()); n != before {
		t.Fatalf("canceled summary changed the session: %d entries, was %d", n, before)
	}
	if _, ok := a.modal.(*treePicker); !ok {
		t.Fatalf("the tree should open again, got %T", a.modal)
	}
	if got := transcriptLines(a); strings.Contains(got, "Summarizing") || !strings.Contains(got, "a2") {
		t.Fatalf("transcript:\n%s", got)
	}
}

func TestBranchSummaryNotAsked(t *testing.T) {
	a := treeApp(t)
	a.record("user", "u1")
	a.record("assistant", "a1")
	a.record("user", "u2")
	a.record("assistant", "a2")

	// "No summary" moves as before.
	a.selectTreeEntry(entryID(t, a, "u2"))
	pickSummary(t, a, summaryNone)
	if e := a.loadSession(); e[len(e)-1].Type != session.TypeBranch {
		t.Fatalf("expected a plain branch entry, got %q", e[len(e)-1].Type)
	}

	// Nothing to summarize: the branch left holds only the branch marker.
	a.editor.SetText("")
	a.selectTreeEntry(entryID(t, a, "a2"))
	if a.modal != nil {
		t.Fatalf("asked with nothing to summarize: %T", a.modal)
	}

	// branchSummary.skipPrompt never asks.
	a.skipSummary = true
	a.selectTreeEntry(entryID(t, a, "u1"))
	if a.modal != nil || a.editor.Text() == "" {
		t.Fatalf("skipPrompt: modal %T, editor %q", a.modal, a.editor.Text())
	}

	// Esc on the question goes back to the tree.
	a.skipSummary = false
	a.record("user", "u9")
	a.selectTreeEntry(entryID(t, a, "a1"))
	a.modal.HandleInput("\x1b")
	if _, ok := a.modal.(*treePicker); !ok {
		t.Fatalf("esc should reopen the tree, got %T", a.modal)
	}
}

func TestBranchSummaryInContext(t *testing.T) {
	a := agent.New(config.ModelRef{Model: config.Model{ID: "m"}}, "", t.TempDir())
	entries := []session.Entry{
		{Type: session.TypeMessage, ID: "1", Message: &provider.Message{Role: "user", Content: "u1"}},
		{Type: session.TypeBranchSummary, ID: "2", Parent: "1", Summary: "tried X"},
	}
	a.Restore(entries)
	if bd := a.Breakdown(); bd.Messages != 2 {
		t.Fatalf("%d messages", bd.Messages)
	}
	if m := agent.BranchSummaryMessage("tried X"); !strings.HasPrefix(m.Content, agent.BranchSummaryPrefix) || !strings.Contains(m.Content, "<summary>\ntried X\n</summary>") {
		t.Fatalf("message %q", m.Content)
	}
}
