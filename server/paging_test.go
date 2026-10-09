package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func TestPagedSnapshotsPerProtocolAndAcrossBranches(t *testing.T) {
	s, _ := testServer(t)
	w := session.New(s.Cwd)
	var old string
	for i := range 500 {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: fmt.Sprintf("message %03d %s", i, strings.Repeat("x", 200))}})
		if i == 100 {
			old = w.Leaf()
		}
		if i == 450 {
			w.Append(session.Entry{Type: session.TypeCompaction, Notes: "compaction", Replacement: []provider.Message{{Role: "user", Content: "notes"}}})
		}
	}
	w.Close()
	ctx := context.Background()
	v3 := Connect(ctx, s)
	defer v3.Close()
	if err := v3.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{3}}, nil); err != nil {
		t.Fatal(err)
	}
	var tail ThreadInfo
	if err := v3.Call(ctx, "thread/resume", map[string]any{"threadId": w.ID, "deferStart": true}, &tail); err != nil {
		t.Fatal(err)
	}
	if !tail.HasMore || tail.Before == "" || len(tail.Items) > 200 {
		t.Fatalf("not a paged snapshot: %+v", tail)
	}
	seen := map[string]bool{}
	for _, it := range tail.Items {
		if it.Type == ItemCompaction {
			t.Fatal("snapshot crossed last compaction")
		}
		if strings.HasPrefix(it.ID, itemPrefix(w.ID)) {
			seen[it.ID] = true
		}
	}
	if len(seen) != 49 {
		t.Fatalf("post-compaction tail: %d", len(seen))
	}
	before := tail.Before
	for more := true; more; {
		var page ItemPage
		if err := v3.Call(ctx, "thread/items", map[string]any{"threadId": w.ID, "before": before, "limit": 37}, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 37 {
			t.Fatal("page exceeded limit")
		}
		for i, it := range page.Items {
			if seen[it.ID] {
				t.Fatalf("duplicate %s", it.ID)
			}
			seen[it.ID] = true
			if i > 0 && itemNumber(w.ID, page.Items[i-1].ID) >= itemNumber(w.ID, it.ID) {
				t.Fatal("page is not newest-last")
			}
		}
		if page.HasMore && page.Before == before {
			t.Fatal("cursor did not progress")
		}
		before, more = page.Before, page.HasMore
	}
	if len(seen) != 501 {
		t.Fatalf("paged history lost compaction or items: %d", len(seen))
	}
	legacy := Connect(ctx, s)
	defer legacy.Close()
	if err := legacy.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{2}}, nil); err != nil {
		t.Fatal(err)
	}
	var full ThreadInfo
	if err := legacy.Call(ctx, "thread/read", map[string]any{"threadId": w.ID, "limit": 1}, &full); err != nil {
		t.Fatal(err)
	}
	persisted := 0
	for _, it := range full.Items {
		if strings.HasPrefix(it.ID, itemPrefix(w.ID)) {
			persisted++
		}
	}
	if persisted != 501 || full.Before != "" || full.HasMore {
		t.Fatalf("revision 2 snapshot changed: items=%d before=%s hasMore=%v", persisted, full.Before, full.HasMore)
	}
	if err := v3.Call(ctx, "thread/navigate", map[string]any{"threadId": w.ID, "entryId": old}, nil); err != nil {
		t.Fatal(err)
	}
	var branch ThreadInfo
	if err := v3.Call(ctx, "thread/read", map[string]any{"threadId": w.ID, "limit": 11}, &branch); err != nil {
		t.Fatal(err)
	}
	if len(branch.Items) > 11 || !branch.HasMore {
		t.Fatal("branch tail was not capped")
	}
	for _, it := range branch.Items {
		if it.Type == ItemAgent && strings.Contains(it.Text, "message 499") {
			t.Fatal("page retained abandoned branch")
		}
	}
}

func TestEarlierPageDoesNotAdvanceLiveBoundary(t *testing.T) {
	var view ThreadView
	view.Reset(ThreadInfo{ID: "t", EventID: 10, HasMore: true, Before: "t-i2", Items: []Item{{ID: "t-i2", Type: ItemAgent, Text: "current"}}})
	raw, _ := json.Marshal(map[string]any{"threadId": "t", "itemId": "t-i2", "delta": " live"})
	if !view.Apply(Notification{Method: "item/delta", EventID: 11, Params: raw}) {
		t.Fatal("delta not applied")
	}
	added := view.Prepend(ItemPage{Items: []Item{{ID: "t-i1", Type: ItemAgent, Text: "older"}, {ID: "t-i2", Type: ItemAgent, Text: "stale page"}}, Before: "t-i1"})
	if len(added) != 1 || view.EventID != 11 || view.Items[1].Text != "current live" {
		t.Fatalf("page replaced live state or cursor: %+v", view)
	}
	if view.Apply(Notification{Method: "item/delta", EventID: 11, Params: raw}) {
		t.Fatal("page made an event apply twice")
	}
}

func TestEmptyPagedSnapshotWireShape(t *testing.T) {
	for _, version := range []int{2, 3} {
		info, err := shapeSnapshot(ThreadInfo{ID: "empty", Items: []Item{}}, version, 200)
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := encodeJSON(&out, info); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out.String()), &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"hasMore", "before"} {
			if _, exists := fields[key]; exists != (version == 3) {
				t.Fatalf("revision %d: %s", version, out.String())
			}
		}
		if version == 3 && string(fields["items"]) != "[]" {
			t.Fatalf("empty items: %s", out.String())
		}
	}
}

func TestLiveOnlyStatusDoesNotShiftPageBoundary(t *testing.T) {
	s, _ := testServer(t)
	w := session.New(s.Cwd)
	for i := range 8 {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: fmt.Sprint(i)}})
	}
	w.Close()
	var b transcript.Builder
	b.IDPrefix = itemPrefix(w.ID)
	full, _, err := replayFile(&b, w.ID, w.Path, itemPrefix(w.ID)+"99", 200)
	if err != nil {
		t.Fatal(err)
	}
	anchor := full.Items[5]
	// A live-only goal status shifted the live item's counter by one.
	anchor.ID = itemPrefix(w.ID) + "7"
	page, _, err := replayFile(&b, w.ID, w.Path, anchor.ID, 200, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 5 || page.Items[4].Text != "4" {
		t.Fatalf("live counter included loaded item: %+v", page.Items)
	}
}
