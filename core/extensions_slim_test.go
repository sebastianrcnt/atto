//go:build noext

package core

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
)

func TestSlimLoadedReportsIgnoredFiles(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	cwd := t.TempDir()
	writeFile(t, filepath.Join(config.ExtensionsDir(), "user.ts"), "throw 1")
	writeFile(t, filepath.Join(cwd, ".git/HEAD"), "x")
	writeFile(t, filepath.Join(cwd, ".atto/extensions/project.js"), "throw 1")
	ag := agent.New(config.ModelRef{}, "", cwd)
	m := LoadExtensions(ag, &extensions.Headless{})
	defer m.Close()
	l := Collect(ag, nil, "", "")
	want := "this atto build has no extension support (slim); 2 extension files ignored"
	if text := l.Text(); strings.Count(text, want) != 1 {
		t.Fatalf("%s", text)
	}
	if len(l.Warnings) != 0 {
		t.Fatalf("unexpected trust prompts: %+v", l.Warnings)
	}
}
