package extensions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func TestSessionStoreResumeBranchForkAndFreshSession(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	ctx := context.Background()
	w := session.New(t.TempDir())
	defer w.Close()
	store := SessionStore(w)
	if v, err := store(ctx, "a", "get", "count", nil); err != nil || v != nil {
		t.Fatal(string(v), err)
	}
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "start"}})
	_, err := store(ctx, "a", "set", "count", json.RawMessage(`1`))
	if err != nil {
		t.Fatal(err)
	}
	point := w.Leaf()
	_, err = store(ctx, "a", "set", "count", json.RawMessage(`2`))
	if err != nil {
		t.Fatal(err)
	}
	reopened := SessionStore(w)
	if v, err := reopened(ctx, "a", "get", "count", nil); err != nil || string(v) != "2" {
		t.Fatal(string(v), err)
	}
	if v, _ := store(ctx, "other", "get", "count", nil); v != nil {
		t.Fatal("cross-provider store leak")
	}
	f, err := session.ForkFile(w.Path, t.TempDir(), point)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	forkStore := SessionStore(f)
	if v, err := forkStore(ctx, "a", "get", "count", nil); err != nil || string(v) != "1" {
		t.Fatal("fork point", string(v), err)
	}
	w.Branch(point)
	if v, err := store(ctx, "a", "get", "count", nil); err != nil || string(v) != "1" {
		t.Fatal("branch replay", string(v), err)
	}
	_, err = store(ctx, "a", "delete", "count", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := store(ctx, "a", "get", "count", nil); err != nil || v != nil {
		t.Fatal(string(v), err)
	}
	fresh := session.New(t.TempDir())
	defer fresh.Close()
	if v, err := SessionStore(fresh)(ctx, "a", "get", "count", nil); err != nil || v != nil {
		t.Fatal("fresh store", string(v), err)
	}
}
