package web

import (
	"os"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/server/web/internal/build"
)

func TestDistIsCurrent(t *testing.T) {
	hash, e := build.SourceHash(os.DirFS("."))
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("dist/source.sha256")
	if e != nil {
		t.Fatal(e)
	}
	if strings.TrimSpace(string(b)) != hash {
		t.Fatal("web dist is stale: go generate ./server/web")
	}
}
func TestNoHTMLInjectionOrRemoteRuntime(t *testing.T) {
	for _, name := range []string{"src/main.ts", "src/elements.ts", "dist/app.js"} {
		b, e := os.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "eval(", "serviceWorker"} {
			if strings.Contains(string(b), bad) {
				t.Fatalf("%s contains %s", name, bad)
			}
		}
	}
}
