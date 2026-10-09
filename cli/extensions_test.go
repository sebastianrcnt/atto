//go:build !noext

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
)

// atto -p runs extensions without a UI: dialogs get their defaults,
// notices go to stderr, and the events fire.
func TestRunPrintExtensions(t *testing.T) {
	bodies := imageModelServer(t, `["text"]`)
	cwd := t.TempDir()
	t.Chdir(cwd)
	ext := filepath.Join(os.Getenv("ATTO_DIR"), "extensions", "ctx.ts")
	if err := os.MkdirAll(filepath.Dir(ext), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `export default function (atto: any) {
  atto.on("session_start", (e: any) => atto.fs.writeFile("log.txt", e.reason));
  atto.on("session_end", (e: any) => atto.fs.writeFile("log.txt", atto.fs.readFile("log.txt") + "," + e.reason));
  atto.on("user_prompt", async (_e: any, ctx: any) => {
    ctx.ui.notify("asked: " + await ctx.ui.confirm("ok?") + " " + ctx.hasUI);
    return "Extra context from an extension.";
  });
}`
	if err := os.WriteFile(ext, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	quiet(t)
	errOut, err := os.CreateTemp(t.TempDir(), "err")
	if err != nil {
		t.Fatal(err)
	}
	defer errOut.Close()
	os.Stderr = errOut
	if err := RunPrint(PrintOptions{Prompt: "hi", NoSave: true}); err != nil {
		t.Fatal(err)
	}
	if b := bodies(); len(b) != 1 || !strings.Contains(b[0], `hi\n\nExtra context from an extension.`) {
		t.Fatalf("request: %v", b)
	}
	if data, _ := os.ReadFile(errOut.Name()); !strings.Contains(string(data), "[ctx] asked: false false\n") {
		t.Fatalf("stderr %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(cwd, "log.txt")); string(data) != "startup,other" {
		t.Fatalf("events %q", data)
	}
}

func TestExtensionsCLI(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("ATTO_DIR", filepath.Join(root, "atto"))
	t.Setenv(config.EnvAgent, "")
	cwd := filepath.Join(root, "proj")
	for path, text := range map[string]string{
		filepath.Join(cwd, ".git", "HEAD"):                            "x",
		filepath.Join(cwd, ".atto", "extensions", "local.ts"):         "export default () => {}",
		filepath.Join(root, "atto", "extensions", "mine", "index.ts"): "export default () => {}",
		filepath.Join(root, "atto", "extensions", "bad.js"):           "export default (",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwd)
	run := func(args ...string) (string, error) {
		var out strings.Builder
		err := RunExtensions(args, &out)
		return out.String(), err
	}
	out, err := run()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bad  failed: ", "bad.js:1:", "mine  ok · user", "diff  ok · builtin · builtin/diff.go", "local  needs approval · project", "atto extensions approve local"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}

	t.Setenv(config.EnvAgent, "1")
	if _, err := run("approve", "local"); err == nil || !strings.Contains(err.Error(), "approved by the user") {
		t.Fatalf("an agent cannot approve: %v", err)
	}
	t.Setenv(config.EnvAgent, "")
	if out, err := run("approve", "local"); err != nil || !strings.Contains(out, "Approved local") {
		t.Fatal(out, err)
	}
	if out, _ := run("list", "-json"); !strings.Contains(out, `"status": "ready"`) || strings.Contains(out, extensions.NeedsApproval) {
		t.Fatalf("approved:\n%s", out)
	}
	if out, _ := run("types"); out != extensions.Types {
		t.Fatal("types prints atto.d.ts")
	}
	if out, _ := run("docs"); !strings.HasPrefix(out, "# Writing atto extensions") {
		t.Fatal("docs prints the guide")
	}
	if out, err := run("source", "diff"); err != nil || !strings.Contains(out, "nativeDiff") || !strings.Contains(out, `"diff"`) {
		t.Fatalf("source diff: %v\n%s", err, out)
	}
	if _, err := run("source", "nope"); err == nil {
		t.Fatal("unknown built-in extension")
	}
	if _, err := run("source"); err == nil {
		t.Fatal("source needs a name")
	}
	if _, err := run("nope"); err == nil {
		t.Fatal("usage")
	}

	var ctx strings.Builder
	if err := RunContext(nil, &ctx); err != nil || !strings.Contains(ctx.String(), "mine") || !strings.Contains(ctx.String(), "ready · user") {
		t.Fatalf("atto context lists them: %v\n%s", err, ctx.String())
	}
}

// message_end fires in atto -p too (there is no UI: the display calls are
// no-ops), once, with the block's ID, text and model.
func TestRunPrintBlockEvents(t *testing.T) {
	imageModelServer(t, `["text"]`)
	cwd := t.TempDir()
	t.Chdir(cwd)
	ext := filepath.Join(os.Getenv("ATTO_DIR"), "extensions", "blocks.ts")
	if err := os.MkdirAll(filepath.Dir(ext), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `export default function (atto: any) {
  atto.on("message_end", async (e: any, ctx: any) => {
    atto.fs.writeFile("ev.txt", [e.blockId, e.text, e.model].join("|"));
    await new Promise((r) => setTimeout(r, 50));
    atto.fs.writeFile("ev2.txt", "after");
  });
  atto.on("reasoning_end", () => atto.fs.writeFile("reasoning.txt", "fired"));
}`
	if err := os.WriteFile(ext, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	quiet(t)
	if err := RunPrint(PrintOptions{Prompt: "hi", NoSave: true}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(cwd, "ev.txt"))
	parts := strings.Split(string(data), "|")
	if len(parts) != 3 || !strings.HasSuffix(parts[0], ".n1:text") || parts[1] != "a gray square" || parts[2] != "fake/m" {
		t.Fatalf("event %q", data)
	}
	if _, err := os.Stat(filepath.Join(cwd, "reasoning.txt")); err == nil {
		t.Fatal("no reasoning, no reasoning_end")
	}
}
