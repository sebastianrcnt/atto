//go:build !noext

package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/tui"
)

// extApp is a loadedApp with the extension demo.ts running, as Run sets
// it up.
func extUIApp(t *testing.T, src string) *App {
	cwd, _ := testEnv(t)
	writeTestFile(t, filepath.Join(config.ExtensionsDir(), "demo.ts"), src)
	return startApp(t, cwd)
}

// extensionCommands is the runtime catalog projected for the suggestions.
func (a *App) extensionCommands() []command {
	var out []command
	for _, c := range a.catalog {
		if c.Origin == "extension" {
			out = append(out, command{c.Name, c.Args, c.Desc})
		}
	}
	return out
}

func TestExtensionInTUI(t *testing.T) {
	a := extUIApp(t, demoExtension)
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
	within(t, a, "the new extension catalog", func() bool {
		cs := a.extensionCommands()
		return len(cs) == 1 && cs[0].name == "demo2"
	})
}

func TestExtensionDialogWhileModalOpen(t *testing.T) {
	a := extUIApp(t, `export default (atto: any) => atto.registerCommand("ask", { handler: async (_a: string, ctx: any) => ctx.ui.notify("got " + await ctx.ui.confirm("x?")) })`)
	a.ui.Do(func() {
		a.openModal(&tui.SelectList{Title: "busy"})
		a.runCommand("/ask")
	})
	settle(a)
	a.ui.Do(func() {
		if strings.Contains(bodyText(a), "[demo] got false") {
			t.Fatal("question auto-answered behind a picker")
		}
		a.closeModal()
	})
	within(t, a, "waiting question", func() bool { return a.modal != nil && strings.Contains(plainLines(a.renderInput(100)), "x?") })
	key(a, "\x1b")
	within(t, a, "cancelled answer", func() bool { return strings.Contains(bodyText(a), "[demo] got false") })
}

func TestExtensionsCommand(t *testing.T) {
	a := extUIApp(t, demoExtension)
	proj := filepath.Join(a.cwd, ".atto", "extensions", "local.js")
	writeTestFile(t, proj, `export default (atto) => atto.registerCommand("local", { handler() {} })`)
	a.ui.Do(func() { a.runCommand("/reload") })
	settle(a)
	a.ui.Do(func() { a.runCommand("/extensions") })
	settle(a)
	got := shown(a)
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
