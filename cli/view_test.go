package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/session"
)

func runView(args ...string) (string, error) {
	var out strings.Builder
	err := RunView(args, &out)
	return out.String(), err
}

func TestRunView(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	shot := write("shot.png", pngBytes(t, 3, 2))
	notes := write("notes.txt", []byte("hello"))
	huge := filepath.Join(dir, "huge.png")
	if f, err := os.Create(huge); err == nil {
		_ = f.Truncate(images.MaxFileBytes + 1) // sparse
		f.Close()
	}
	wide := write("wide.png", pngBytes(t, 4096, 10))

	// Outside a foreground command of the agent.
	t.Setenv(config.EnvView, "")
	if _, err := runView(shot); err == nil || !strings.Contains(err.Error(), "foreground") {
		t.Fatalf("no dir: %v", err)
	}
	if _, err := runView(); err == nil || !strings.Contains(err.Error(), "usage: atto view") {
		t.Fatalf("no args: %v", err)
	}
	if _, err := runView("-h"); err == nil || !strings.Contains(err.Error(), "usage: atto view") {
		t.Fatalf("-h: %v", err)
	}

	view := t.TempDir()
	t.Setenv(config.EnvView, view)
	for path, want := range map[string]string{
		filepath.Join(dir, "missing.png"): "no such file",
		notes:                             "not a supported image",
		huge:                              "not an image file",
	} {
		if _, err := runView(shot, path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", filepath.Base(path), err, want)
		}
	}
	if got, _ := images.Collect(view); len(got) != 0 {
		t.Fatalf("a failed call attached %d images", len(got))
	}

	out, err := runView(shot, wide)
	if err != nil || out != "attached shot.png (3×2) for you to see\nattached wide.png (2048×5) for you to see\n" {
		t.Fatalf("%q %v", out, err)
	}
	got, err := images.Collect(view)
	if err != nil || len(got) != 2 || got[0].Name != "shot.png" || got[1].Width != 2048 || len(got[1].Data) == 0 {
		t.Fatalf("collected %+v %v", got, err)
	}
	many := make([]string, images.MaxViewed)
	for i := range many {
		many[i] = shot
	}
	if _, err := runView(many...); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("too many: %v", err)
	}

	// The command ended: its directory is gone.
	os.RemoveAll(view)
	if _, err := runView(shot); err == nil || !strings.Contains(err.Error(), "has ended") {
		t.Fatalf("gone: %v", err)
	}
}

// viewServer is a chat completions server whose model runs atto view in
// its first reply and answers in the next. It keeps request bodies.
func viewServer(t *testing.T, input string) func() []string {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			args := `{\"description\":\"look\",\"command\":\"atto view shot.png\"}`
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"%s\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n", args)
		} else {
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a gray square\"},\"finish_reason\":\"stop\"}]}\n\n")
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	models := `{"providers":{"fake":{"baseUrl":"` + srv.URL + `","models":[{"id":"m","contextWindow":10000,"input":` + input + `}]}}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	// "atto" on PATH is this test binary (TestMain runs view).
	bin := t.TempDir()
	exe, _ := os.Executable()
	if err := os.Symlink(exe, filepath.Join(bin, "atto")); err != nil {
		t.Skip("no symlinks:", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(config.EnvView, "")
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.WriteFile(filepath.Join(cwd, "shot.png"), pngBytes(t, 3, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), bodies...) }
}

func TestPrintView(t *testing.T) {
	bodies := viewServer(t, `["text","image"]`)
	quiet(t)
	if err := RunPrint(PrintOptions{Prompt: "take a look"}); err != nil {
		t.Fatal(err)
	}
	b := bodies()
	if len(b) != 2 || !strings.Contains(b[1], "attached shot.png (3×2) for you to see") ||
		!strings.Contains(b[1], `"Attached image(s) from tool result:"},{"type":"image_url","image_url":{"url":"data:image/png;base64,`) {
		t.Fatalf("requests: %v", b)
	}
	// The tool result keeps the stored image in the session.
	cwd, _ := os.Getwd()
	s, ok := session.Latest(cwd)
	if !ok {
		t.Fatal("no session")
	}
	_, entries, _ := session.Load(s.Path)
	var saved bool
	for _, e := range entries {
		if m := e.Message; m != nil && m.Role == "tool" && len(m.Images) == 1 && m.Images[0].Name == "shot.png" {
			_, err := os.Stat(filepath.Join(images.Dir(), m.Images[0].File))
			saved = err == nil
		}
	}
	if !saved {
		t.Fatal("image not saved with the tool result")
	}
}

func TestPrintViewTextModel(t *testing.T) {
	bodies := viewServer(t, `["text"]`)
	quiet(t)
	if err := RunPrint(PrintOptions{Prompt: "take a look"}); err != nil {
		t.Fatal(err)
	}
	b := bodies()
	if len(b) != 2 || strings.Contains(b[1], "base64") || !strings.Contains(b[1], agent.ViewUnsupported) {
		t.Fatalf("requests: %v", b)
	}
}
