package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
)

func TestThreadResources(t *testing.T) {
	h := newHarness(t)
	th, err := h.s.thread(h.id)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, text := range map[string]string{"main.go": "hello", ".gitignore": "secret\n", "secret": "hidden"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(output, []byte("full output"), 0o600); err != nil {
		t.Fatal(err)
	}
	im := provider.Image{File: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png", MIME: "image/png", Data: []byte("image bytes")}
	if err := images.Save(im); err != nil {
		t.Fatal(err)
	}
	if err := th.call(func() error {
		th.cwd = root
		th.items = append(th.items, Item{ID: "resource", Type: ItemCommand, FullOutput: output, Images: wireImages([]provider.Image{im})})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	files := h.call("thread/files", map[string]any{"query": "MAIN"})["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "main.go" {
		t.Fatalf("files: %v", files)
	}
	all := h.call("thread/files", nil)["files"].([]any)
	if len(all) != 2 {
		t.Fatalf("ignored file exposed: %v", all)
	}
	if _, err := h.try(h.c, "thread/files", map[string]any{"limit": 1001}); err == nil {
		t.Fatal("unbounded limit accepted")
	}
	if got := h.call("item/output", map[string]any{"itemId": "resource"})["output"]; got != "full output" {
		t.Fatalf("output: %v", got)
	}
	if got := h.call("item/image", map[string]any{"itemId": "resource", "index": 0})["data"]; got != base64.StdEncoding.EncodeToString(im.Data) {
		t.Fatalf("image: %v", got)
	}
	for _, p := range []map[string]any{{"itemId": "other", "index": 0}, {"itemId": "resource", "index": -1}, {"itemId": "resource", "index": 1}} {
		if _, err := h.try(h.c, "item/image", p); err == nil {
			t.Fatalf("invalid reference accepted: %v", p)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := threadFiles(ctx, root, "", 10); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestUserMessageForkProvenance(t *testing.T) {
	h := newHarness(t)
	h.call("input/submit", map[string]any{"input": "fork this message"})
	h.completed()
	info := h.call("thread/read", nil)
	var entry string
	for _, row := range info["items"].([]any) {
		it := row.(map[string]any)
		if it["type"] == ItemUser {
			entry, _ = it["entryId"].(string)
		}
	}
	if entry == "" {
		t.Fatal("live user item has no persisted entry ID")
	}
	out := h.call("thread/fork", map[string]any{"entryId": entry})
	if out["input"] != "fork this message" || out["path"] == "" || out["threadId"] == "" {
		t.Fatalf("fork: %v", out)
	}
	var forked ThreadInfo
	if err := h.c.Call(context.Background(), "thread/resume", map[string]any{"threadId": out["threadId"]}, &forked); err != nil {
		t.Fatalf("resume root fork: %v", err)
	}
	if forked.ID != out["threadId"] {
		t.Fatalf("fork ID: %v", forked.ID)
	}
}

func TestPNGResourcePreview(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1000, 1000))); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(file, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := readResource(resourceRequest{path: file, mime: "image/png", kind: "image", preview: true})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	b, err := base64.StdEncoding.DecodeString(out["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width != 350 || cfg.Height != 350 || out["mimeType"] != "image/png" {
		t.Fatalf("PNG preview: %v / %v", cfg, err)
	}
}

func TestOfflineTranscriptResources(t *testing.T) {
	h := newHarness(t)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	h.call("input/submit", map[string]any{"input": "image for offline read", "images": []ImageInput{{MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(data.Bytes())}}})
	h.completed()
	h.call("thread/close", nil)
	info := h.call("thread/read", map[string]any{"offline": true})
	var id string
	for _, row := range info["items"].([]any) {
		it := row.(map[string]any)
		if it["type"] == ItemUser {
			id = it["id"].(string)
		}
	}
	if id == "" {
		t.Fatal("saved image user item missing")
	}
	out := h.call("item/image", map[string]any{"offline": true, "itemId": id, "index": 0, "preview": true})
	if out["mimeType"] != "image/png" || out["data"] == "" {
		t.Fatalf("offline image: %v", out)
	}
	if h.s.Loaded(h.id) {
		t.Fatal("offline resource read acquired execution ownership")
	}
}
