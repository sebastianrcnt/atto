package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/session"
)

const DefaultItemLimit = 200

// ItemPage is an earlier page, oldest first. Before is an exclusive opaque
// item cursor; unlike EventID it never advances the live-event boundary.
type ItemPage struct {
	Items   []Item `json:"items"`
	HasMore bool   `json:"hasMore"`
	Before  string `json:"before"`
}

func itemNumber(sid, id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, itemPrefix(sid)))
	return n
}

// replayFile never builds the full transcript: the builder and the page retain
// only limit items. The full scan also applies later display-only amendments.
func replayFile(b *transcript.Builder, sid, path, before string, limit int, anchors ...Item) (ItemPage, blocks, error) {
	if limit <= 0 {
		limit = DefaultItemLimit
	}
	bl := blocks{}
	var kept []*transcript.Item
	bound := 0
	if before != "" {
		bound = itemNumber(sid, before)
		if bound < 1 {
			return ItemPage{}, nil, invalid("invalid before cursor")
		}
	}
	// Live-only notices/goals can occupy IDs absent from a disk replay. Resolve
	// the loaded boundary by saved block/entry/call identity, not that counter.
	var anchor *Item
	if len(anchors) > 0 {
		anchor = &anchors[0]
		bound = 0
	}
	resolve := func(it *transcript.Item) {
		if anchor == nil {
			return
		}
		w := wireItem(sid, it)
		matches := anchor.CallID != "" && w.CallID == anchor.CallID || anchor.EntryID != "" && w.EntryID == anchor.EntryID && w.Type == anchor.Type
		if !matches {
			return
		}
		bound = itemNumber(sid, it.ID)
		anchor = nil
		n := 0
		for _, old := range kept {
			if itemNumber(sid, old.ID) < bound {
				kept[n] = old
				n++
			}
		}
		clear(kept[n:])
		kept = kept[:n]
	}
	boundary := 1
	b.MaxItems = limit
	b.Reset()
	b.Handler = transcript.Handler{
		Completed: func(it *transcript.Item) {
			resolve(it)
			n := itemNumber(sid, it.ID)
			if before == "" && it.Kind == transcript.Compaction {
				kept = nil
				clear(bl)
				boundary = n + 1
				return
			}
			if bound > 0 && n >= bound {
				return
			}
			kept = append(kept, it)
			if len(kept) > limit {
				old := kept[0]
				delete(bl, blockID(sid, old))
				copy(kept, kept[1:])
				kept[len(kept)-1] = nil
				kept = kept[:len(kept)-1]
			}
		},
		Saved: func(it *transcript.Item) {
			resolve(it)
			n := itemNumber(sid, it.ID)
			if bound == 0 || n < bound {
				bl.saved(sid, it)
			}
		},
		Display: func(d transcript.Display) {
			if x := bl[session.BlockID(sid, d.EntryID, d.Block)]; x != nil {
				x.disp.Apply(d)
			}
		},
	}
	err := session.VisitActive(path, func(e session.Entry) error {
		b.ReplayEntry(e)
		applyUITail(kept, e)
		pruneReplayBlocks(bl, kept)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return ItemPage{}, nil, err
	}
	b.FinishReplay()
	b.Handler = transcript.Handler{}
	page := ItemPage{Items: make([]Item, 0, len(kept))}
	for _, it := range kept {
		page.Items = append(page.Items, bl.attach(wireItem(sid, it)))
	}
	if err := session.VisitActive(path, func(e session.Entry) error { applyUIItems(page.Items, e); return nil }); err != nil && !os.IsNotExist(err) {
		return ItemPage{}, nil, err
	}
	first := boundary
	if len(page.Items) > 0 {
		page.Before = page.Items[0].ID
		first = itemNumber(sid, page.Before)
	} else {
		page.Before = itemPrefix(sid) + strconv.Itoa(boundary)
	}
	page.HasMore = first > 1
	// Saved fires before Completed; discard any blocks outside the bounded page.
	used := map[string]bool{}
	for _, it := range page.Items {
		used[it.BlockID] = true
	}
	for id := range bl {
		if !used[id] {
			delete(bl, id)
		}
	}
	return page, bl, nil
}

func pruneReplayBlocks(bl blocks, kept []*transcript.Item) {
	used := make(map[string]bool, len(kept))
	for _, it := range kept {
		used[it.ID] = true
	}
	for id, block := range bl {
		if !used[block.item] {
			delete(bl, id)
		}
	}
}

func (t *thread) trimItems() {
	count := 0
	for _, it := range t.items {
		if it.Type != ItemNotice {
			count++
		}
	}
	remove := count - DefaultItemLimit
	if remove <= 0 {
		return
	}
	slices.SortStableFunc(t.items, func(a, b Item) int { return cmp.Compare(t.itemOrder[a.ID], t.itemOrder[b.ID]) })
	kept := t.items[:0]
	for _, it := range t.items {
		if remove > 0 && it.Type != ItemNotice {
			delete(t.itemOrder, it.ID)
			delete(t.blocks, it.BlockID)
			remove--
			continue
		}
		kept = append(kept, it)
	}
	clear(t.items[len(kept):])
	t.items = kept
	t.hasMore = true
	for _, it := range t.items {
		if strings.HasPrefix(it.ID, itemPrefix(t.id)) {
			t.before = it.ID
			break
		}
	}
}

func (t *thread) replayDisk() error {
	page, bl, err := replayFile(&t.tr, t.id, t.sess.Path, "", DefaultItemLimit)
	if err != nil {
		return err
	}
	t.items, t.blocks = page.Items, bl
	t.hasMore, t.before = page.HasMore, page.Before
	t.resetItemOrder()
	t.tr.Handler = t.handler()
	t.itemMeta = map[string]userMeta{}
	debug.FreeOSMemory()
	return nil
}

func (t *thread) dropDisplay() {
	t.items = nil
	t.blocks = blocks{}
	t.itemOrder = nil
	t.itemSeq = 0
	t.tr.ForgetCompleted()
}

// loadDisplay always uses a separate builder, so
// attaching cannot reset its open items or change their live event IDs.
func (t *thread) loadDisplay() error {
	b := transcript.Builder{IDPrefix: itemPrefix(t.id)}
	page, bl, err := replayFile(&b, t.id, t.sess.Path, "", DefaultItemLimit)
	if err != nil {
		return err
	}
	t.tr.AdvanceSequence(b.Sequence())
	loaded := t.loaded
	// Recreate the fixed Loaded report rather than retaining a display notice
	// through headless periods. It precedes the reconstructed transcript.
	t.notices++
	report := Item{ID: fmt.Sprintf("%s-n%d", t.id, t.notices), Type: ItemNotice, Status: "completed", Level: "loaded", Text: strings.Join(append([]string{"Loaded"}, core.FormatRows(loaded.Summary(), "  ", 12)...), "\n"), Loaded: &loaded}
	t.items, t.blocks, t.hasMore, t.before = append([]Item{report}, page.Items...), bl, page.HasMore, page.Before
	t.resetItemOrder()
	for _, it := range t.tr.Open() {
		t.startedItem(it.ID)
	}
	debug.FreeOSMemory()
	return nil
}

func (s *Server) snapshotResult(ctx context.Context, method string, p threadParams, out any) (any, error) {
	switch method {
	case "thread/start", "thread/resume", "thread/read", "thread/attach":
	default:
		return out, nil
	}
	info, ok := out.(ThreadInfo)
	if !ok {
		return out, nil
	} // Worker routing has already shaped its response.
	// Shape and capture the snapshot on the lane: events arriving while a
	// disk page is reconstructed must not sneak past its exactly-once cursor.
	if t, err := s.thread(info.ID); err == nil {
		var shaped ThreadInfo
		err = t.call(func() error {
			fresh := t.snapshot()
			fresh.Context = info.Context
			var shapeErr error
			shaped, shapeErr = shapeSnapshot(fresh, p.Limit)
			return shapeErr
		})
		return shaped, err
	}
	return shapeSnapshot(info, p.Limit)
}

func shapeSnapshot(info ThreadInfo, requestedLimit int) (ThreadInfo, error) {
	info.Paged = true
	limit := requestedLimit
	if (len(info.Items) > 0 || info.SessionPath == "") && (limit <= 0 || limit <= DefaultItemLimit) {
		if limit <= 0 {
			limit = DefaultItemLimit
		}
		start := 0
		for i, it := range info.Items {
			if it.Type == ItemCompaction {
				start = i + 1
				info.HasMore = true
				info.Before = itemPrefix(info.ID) + strconv.Itoa(itemNumber(info.ID, it.ID)+1)
			}
		}
		info.Items = info.Items[start:]
		if len(info.Items) > limit {
			info.Items = info.Items[len(info.Items)-limit:]
			info.HasMore = true
		}
		for _, it := range info.Items {
			if strings.HasPrefix(it.ID, itemPrefix(info.ID)) {
				info.Before = it.ID
				break
			}
		}
		if info.Before == "" {
			info.Before = itemPrefix(info.ID) + "1"
		}
		return info, nil
	}
	var b transcript.Builder
	b.IDPrefix = itemPrefix(info.ID)
	page, _, err := replayFile(&b, info.ID, info.SessionPath, "", limit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ThreadInfo{}, err
	}
	// Merge unsaved/in-progress items and runtime notices without replacing the
	// live event cursor captured on the lane. Completed persisted items win from
	// the snapshot (input provenance and streaming timing included).
	originalItems := info.Items
	byID := map[string]int{}
	for i, it := range page.Items {
		byID[it.ID] = i
	}
	var notices []Item
	for _, it := range info.Items {
		if i, exists := byID[it.ID]; exists {
			page.Items[i] = it
		} else if it.Type == ItemNotice {
			notices = append(notices, it)
		} else if it.Status == "inProgress" || itemNumber(info.ID, it.ID) >= itemNumber(info.ID, page.Before) {
			page.Items = append(page.Items, it)
		}
	}
	info.Items = append(notices, page.Items...)
	ranks := map[string]int{}
	previous := 0
	for _, it := range info.Items {
		if it.Type != ItemNotice {
			ranks[it.ID] = 2 * itemNumber(info.ID, it.ID)
		}
	}
	for _, it := range originalItems {
		if it.Type == ItemNotice {
			ranks[it.ID] = 2*previous + 1
		} else {
			previous = itemNumber(info.ID, it.ID)
		}
	}
	slices.SortStableFunc(info.Items, func(a, b Item) int { return cmp.Compare(ranks[a.ID], ranks[b.ID]) })
	if limit <= 0 {
		limit = DefaultItemLimit
	}
	if len(info.Items) > limit {
		info.Items = info.Items[len(info.Items)-limit:]
		page.HasMore = true
	}
	info.HasMore, info.Before = page.HasMore, page.Before
	for _, it := range info.Items {
		if strings.HasPrefix(it.ID, itemPrefix(info.ID)) {
			info.Before = it.ID
			break
		}
	}
	debug.FreeOSMemory()
	return info, nil
}

// pageAnchor uses only the bounded loaded tail. Earlier disk page cursors need
// no lookup; their IDs already use the disk sequence. Live-only items are not
// saved, so the next saved item is their exclusive history boundary.
func (t *thread) pageAnchor(before string) []Item {
	found := false
	for _, it := range t.items {
		found = found || it.ID == before
		if found && (it.EntryID != "" || it.CallID != "") {
			return []Item{it}
		}
	}
	return nil
}
