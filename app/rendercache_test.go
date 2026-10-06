package app

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
)

func hasCommand(cmds []command, name string) bool {
	return slices.ContainsFunc(cmds, func(c command) bool { return c.name == name })
}

func TestAllCommandsCached(t *testing.T) {
	a := extApp(t, demoExtension)
	within(t, a, "the extension command", func() bool { return hasCommand(a.allCommands(), "demo") })
	var first, second []command
	a.ui.Do(func() { first, second = a.allCommands(), a.allCommands() })
	if &first[0] != &second[0] {
		t.Fatal("the list is rebuilt on every call")
	}

	// A command registered at run time shows without a reload.
	writeTestFile(t, filepath.Join(config.ExtensionsDir(), "demo.ts"),
		`export default (atto: any) => { atto.registerCommand("demo", { handler() {} }); atto.registerCommand("later", { handler() {} }) }`)
	a.ui.Do(func() { a.runCommand("/reload") })
	within(t, a, "the new extension command", func() bool { return hasCommand(a.allCommands(), "later") })

	// The open suggestion list follows the commands.
	a.ui.Do(func() {
		a.editor.SetText("/lat")
		if got := plainLines(a.renderSuggestions(100)); !strings.Contains(got, "/later") {
			t.Errorf("suggestions:\n%s", got)
		}
	})

	// And they go when the extensions do.
	a.ui.Do(func() { a.ext.Close() })
	within(t, a, "the commands to go", func() bool { return !hasCommand(a.allCommands(), "later") })
}

func TestBuiltinStatusCachedUntilInputsChange(t *testing.T) {
	price := &ai.ModelCost{Input: 1, Output: 2}
	a := statusApp(t, price)
	// fresh is what an uncached call returns.
	fresh := func(first, width int) string {
		m, effort := a.agent.Current()
		return strings.Join(a.buildStatus(m, effort, first, width), "\n")
	}
	got := func(first, width int) string { return strings.Join(a.builtinStatus(first, width), "\n") }
	check := func(what string) {
		t.Helper()
		for _, w := range []int{60, 100, 160} {
			if g, f := got(w, w), fresh(w, w); g != f {
				t.Errorf("after %s, width %d: cached\n%q\nfresh\n%q", what, w, g, f)
			}
		}
	}
	check("start")
	check("repeat") // from the cache

	// Rows from the cache are the caller's to change.
	r := a.builtinStatus(160, 160)
	r[0] = "changed"
	if a.builtinStatus(160, 160)[0] == "changed" {
		t.Fatal("a caller's edit reached the cache")
	}

	// The effort shows only for a model with levels.
	a.agent.SetModel(config.ModelRef{Model: config.Model{ID: "m", Name: "Orca", ContextWindow: 262000, Cost: price, Efforts: []string{"low", "high"}}})
	a.agent.SetEffort("low")
	check("levels")
	before := got(160, 160)
	a.agent.SetEffort("high")
	if got(160, 160) == before {
		t.Error("effort: the status line did not change")
	}
	check("effort")
	// The same counts, but a level mapped to nil: no levels left, no effort (#4).
	x := "x"
	a.agent.SetModel(config.ModelRef{Model: config.Model{ID: "m", Name: "Orca", ContextWindow: 262000, Cost: price, Efforts: []string{"low"}, EffortMap: map[string]*string{"low": &x}}})
	a.agent.SetEffort("low")
	check("mapped level")
	got(160, 160) // cached at the width compared next
	a.agent.SetModel(config.ModelRef{Model: config.Model{ID: "m", Name: "Orca", ContextWindow: 262000, Cost: price, Efforts: []string{"low"}, EffortMap: map[string]*string{"low": nil}}})
	a.agent.SetEffort("low")
	if g, f := got(160, 160), fresh(160, 160); g != f {
		t.Errorf("after a level mapped to nil: cached\n%q\nfresh\n%q", g, f)
	}

	for what, change := range map[string]func(){
		"usage":     func() { a.usage.add(provider.Usage{PromptTokens: 1000, CompletionTokens: 777, Cost: 1}) },
		"context":   func() { a.ctxTokens = 250000 },
		"session":   func() { a.sessName = "another" },
		"branch":    func() { a.gitBranch = "dev" },
		"directory": func() { a.cwd = "/work/other" },
		"model": func() {
			a.agent = agent.New(config.ModelRef{Model: config.Model{ID: "n", Name: "Narwhal", ContextWindow: 8000}}, "", t.TempDir())
		},
		"memory":      func() { rssBytes.Store(rssBytes.Load() + 300<<20) },
		"cost":        func() { a.usage.cost = 0.5 },
		"cache label": func() { a.usage.last = provider.Usage{PromptTokens: 100, CachedTokens: 10} },
	} {
		before := got(160, 160)
		change()
		after := got(160, 160)
		if after == before {
			t.Errorf("%s: the status line did not change", what)
		}
		check(what)
	}
}

func BenchmarkBuiltinStatus(b *testing.B) {
	a := statusApp(&testing.T{}, &ai.ModelCost{Input: 1, Output: 2})
	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			a.builtinStatus(100, 100)
		}
	})
	b.Run("rebuilt", func(b *testing.B) {
		m, effort := a.agent.Current()
		b.ReportAllocs()
		for b.Loop() {
			a.buildStatus(m, effort, 100, 100)
		}
	})
}

func BenchmarkAllCommands(b *testing.B) {
	a := statusApp(&testing.T{}, nil)
	b.ReportAllocs()
	for b.Loop() {
		a.allCommands()
	}
}
