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

// ctx.session reads the name and the conversation's text from the
// session file, and setName goes to the front end.
func TestSessionNameAndMessages(t *testing.T) {
	dir, cwd := env(t)
	write(t, dir+"/s.ts", `
export default function (atto: Atto) {
  atto.registerCommand("show", { handler: (_a, ctx) => {
    ctx.ui.notify("name=" + ctx.session.name);
    ctx.ui.notify("all=" + JSON.stringify(ctx.session.messages()));
    ctx.ui.notify("last=" + JSON.stringify(ctx.session.messages(1)));
    ctx.session.setName("  new   name ");
    try { ctx.session.setName("   "); } catch (e) { ctx.ui.notify("empty refused"); }
  }});
}`)
	h := newHost(true)
	m := load(t, cwd, h)
	m.SetSession(savedSession(t, cwd))
	m.RunCommand("show", "")
	eventually(t, "setName", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.names) == 1 })
	if h.names[0] != "new name" {
		t.Fatalf("names %q", h.names)
	}
	eventually(t, "the notices", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.notices) >= 4 })
	h.mu.Lock()
	log := strings.Join(h.notices, "\n")
	h.mu.Unlock()
	for _, want := range []string{
		"name=old name",
		`all=[{"role":"user","text":"fix the parser crash on empty input"},{"role":"assistant","text":"Fixed: the parser checks for EOF first."}]`,
		`last=[{"role":"assistant","text":"Fixed: the parser checks for EOF first."}]`,
		"empty refused",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
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
