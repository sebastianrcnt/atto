package app

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/tui"
)

func writePNG(t *testing.T, w, h int) string {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	path := filepath.Join(t.TempDir(), "Screen Shot.png")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPastedImagePathAttaches(t *testing.T) {
	path := writePNG(t, 3, 2)
	a := testApp(t, "text", "image")
	a.editor.HandleInput(tui.PastePrefix + "'" + path + "'")
	if a.editor.Text() != "[image 1: 3x2 PNG] " || len(a.editor.Attachments()) != 1 {
		t.Fatalf("editor %q", a.editor.Text())
	}
	// A path to something that is not an image stays text.
	a.editor.HandleInput(tui.PastePrefix + "/nonexistent/x.png")
	if !strings.HasSuffix(a.editor.Text(), "/nonexistent/x.png") {
		t.Fatalf("editor %q", a.editor.Text())
	}

	// A text-only model gets the path as text, as in codex.
	b := testApp(t)
	b.editor.HandleInput(tui.PastePrefix + path)
	if b.editor.Text() != path {
		t.Fatalf("text model editor %q", b.editor.Text())
	}
}

func TestClipboardKeyAttachesImage(t *testing.T) {
	a := testApp(t, "text", "image")
	im, _ := images.ReadFile(writePNG(t, 5, 4))
	var calls atomic.Int32
	a.clipboard = func(context.Context) (provider.Image, error) { calls.Add(1); return im, nil }
	for _, key := range []string{"\x16", "\x1bv"} { // ctrl+v, alt+v
		a.ui.Do(func() { a.onInput(key) })
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		a.ui.Do(func() { n = len(a.editor.Attachments()) })
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("attachments %d, clipboard calls %d", n, calls.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := a.editor.Text(); got != "[image 1: 5x4 PNG] [image 2: 5x4 PNG] " {
		t.Fatalf("editor %q", got)
	}

	// A text-only model: Ctrl+V does not read the clipboard.
	a.models = config.ModelsFile{Providers: map[string]config.Provider{"t": {Models: []config.Model{{ID: "m"}}}}}
	before := calls.Load()
	a.onInput("\x16")
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != before {
		t.Fatal("clipboard read for a text-only model")
	}
}
