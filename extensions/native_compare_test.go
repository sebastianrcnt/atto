//go:build !noext

package extensions

import (
	"encoding/json"
	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

// Keep the pre-port implementations only as test fixtures, never embedded
// into atto. Compare the same real repositories and options on both paths.
func TestNativeDiffMatchesTypeScript(t *testing.T) {
	cwd := changedRepo(t)
	nativeHost := newHost(true)
	native := load(t, cwd, nativeHost)
	src, err := os.ReadFile("testdata/diff.ts.txt")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(config.ExtensionsDir(), "diff.ts"), string(src))
	tsHost := newHost(true)
	ts := load(t, cwd, tsHost)
	for _, args := range []string{"", "sub", "--staged", "--cached", "--staged sub", "-- missing", "'sub'", "it's"} {
		a, an := runDiff(t, native, nativeHost, args)
		b, bn := runDiff(t, ts, tsHost, args)
		if a != b || an != bn {
			t.Errorf("%q: Go %+v %q; TS %+v %q", args, a, an, b, bn)
		}
	}
}
func TestNativeCleanMatchesTypeScript(t *testing.T) {
	src, err := os.ReadFile("testdata/autorename.ts.txt")
	if err != nil {
		t.Fatal(err)
	}
	code, err := bundle(api.BuildOptions{Stdin: &api.StdinOptions{Contents: string(src), Loader: api.LoaderTS}}, "reference.ts")
	if err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	vm.Set("module", map[string]any{"exports": map[string]any{}})
	if _, err := vm.RunString(code); err != nil {
		t.Fatal(err)
	}
	fn, ok := goja.AssertFunction(vm.Get("module").ToObject(vm).Get("exports").ToObject(vm).Get("clean"))
	if !ok {
		t.Fatal("no clean")
	}
	for _, input := range []string{"", "\n Title: **Fix parser crash.**\nnoise", "NAME: \"A good title.\"", "### ‘제목 입니다’.", strings.Repeat("界", 90), strings.Repeat("😀", 45), " \u2002title:\u2002name\u2002. "} {
		result, err := fn(goja.Undefined(), vm.ToValue(input))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := cleanName(input), result.String(); got != want {
			t.Errorf("%q: Go %q, TS %q", input, got, want)
		}
	}
}

func TestNativeAutorenameMatchesTypeScript(t *testing.T) {
	dir, cwd := env(t)
	side := newSideServer(t)
	sideModels(t, side.URL)
	ag := agent.New(config.ModelRef{ProviderName: "s", Provider: config.Provider{BaseURL: side.URL}, Model: config.Model{ID: "m", ContextWindow: 100000}}, "", cwd)
	sid := savedSession(t, cwd)
	nativeHost := newHost(true)
	native := Load(Options{Cwd: cwd, Host: nativeHost, Agent: ag})
	defer native.Close()
	native.SetSession(sid)
	src, err := os.ReadFile("testdata/autorename.ts.txt")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "autorename.ts"), string(src))
	tsHost := newHost(true)
	ts := Load(Options{Cwd: cwd, Host: tsHost, Agent: ag})
	defer ts.Close()
	ts.SetSession(sid)
	for _, pair := range []struct {
		m *Manager
		h *fakeHost
	}{{native, nativeHost}, {ts, tsHost}} {
		if !pair.m.RunCommand("autorename", "") {
			t.Fatal("missing command")
		}
		eventually(t, "name", func() bool { pair.h.mu.Lock(); defer pair.h.mu.Unlock(); return len(pair.h.names) == 1 })
	}
	nativeHost.mu.Lock()
	a := nativeHost.names[0]
	nativeHost.mu.Unlock()
	tsHost.mu.Lock()
	b := tsHost.names[0]
	tsHost.mu.Unlock()
	if a != b {
		t.Fatalf("Go %q; TS %q", a, b)
	}
	reqs := side.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests %d", len(reqs))
	}
	one, _ := json.Marshal(reqs[0])
	two, _ := json.Marshal(reqs[1])
	if string(one) != string(two) {
		t.Fatalf("Go %s; TS %s", one, two)
	}
}

func TestUserCommandShadowsNativeByRegistrationOrder(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "custom.ts"), `export default (atto:any)=>atto.registerCommand("diff",{description:"custom",handler(){}})`)
	m := load(t, cwd, newHost(true))
	n := 0
	for _, c := range m.Commands() {
		if c.Name == "diff" {
			n++
			if c.Description != "custom" {
				t.Fatalf("%+v", c)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d diff commands", n)
	}
}
