package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/tui"
)

// extApp is a loadedApp with the extension demo.ts running, as Run sets
// it up.
func extApp(t *testing.T, src string) *App {
	t.Helper()
	a := loadedApp(t)
	writeTestFile(t, filepath.Join(config.ExtensionsDir(), "demo.ts"), src)
	a.ext = core.LoadExtensions(a.agent, newTUIHost(a))
	t.Cleanup(func() {
		a.ext.Close()
		a.doQuit()
	})
	a.ui.Do(func() {
		a.newSession("")
		a.sessionStartHook("startup")
	})
	return a
}

// within polls cond under the UI lock: extensions reach the UI
// asynchronously.
func within(t *testing.T, a *App, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		ok := false
		a.ui.Do(func() { ok = cond() })
		if ok {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func bodyText(a *App) string {
	var out []string
	for _, c := range a.ui.Body.Children {
		out = append(out, c.Render(100)...)
	}
	return plainLines(out)
}

const demoExtension = `
export default function (atto: any) {
  atto.on("session_start", (e: any, ctx: any) => ctx.ui.setStatus("mode", "demo:" + e.reason));
  atto.registerCommand("demo", {
    description: "Demo things",
    handler: async (args: string, ctx: any) => {
      ctx.ui.setWidget("w", ["widget " + args]);
      const pick = await ctx.ui.select("Pick one", ["red", "green"]);
      const ok = await ctx.ui.confirm("Sure?");
      const name = await ctx.ui.input("Name?");
      ctx.ui.notify("picked " + pick + " " + ok + " " + name, "warning");
    },
  });
  atto.registerCommand("model", { handler: () => ctx.ui.notify("shadowed") });
}
`

func TestExtensionInTUI(t *testing.T) {
	a := extApp(t, demoExtension)
	within(t, a, "the status item", func() bool {
		return strings.Contains(plainLines(a.renderStatus(100)), "demo:startup")
	})

	// Listed with the built-in commands, marked as an extension's; a
	// built-in name stays the built-in's.
	a.ui.Do(func() { a.editor.SetText("/dem") })
	var list string
	a.ui.Do(func() { list = plainLines(a.renderSuggestions(100)) })
	if !strings.Contains(list, "/demo [args]") || !strings.Contains(list, "Demo things (extension demo)") {
		t.Fatalf("command list:\n%s", list)
	}
	for _, c := range a.extensionCommands() {
		if c.name == "model" {
			t.Fatal("/model stays the built-in")
		}
	}

	a.ui.Do(func() { a.editor.SetText(""); a.runCommand("/demo now") })
	within(t, a, "the select dialog", func() bool { return a.modal != nil })
	var shown string
	a.ui.Do(func() { shown = plainLines(a.renderInput(100)) + "\n" + plainLines(a.renderWidgets(100)) })
	if !strings.Contains(shown, "Pick one") || !strings.Contains(shown, "green") || !strings.Contains(shown, "(demo)") {
		t.Fatalf("dialog:\n%s", shown)
	}
	a.ui.Do(func() {
		if w := plainLines(a.renderWidgets(100)); w != "" {
			t.Errorf("widgets hide behind a dialog: %q", w)
		}
		a.modal.HandleInput("\x1b[B") // down: green
		a.modal.HandleInput("\r")
	})
	within(t, a, "the confirm dialog", func() bool { return a.modal != nil && strings.Contains(plainLines(a.renderInput(100)), "Sure?") })
	a.ui.Do(func() { a.modal.HandleInput("\r") }) // Yes
	within(t, a, "the input dialog", func() bool { return a.modal != nil && strings.Contains(plainLines(a.renderInput(100)), "Name?") })
	a.ui.Do(func() {
		for _, k := range []string{"A", "n", "n", "\r"} {
			a.modal.HandleInput(k)
		}
	})
	within(t, a, "the notice", func() bool { return strings.Contains(bodyText(a), "[demo] picked green true Ann") })
	a.ui.Do(func() {
		if a.modal != nil {
			t.Error("the dialog closed")
		}
		if w := strings.TrimSpace(plainLines(a.renderWidgets(100))); w != "widget now" {
			t.Errorf("widget %q", w)
		}
	})

	// The Loaded block lists it; /reload disposes of it: its status item
	// and widget go, and the new code runs.
	bs := loadedBlocks(a)
	if got := plainLines(bs[len(bs)-1].Render(100)); !strings.Contains(got, "Extensions  3: demo, autorename, diff") {
		t.Fatalf("loaded block:\n%s", got)
	}
	writeTestFile(t, filepath.Join(config.ExtensionsDir(), "demo.ts"), `export default (atto: any) => atto.registerCommand("demo2", { handler() {} })`)
	a.ui.Do(func() { a.runCommand("/reload") })
	within(t, a, "the old extension's UI to go", func() bool {
		return !strings.Contains(plainLines(a.renderStatus(100)), "demo:") && a.renderWidgets(100) == nil
	})
	bs = loadedBlocks(a)
	if got := plainLines(bs[len(bs)-1].Render(100)); !strings.Contains(got, "changed  extension demo") {
		t.Fatalf("reload block:\n%s", got)
	}
	if cs := a.extensionCommands(); len(cs) != 3 || cs[0].name != "demo2" || cs[2].name != "diff" {
		t.Fatalf("%+v", cs)
	}
}

func TestExtensionDialogWhileModalOpen(t *testing.T) {
	a := extApp(t, `export default (atto: any) => atto.registerCommand("ask", { handler: async (_a: string, ctx: any) => ctx.ui.notify("got " + await ctx.ui.confirm("x?")) })`)
	a.ui.Do(func() {
		a.openModal(&tui.SelectList{Title: "busy"})
		a.runCommand("/ask")
	})
	within(t, a, "the default answer", func() bool { return strings.Contains(bodyText(a), "[demo] got false") })
}

func TestExtensionsCommand(t *testing.T) {
	a := extApp(t, demoExtension)
	proj := filepath.Join(a.cwd, ".atto", "extensions", "local.js")
	writeTestFile(t, proj, `export default (atto) => atto.registerCommand("local", { handler() {} })`)
	a.ui.Do(func() { a.runCommand("/reload") })
	a.ui.Do(func() { a.runCommand("/extensions") })
	got := bodyText(a)
	for _, s := range []string{"demo  loaded · user", "local  needs approval: /extensions approve local"} {
		if !strings.Contains(got, s) {
			t.Fatalf("lacks %q:\n%s", s, got)
		}
	}
	a.ui.Do(func() { a.runCommand("/extensions approve local") })
	within(t, a, "the approved extension", func() bool {
		for _, c := range a.extensionCommands() {
			if c.name == "local" {
				return true
			}
		}
		return false
	})
}
