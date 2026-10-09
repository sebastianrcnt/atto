package extensions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// savedSession writes a session with a name, a question, a command and an
// answer, as a conversation would leave it.
func savedSession(t *testing.T, cwd string) string {
	t.Helper()
	w := session.New(cwd)
	w.Append(session.Entry{Type: session.TypeName, Name: "old name"})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "fix the parser crash on empty input"}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "tool", Content: "secret command output"}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "Fixed: the parser checks for EOF first."}})
	w.Close()
	return w.ID
}

// /autorename asks the session's own model, without thinking, from the
// conversation's text (no command output), and names the session.
func TestAutorename(t *testing.T) {
	_, cwd := env(t)
	side := newSideServer(t)
	sideModels(t, side.URL)
	ag := agent.New(config.ModelRef{ProviderName: "s", Provider: config.Provider{BaseURL: side.URL}, Model: config.Model{ID: "m", ContextWindow: 100000}}, "", cwd)
	h := newHost(true)
	m := Load(Options{Cwd: cwd, Host: h, Agent: ag})
	t.Cleanup(m.Close)
	m.SetSession(savedSession(t, cwd))
	if !m.RunCommand("autorename", "") {
		t.Fatal("no /autorename")
	}
	eventually(t, "the name", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.names) == 1 })
	if name := h.names[0]; !strings.HasPrefix(name, "re:") || strings.Contains(name, "\n") || len(name) > 80 {
		t.Fatalf("name %q", name)
	}
	reqs := side.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	body, _ := json.Marshal(reqs[0])
	for _, want := range []string{"fix the parser crash", "checks for EOF", `named \"old name\"`, `"reasoning_effort":"none"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("request lacks %s: %s", want, body)
		}
	}
	if strings.Contains(string(body), "secret command output") {
		t.Error("command output was sent")
	}
}
