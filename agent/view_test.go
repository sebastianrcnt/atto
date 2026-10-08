package agent

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
)

// viewAgent is an agent whose shell runs the test binary as atto view
// (see TestMain) in a directory holding shot.png.
func viewAgent(t *testing.T, url string, input ...string) *Agent {
	t.Helper()
	dir := t.TempDir()
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 5, 4)))
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a := New(config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: url},
		Model: config.Model{ID: "m", ContextWindow: 100000, Input: input}}, "", dir)
	a.SetSession("s", []string{"VIEW_EXE=" + exe})
	return a
}

func viewCommand(kind shell.Kind) string {
	switch kind {
	case shell.PowerShell:
		return `& "$env:VIEW_EXE" _view shot.png`
	case shell.Cmd:
		return `"%VIEW_EXE%" _view shot.png`
	default:
		return `"$VIEW_EXE" _view shot.png`
	}
}

func viewDirCommand(kind shell.Kind) string {
	switch kind {
	case shell.PowerShell:
		return `Write-Output "dir=$env:ATTO_VIEW_DIR"; if (!(Test-Path -PathType Container $env:ATTO_VIEW_DIR)) { exit 1 }`
	case shell.Cmd:
		return `echo dir=%ATTO_VIEW_DIR% & if not exist "%ATTO_VIEW_DIR%\" exit /b 1`
	default:
		return `echo "dir=$ATTO_VIEW_DIR"; test -d "$ATTO_VIEW_DIR"`
	}
}

func TestViewCommands(t *testing.T) {
	for _, tt := range []struct {
		kind shell.Kind
		view string
		dir  string
	}{
		{shell.Bash, `"$VIEW_EXE" _view shot.png`, `echo "dir=$ATTO_VIEW_DIR"; test -d "$ATTO_VIEW_DIR"`},
		{shell.Sh, `"$VIEW_EXE" _view shot.png`, `echo "dir=$ATTO_VIEW_DIR"; test -d "$ATTO_VIEW_DIR"`},
		{shell.PowerShell, `& "$env:VIEW_EXE" _view shot.png`, `Write-Output "dir=$env:ATTO_VIEW_DIR"; if (!(Test-Path -PathType Container $env:ATTO_VIEW_DIR)) { exit 1 }`},
		{shell.Cmd, `"%VIEW_EXE%" _view shot.png`, `echo dir=%ATTO_VIEW_DIR% & if not exist "%ATTO_VIEW_DIR%\" exit /b 1`},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := viewCommand(tt.kind); got != tt.view {
				t.Fatalf("view command %q, want %q", got, tt.view)
			}
			if got := viewDirCommand(tt.kind); got != tt.dir {
				t.Fatalf("directory command %q, want %q", got, tt.dir)
			}
		})
	}
}

func TestViewAttachesToToolResult(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	srv, seen := fakeServer(t, toolCall(viewCommand(shell.Default().Kind)), text("a gray square"), text("NOTES"))
	a := viewAgent(t, srv.URL, "text", "image")
	w := session.New(t.TempDir())
	t.Cleanup(w.Close)
	a.Record = w.Append
	var ended ToolEnd
	err := a.Run(context.Background(), "look at it", func(ev any) {
		if e, ok := ev.(ToolEnd); ok {
			ended = e
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ended.Images) != 1 || ended.Images[0].Name != "shot.png" || ended.Images[0].Width != 5 {
		t.Fatalf("ToolEnd images %+v", ended.Images)
	}

	// Chat completions takes no images in tool messages: the result is
	// text, and a user message after it carries the image.
	msgs := seen()[1]
	if len(msgs) != 5 || msgs[3]["role"] != "tool" || !strings.Contains(msgs[3]["content"].(string), "attached shot.png") {
		t.Fatalf("second request: %v", msgs)
	}
	parts := contentParts(msgs[4])
	if msgs[4]["role"] != "user" || len(parts) != 2 || parts[1]["type"] != "image_url" ||
		!strings.HasPrefix(parts[1]["image_url"].(map[string]any)["url"].(string), "data:image/png;base64,") {
		t.Fatalf("image message: %v", msgs[4])
	}
	if b := a.Breakdown(); b.Images != imageChars {
		t.Fatalf("breakdown images %d", b.Images)
	}

	// The session holds a reference to the stored image, and a resumed
	// session sends the same request.
	w.Close()
	data, _ := os.ReadFile(w.Path)
	im := ended.Images[0]
	if !bytes.Contains(data, []byte(`"file":"`+im.File+`"`)) || bytes.Contains(data, []byte("base64")) {
		t.Fatalf("session: %s", data)
	}
	if _, err := os.Stat(filepath.Join(images.Dir(), im.File)); err != nil {
		t.Fatal("image not stored:", err)
	}
	_, entries, err := session.Load(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	b := viewAgent(t, srv.URL, "text", "image")
	b.Restore(entries)
	_, r1 := a.request()
	_, r2 := b.request()
	if !reflect.DeepEqual(r1.Messages[1:], r2.Messages[1:]) { // the prompts name different directories
		t.Fatalf("restored request differs:\n%+v\n%+v", r1.Messages, r2.Messages)
	}

	// Compaction keeps no tool results, so no images.
	if err := b.Compact(context.Background(), func(any) {}); err != nil {
		t.Fatal(err)
	}
	for _, m := range b.messages {
		if len(m.Images) > 0 {
			t.Fatalf("image kept by compaction: %+v", m)
		}
	}
}

func TestViewTextModel(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	srv, seen := fakeServer(t, toolCall(viewCommand(shell.Default().Kind)), text("ok"))
	a := viewAgent(t, srv.URL) // no "image" input
	if err := a.Run(context.Background(), "look", func(any) {}); err != nil {
		t.Fatal(err)
	}
	msgs := seen()[1]
	if len(msgs) != 4 || !strings.Contains(msgs[3]["content"].(string), ViewUnsupported) {
		t.Fatalf("second request: %v", msgs)
	}
	if tm := a.messages[2]; tm.Role != "tool" || len(tm.Images) != 0 {
		t.Fatalf("tool message %+v", tm)
	}
}

func TestViewDirOnlyForForegroundCalls(t *testing.T) {
	dir := t.TempDir()
	srv, seen := fakeServer(t, toolCall(viewDirCommand(shell.Default().Kind)), text("ok"))
	a := newTestAgent(srv.URL)
	a.Cwd = dir
	if err := a.Run(context.Background(), "go", func(any) {}); err != nil {
		t.Fatal(err)
	}
	out := seen()[1][3]["content"].(string)
	if !strings.Contains(out, "atto-view-") || strings.Contains(out, "exit code") {
		t.Fatalf("output %q", out)
	}
	// The directory goes with the call.
	d := strings.TrimPrefix(strings.TrimSuffix(strings.SplitN(out, "\n", 2)[0], "\r"), "dir=")
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Fatalf("%s left behind: %v", d, err)
	}
}

func TestViewedReportsSaveFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ATTO_DIR", root)
	if err := os.WriteFile(images.Dir(), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := viewAgent(t, "", "text", "image")
	dir := t.TempDir()
	im, err := images.ReadFile(filepath.Join(a.Cwd, "shot.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := images.Drop(dir, "shot.png", im); err != nil {
		t.Fatal(err)
	}
	imgs, note := a.viewed(dir)
	if len(imgs) != 1 || !strings.Contains(note, "shot.png") || !strings.Contains(note, "will not survive resume") {
		t.Fatalf("save failure: %d images, %q", len(imgs), note)
	}
}
