package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// rawServer replies in order and keeps each request's raw messages.
func rawServer(t *testing.T, replies ...[]string) (*httptest.Server, func() [][]json.RawMessage) {
	var mu sync.Mutex
	var seen [][]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.Unmarshal(data, &body)
		mu.Lock()
		i := len(seen)
		seen = append(seen, body.Messages)
		mu.Unlock()
		if i >= len(replies) {
			t.Errorf("unexpected request %d", i)
			return
		}
		for _, c := range replies[i] {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]json.RawMessage { mu.Lock(); defer mu.Unlock(); return seen }
}

// Going back to before a user message and sending another must repeat the
// earlier part of the conversation byte for byte, images included, so the
// provider's prefix cache still matches.
func TestBranchKeepsPrefixBytes(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	srv, seen := rawServer(t, toolCall("echo hi"), text("done"), text("two"), text("edited"))
	w := session.New(t.TempDir())
	a := imageAgent(srv.URL, "text", "image")
	a.Record = w.Append
	ctx := context.Background()
	im := testPNG(t)
	if err := images.Save(im); err != nil {
		t.Fatal(err)
	}
	if err := a.RunWithImages(ctx, "first [image 1: 3x2 PNG]", []provider.Image{im}, func(any) {}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(ctx, "second", func(any) {}); err != nil {
		t.Fatal(err)
	}
	w.Close()

	// The tree: pick "second" → back to its parent, as a resume would see it.
	h, entries, err := session.Load(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	var second string
	for _, e := range entries {
		if e.Message != nil && e.Message.Content == "second" {
			second = e.ID
		}
	}
	leaf, text, _ := session.BranchPoint(entries, second)
	if text != "second" {
		t.Fatalf("editor text %q", text)
	}
	r := session.Resume(w.Path, h)
	r.Branch(leaf)
	_, entries, _ = session.Load(w.Path)

	b := imageAgent(srv.URL, "text", "image")
	b.SetStart(h.Time)
	b.Record = r.Append
	b.Restore(session.Active(entries))
	if err := b.Run(ctx, "second, edited", func(any) {}); err != nil {
		t.Fatal(err)
	}
	r.Close()

	reqs := seen()
	before, after := reqs[2], reqs[3] // "second", then its edit
	// system, user, assistant tool call, tool result, assistant "done"
	if len(before) != 6 || len(after) != 6 {
		t.Fatalf("lengths %d %d", len(before), len(after))
	}
	for i := range 5 {
		if !bytes.Equal(before[i], after[i]) {
			t.Fatalf("message %d differs:\n%s\n%s", i, before[i], after[i])
		}
	}
	if !bytes.Contains(before[1], []byte("data:image/png;base64,")) {
		t.Fatalf("the image should be in the request: %s", before[1])
	}
	if bytes.Equal(before[5], after[5]) {
		t.Fatal("the edited message should differ")
	}
}
