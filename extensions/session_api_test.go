//go:build !noext

package extensions

import (
	"strings"
	"testing"
)

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
