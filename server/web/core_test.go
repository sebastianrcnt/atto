package web

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
	"github.com/sebastianrcnt/atto/ui/uitest"
)

func runTS(t *testing.T, entry string, after func(*goja.Runtime)) {
	t.Helper()
	res := api.Build(api.BuildOptions{EntryPoints: []string{entry}, Bundle: true, Write: false, Outfile: "test.js", Format: api.FormatIIFE, Target: api.ES2017, LogLevel: api.LogLevelSilent})
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	vm := goja.New()
	shim, e := os.ReadFile("test/dom.js")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = vm.RunString(string(shim)); e != nil {
		t.Fatal(e)
	}
	if entry == "test/app_test.ts" {
		sock, e := os.ReadFile("test/socket.js")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = vm.RunString(string(sock)); e != nil {
			t.Fatal(e)
		}
		if t.Name() == "TestTypeScriptPageWorkflow/phone" {
			vm.Set("innerWidth", 400)
		}
	}
	b, e := json.Marshal(uitest.Catalog())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = vm.RunString("globalThis.fixture=" + string(b)); e != nil {
		t.Fatal(e)
	}
	if _, e = vm.RunString(string(res.OutputFiles[0].Contents)); e != nil {
		t.Fatal(e)
	}
	after(vm)
	if !vm.Get("testDone").ToBoolean() {
		t.Fatal("TS tests did not finish")
	}
}
func TestTypeScriptCoreAndCatalog(t *testing.T) {
	runTS(t, "test/core_test.ts", func(vm *goja.Runtime) {
		if _, e := vm.RunString("checkPromises()"); e != nil {
			t.Fatal(e)
		}
	})
}

func TestTypeScriptPageWorkflow(t *testing.T) {
	for _, width := range []string{"phone", "desktop"} {
		t.Run(width, func(t *testing.T) {
			runTS(t, "test/app_test.ts", func(vm *goja.Runtime) {
				for _, step := range []string{"openPage", "checkPage", "checkTurn", "checkMutation", "checkPageMerge", "checkDialog", "checkAction"} {
					for range 20 {
						if _, e := vm.RunString("flush()"); e != nil {
							t.Fatalf("before %s: %v", step, e)
						}
					}
					if _, e := vm.RunString(fmt.Sprint(step, "()")); e != nil {
						t.Fatalf("%s: %v", step, e)
					}
				}
			})
		})
	}
}
