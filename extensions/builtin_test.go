//go:build !noext

package extensions

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
)

func named(list []Info, name string) []Info {
	var out []Info
	for _, in := range list {
		if in.Name == name {
			out = append(out, in)
		}
	}
	return out
}

func TestBuiltinListedAndLoadedWithoutApproval(t *testing.T) {
	_, cwd := env(t)
	for _, list := range [][]Info{Inspect(cwd), load(t, cwd, newHost(true)).Report()} {
		got := named(list, "diff")
		if len(got) != 1 || got[0].Source != Builtin || got[0].Hash == "" {
			t.Fatalf("%+v", list)
		}
		if st := got[0].Status; st != Ready && st != Loaded {
			t.Errorf("status %q", st)
		}
	}
	m := load(t, cwd, newHost(true))
	in := info(t, m, "diff")
	if in.Status != Loaded || !slices.Equal(in.Commands, []string{"diff"}) {
		t.Errorf("%+v", in)
	}
	if cs := m.Commands(); len(cs) != 2 || cs[1].Name != "diff" || cs[1].Ext != "diff" || cs[1].Description == "" || cs[0].Name != "autorename" {
		t.Errorf("%+v", cs)
	}
	// Nothing to approve, and no types file written for it.
	if _, err := Approve(cwd, "diff"); err == nil {
		t.Error("a built-in extension needs no approval")
	}
}

func TestBuiltinCanBeDisabled(t *testing.T) {
	_, cwd := env(t)
	write(t, config.SettingsPath(), `{"extensions":{"timeout":1,"disabled":["diff"]}}`)
	if got := named(Inspect(cwd), "diff"); len(got) != 1 || got[0].Status != Disabled || got[0].Source != Builtin {
		t.Fatalf("%+v", got)
	}
	m := load(t, cwd, newHost(true))
	if in := info(t, m, "diff"); in.Status != Disabled {
		t.Errorf("%+v", in)
	}
	if len(m.Commands()) != 1 || m.RunCommand("diff", "") {
		t.Error("a disabled extension has no commands")
	}
}

func TestUserAndProjectExtensionsOverrideBuiltin(t *testing.T) {
	dir, cwd := env(t)

	// A user extension of the same name replaces it.
	write(t, filepath.Join(dir, "diff.ts"), `export default (atto: any) => atto.registerCommand("diff", { description: "mine", handler: (_a: string, ctx: any) => ctx.ui.notify("my diff") })`)
	h := newHost(true)
	m := load(t, cwd, h)
	got := named(m.Report(), "diff")
	if len(got) != 1 || got[0].Source != User || got[0].Status != Loaded {
		t.Fatalf("%+v", m.Report())
	}
	if cs := m.Commands(); len(cs) != 2 || cs[0].Description != "mine" {
		t.Errorf("%+v", cs)
	}
	m.RunCommand("diff", "")
	eventually(t, "the user's /diff", func() bool { return slices.Contains(h.snapshot().notices, "info diff: my diff") })

	// So does a project one, once it is approved; until then nothing runs.
	if err := os.Remove(filepath.Join(dir, "diff.ts")); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(cwd, ".atto", "extensions", "diff.ts")
	write(t, proj, `export default (atto: any) => atto.registerCommand("diff", { description: "project", handler() {} })`)
	got = named(Inspect(cwd), "diff")
	if len(got) != 1 || got[0].Source != Project || got[0].Status != NeedsApproval {
		t.Fatalf("%+v", got)
	}
	if _, err := Approve(cwd, "diff"); err != nil {
		t.Fatal(err)
	}
	m2 := load(t, cwd, newHost(true))
	if cs := m2.Commands(); len(cs) != 2 || cs[0].Description != "project" {
		t.Errorf("%+v", cs)
	}
}

// Loading native built-in commands must stay cheap.
func TestBuiltinStartupCost(t *testing.T) {
	_, cwd := env(t)
	timeLoads := func(settings string) time.Duration {
		write(t, config.SettingsPath(), settings)
		var runs []time.Duration
		for range 7 {
			start := time.Now()
			m := Load(Options{Cwd: cwd})
			runs = append(runs, time.Since(start))
			m.Close()
		}
		slices.Sort(runs)
		return runs[len(runs)/2]
	}
	with := timeLoads(`{"extensions":{"timeout":1}}`)
	without := timeLoads(`{"extensions":{"timeout":1,"disabled":["diff"]}}`)
	t.Logf("extension load, median of 7: with built-ins %s, without %s (+%s)", with, without, with-without)
	// The target is 30ms; the race detector slows esbuild and goja a few
	// times over, so this only guards against an order-of-magnitude slip.
	if with-without > 250*time.Millisecond {
		t.Errorf("loading the built-in extensions adds %s", with-without)
	}
	// Reading native source alone.
	spec := builtinSpecs()[0]
	start := time.Now()
	for range 5 {
		if _, err := bundleSpec(spec); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("bundling %s: %s each", spec.Path, time.Since(start)/5)
}
