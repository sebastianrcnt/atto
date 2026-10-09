//go:build noext

package extensions

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
)

func TestSlimReportsFilesWithoutLoading(t *testing.T) {
	dir, cwd := env(t)
	for _, path := range []string{filepath.Join(dir, "user.ts"), filepath.Join(cwd, ".atto/extensions/local/index.js"), filepath.Join(dir, "diff.ts")} {
		write(t, path, `throw new Error("must never run");`)
	}
	write(t, filepath.Join(dir, "ignore.d.ts"), "declaration")
	h := newHost(true)
	m := load(t, cwd, h)
	for _, list := range [][]Info{Inspect(cwd), m.Report()} {
		if len(list) != 3 {
			t.Fatalf("%+v", list)
		}
		for _, in := range list {
			if in.Status != Ignored || in.Hash != "" || len(in.Commands) != 0 {
				t.Errorf("not ignored: %+v", in)
			}
		}
		if msg := UnsupportedMessage(IgnoredCount(list)); msg != "this atto build has no extension support (slim); 3 extension files ignored" {
			t.Fatal(msg)
		}
	}
	if _, err := Approve(cwd, "local"); err == nil || !strings.Contains(err.Error(), "3 extension files ignored") {
		t.Fatalf("%v", err)
	}
	if m.RunCommand("user", "") || m.RunCommand("local", "") {
		t.Fatal("extension command loaded")
	}
	if out := m.UserPrompt(context.Background(), "hi"); out.Block || len(out.Notices) > 0 {
		t.Fatalf("%+v", out)
	}
	args := agent.BashArgs{Command: "echo hi"}
	got, _ := m.ToolCall(context.Background(), args)
	if got.Command != args.Command {
		t.Fatalf("%+v", got)
	}
	if len(h.snapshot().notices) != 0 {
		t.Fatal("extension executed")
	}
	// A file named diff never disables the native command in slim.
	if len(m.Commands()) != 2 {
		t.Fatalf("native commands: %+v", m.Commands())
	}
}
