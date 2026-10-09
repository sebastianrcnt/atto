package session

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"
)

// The session tree follows pi's design. Every entry has an ID and the ID
// of its parent. Appending makes the new entry a child of the active leaf
// and moves the leaf to it. Going back moves the leaf to an earlier entry
// (Writer.Branch); the next entry then starts a new branch beside the old
// one. The conversation sent to the model is the path from the root to the
// leaf, so continuing from any point replays exactly the messages that
// were on that path, byte for byte, and the prefix cache still matches.
//
// Unlike pi, where the leaf lives in memory until the next append, the move
// is written down as a "branch" entry so a resume lands where the user left.

// newEntryID returns a random entry ID. pi uses 8 hex characters; 16 keeps
// collisions out of reach in long sessions without tracking every ID.
func newEntryID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// link gives entries written before entries had IDs a stable ID ("#n", its
// 1-based position, which cannot collide with a hex ID) and makes each the
// child of the entry before it, so an old linear file loads as one branch.
// The file itself is never rewritten.
func link(entries []Entry) {
	prev := ""
	for i := range entries {
		e := &entries[i]
		if e.ID == "" {
			e.ID, e.Parent = "#"+strconv.Itoa(i+1), prev
		}
		prev = e.ID
	}
}

// Leaf returns the active leaf: the last entry ("" when there is none).
func Leaf(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	return entries[len(entries)-1].ID
}

// index maps entry IDs to positions. A duplicated ID keeps its first entry.
func index(entries []Entry) map[string]int {
	idx := make(map[string]int, len(entries))
	for i, e := range entries {
		if _, ok := idx[e.ID]; !ok {
			idx[e.ID] = i
		}
	}
	return idx
}

// pathIndexes returns the positions of the entries from the root to leaf.
func pathIndexes(entries []Entry, leaf string) []int {
	idx := index(entries)
	var rev []int
	seen := map[string]bool{}
	for id := leaf; id != "" && !seen[id]; {
		i, ok := idx[id]
		if !ok {
			break
		}
		seen[id] = true
		rev = append(rev, i)
		id = entries[i].Parent
	}
	out := make([]int, len(rev))
	for i, j := range rev {
		out[len(rev)-1-i] = j
	}
	return out
}

// Path returns the entries from the root to leaf, in order.
func Path(entries []Entry, leaf string) []Entry {
	var out []Entry
	for _, i := range pathIndexes(entries, leaf) {
		out = append(out, entries[i])
	}
	return out
}

// Active returns the path to the active leaf: the conversation to resume,
// replay and send to the model.
func Active(entries []Entry) []Entry { return Path(entries, Leaf(entries)) }

// OnActivePath reports, per entry, whether it is on the active branch.
func OnActivePath(entries []Entry) []bool {
	on := make([]bool, len(entries))
	for _, i := range pathIndexes(entries, Leaf(entries)) {
		on[i] = true
	}
	return on
}

// Node is one entry in the session tree.
type Node struct {
	Entry     Entry
	N         int // 1-based position in the file, as in atto history
	Children  []*Node
	Label     string    // resolved from label entries
	LabelTime time.Time // when the label was last set
}

// Tree returns the session as a tree. Children keep file order (oldest
// first). A well-formed session has one root; navigating to before the
// first message, or a broken parent chain, adds more.
func Tree(entries []Entry) []*Node {
	nodes := make([]*Node, len(entries))
	byID := map[string]*Node{}
	for i, e := range entries {
		nodes[i] = &Node{Entry: e, N: i + 1}
		if _, ok := byID[e.ID]; !ok {
			byID[e.ID] = nodes[i]
		}
	}
	for _, e := range entries {
		if e.Type != TypeLabel {
			continue
		}
		if n := byID[e.TargetID]; n != nil {
			n.Label, n.LabelTime = e.Label, e.Time
			if e.Label == "" {
				n.LabelTime = time.Time{}
			}
		}
	}
	var roots []*Node
	for _, n := range nodes {
		p := byID[n.Entry.Parent]
		if n.Entry.Parent == "" || p == nil || p == n {
			roots = append(roots, n)
			continue
		}
		p.Children = append(p.Children, n)
	}
	return roots
}

// BranchPoint says where the leaf goes when the user picks entry id in the
// tree, as pi does: a user message is re-opened for editing, so the leaf
// moves to its parent and its text is returned for the editor; any other
// entry becomes the leaf itself.
func BranchPoint(entries []Entry, id string) (leaf, editorText string, ok bool) {
	i, ok := index(entries)[id]
	if !ok {
		return "", "", false
	}
	e := entries[i]
	if e.Type == TypeMessage && e.Message != nil && e.Message.Role == "user" {
		return e.Parent, e.Message.Content, true
	}
	return id, "", true
}

// Abandoned returns the entries a move of the leaf from one entry to
// another leaves behind: those on the path to from that are not on the
// path to to, in order (pi's collectEntriesForBranchSummary). It is empty
// when to is on from's own path, or further along it.
func Abandoned(entries []Entry, from, to string) []Entry {
	keep := map[int]bool{}
	for _, i := range pathIndexes(entries, to) {
		keep[i] = true
	}
	var out []Entry
	for _, i := range pathIndexes(entries, from) {
		if !keep[i] {
			out = append(out, entries[i])
		}
	}
	return out
}

// Fork starts a new session in cwd holding the path from the root to leaf
// of a session (pi's /fork). The entries keep their content but get new
// IDs, chained in order; branch markers are dropped and labels on the
// copied entries carried over. src is recorded as the parent session.
// Nothing is written when the path is empty.
func Fork(src, cwd string, entries []Entry, leaf string) *Writer {
	w := New(cwd)
	w.parent = src
	labels := map[string]Entry{} // target ID -> latest label entry
	for _, e := range entries {
		if e.Type == TypeLabel {
			labels[e.TargetID] = e
		}
	}
	var copied [][2]string // old ID, new ID
	newID := map[string]string{}
	for _, e := range Path(entries, leaf) {
		if e.Type == TypeBranch || e.Type == TypeLabel {
			continue
		}
		old := e.ID
		if e.Type == TypeBlockDisplay || e.Type == TypeUIItemDisplay || e.Type == TypeUIBlockUpdate { // follows its message to its new ID
			if e.TargetID = newID[e.TargetID]; e.TargetID == "" {
				continue
			}
		}
		w.Append(e)
		copied = append(copied, [2]string{old, w.Leaf()})
		newID[old] = w.Leaf()
	}
	for _, c := range copied {
		if l, ok := labels[c[0]]; ok && l.Label != "" {
			w.Append(Entry{Type: TypeLabel, TargetID: c[1], Label: l.Label, Time: l.Time})
		}
	}
	return w
}
