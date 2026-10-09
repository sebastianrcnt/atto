package server

import (
	"strings"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/session"
)

// What a thread's extensions show, kept on the thread and sent to clients
// as data: statuses and replacement texts on reasoning and agentMessage
// items (item/display), text blocks (extText items) and status items and
// widgets (extension/ui). Blocks and text blocks are saved in the session
// (block_display and ext_text entries), so every client shows them on
// resume.

// block is a reasoning or agentMessage item extensions can name.
type block struct {
	item    string // the item's ID
	entryID string // of the assistant message
	kind    string // session.BlockText or session.BlockReasoning
	disp    transcript.BlockDisplay
}

// blocks are a thread's blocks by block ID.
type blocks map[string]*block

// saved makes a block of a reasoning or assistant item saved in session sid.
func (bl blocks) saved(sid string, it *transcript.Item) {
	if it.EntryID == "" {
		return
	}
	kind := session.BlockText
	if it.Kind == transcript.Reasoning {
		kind = session.BlockReasoning
	}
	bl[session.BlockID(sid, it.EntryID, kind)] = &block{item: it.ID, entryID: it.EntryID, kind: kind}
}

// attach gives an item what extensions show on its block.
func (bl blocks) attach(w Item) Item {
	if b := bl[w.BlockID]; b != nil && w.BlockID != "" {
		w.Display = WireDisplay(&b.disp)
	}
	return w
}

// threadHost is the Host of a thread's extensions. Extensions call it
// from their own goroutines while the lane may be waiting for them (a
// reload, session_end), so it never waits for the lane: each call is
// queued on it, in order.
type threadHost struct{ t *thread }

func (h threadHost) do(fn func()) { h.t.do(fn) }

// HasUI is true while an interactive client is attached.
func (h threadHost) HasUI() bool { return h.t.s.interactiveClients() > 0 }

func (h threadHost) Notify(ext, text, level string) {
	h.do(func() {
		lv := ""
		if level == "warning" || level == "error" {
			lv = level
		}
		h.t.notice(lv, "[%s] %s", ext, text)
		h.t.publish("extension/notify", map[string]any{"extension": ext, "message": text, "level": level})
	})
}

func (h threadHost) SetStatus(ext, key, text string) {
	h.ui(func(u *extensions.UIState) { u.SetStatus(ext+"/"+key, text) })
}

func (h threadHost) SetWidget(ext, key string, lines []string) {
	h.ui(func(u *extensions.UIState) { u.SetWidget(ext+"/"+key, lines) })
}

func (h threadHost) ClearUI(ext string) {
	h.ui(func(u *extensions.UIState) { u.Clear(ext) })
}

func (h threadHost) ui(change func(*extensions.UIState)) {
	h.do(func() {
		change(&h.t.ui)
		h.t.publish("extension/ui", map[string]any{"ui": WireExtensionUI(&h.t.ui)})
	})
}

func (h threadHost) SetBlockStatus(ext, id, text string) {
	h.block(ext, id, func(d *transcript.BlockDisplay) bool { return d.SetStatus(ext, text) })
}

func (h threadHost) SetBlockDisplay(ext, id, text string) {
	h.block(ext, id, func(d *transcript.BlockDisplay) bool { return d.SetDisplay(ext, text) })
}

// block changes what ext shows on block id, saves ext's state for it in
// the session and tells the clients. A block that is not the thread's (the
// branch changed) is ignored.
func (h threadHost) block(ext, id string, change func(*transcript.BlockDisplay) bool) {
	h.do(func() {
		t := h.t
		b := t.blocks[id]
		headless := len(t.attached) == 0
		if headless {
			b = t.headlessBlocks[id]
			if b != nil {
				b.disp = transcript.BlockDisplay{}
				_ = session.VisitActive(t.sess.Path, func(e session.Entry) error {
					if e.Type == session.TypeBlockDisplay && e.TargetID == b.entryID && e.Block == b.kind {
						b.disp.Apply(transcript.Display{EntryID: e.TargetID, Block: e.Block, Ext: e.Ext, Status: e.Status, Text: e.Display})
					}
					return nil
				})
			}
		}
		if b == nil || !change(&b.disp) {
			return
		}
		if !strings.HasPrefix(b.entryID, "n") { // "n<k>": not recorded, nothing to attach to
			status, display := b.disp.Snapshot(ext)
			t.sess.Append(session.Entry{Type: session.TypeBlockDisplay, TargetID: b.entryID, Block: b.kind, Ext: ext, Status: status, Display: display})
		}
		t.publish("item/display", map[string]any{"itemId": b.item, "blockId": id, "display": WireDisplay(&b.disp)})
		if headless {
			b.disp = transcript.BlockDisplay{}
		}
	})
}

// SetSessionName names the thread and saves the name, as /name does.
func (h threadHost) SetSessionName(ext, name string) error {
	h.do(func() {
		if h.t.readOnly != "" {
			h.t.notice("", "%s: this conversation is read-only; not named.", ext)
			return
		}
		h.t.nameSession(name)
	})
	return nil
}

// ShowText adds an extText item, completed at once, and saves it in the
// session.
func (h threadHost) ShowText(ext, title, text string, o extensions.TextOptions) {
	h.do(func() {
		h.t.tr.Add(transcript.Item{Kind: transcript.ExtText, Ext: ext, Title: title, Text: text, Lang: o.Lang, Preview: o.Preview})
		if len(h.t.attached) == 0 {
			h.t.tr.ForgetCompleted()
		}
		h.t.sess.Append(session.Entry{Type: session.TypeExtText, Ext: ext, Title: title, Display: text, Lang: o.Lang, Preview: o.Preview})
	})
}

// Ask is a prompt of the runtime (prompts.go).
func (h threadHost) Ask(ext string, q extensions.Question, answer func(any)) {
	h.do(func() { h.t.askExtension(ext, q, answer) })
}

// SendMessage is atto.sendMessage: it steers a running turn, follows a
// compaction, or starts a turn.
func (h threadHost) SendMessage(text string) {
	h.do(func() {
		t := h.t
		switch {
		case t.closing:
		case strings.TrimSpace(text) == "":
		case t.noModel():
		case t.turns.Busy && t.runKind == "turn":
			t.steer("", text)
		case t.turns.Busy:
			t.enqueue("", text, nil)
		default:
			t.runTurn(t.newInput("", text, nil), false)
		}
		t.pendingChanged()
	})
}
