package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/ui"
	"io"
	"sync"
)

// Host is what a front end gives extensions: its UI and a way to talk to
// the model. Methods are called from extension goroutines and must not
// block on the front end's own goroutine (queue the work instead): the
// front end may be waiting for an extension when they are called. ext is
// the extension's name, which owns its status items and widgets.
type Host interface {
	// HasUI is true in the TUI; ctx.hasUI tells extensions.
	HasUI() bool
	// Notify shows text; level is "info", "warning" or "error".
	Notify(ext, text, level string)
	// Ask shows q and calls answer once, from any goroutine, with the
	// choice (a string, nil when canceled) for "select" and "input", or a
	// bool for "confirm".
	Ask(ext string, q Question, answer func(any))
	// DisposeUI retires portable registrations and pending dialogs.
	DisposeUI(ext string)
	// SendMessage queues text as a user message for the model.
	SendMessage(text string)
	// SetSessionName names the session, as /name does. A front end that
	// cannot (atto -p) returns an error.
	SetSessionName(ext, name string) error
}

// Question is a dialog an extension asks: Kind is "select" (Options),
// "confirm" or "input".
type Question struct {
	Kind    string
	Title   string
	Options []string
}

// Headless is the Host of front ends without a UI (atto -p, and the
// server's, which adds what its clients show): notices go to Out (if set),
// status items and widgets are dropped, and questions get their default
// answer at once: select and input undefined, confirm false. Messages go
// to Send.
type Headless struct {
	queue     UIQueue
	mu        sync.Mutex
	elements  *ui.Registry
	store     MemoryStore
	Out       io.Writer
	Send      func(text string)
	StoreJSON func(context.Context, string, string, string, json.RawMessage) (json.RawMessage, error)
	// OnNotify, if set, receives notices instead of Out.
	OnNotify func(ext, text, level string)
}

func (h *Headless) HasUI() bool { return false }

func (h *Headless) Notify(ext, text, level string) {
	if h.OnNotify != nil {
		h.OnNotify(ext, text, level)
		return
	}
	if h.Out == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	prefix := ""
	if level == "warning" || level == "error" {
		prefix = level + ": "
	}
	fmt.Fprintf(h.Out, "[%s] %s%s\n", ext, prefix, text)
}

func (h *Headless) Ask(_ string, q Question, answer func(any)) {
	if q.Kind == "confirm" {
		answer(false)
		return
	}
	answer(nil)
}

// SetSessionName is refused: atto -p has no one to show the name to.
func (h *Headless) SetSessionName(string, string) error {
	return errors.New("naming the session needs the terminal or the server")
}

func (h *Headless) SendMessage(text string) {
	if h.Send != nil {
		h.Send(text)
	}
}

// UIBlock prints native portable blocks for noninteractive frontends.
func (h *Headless) UIBlock(title string, tree ui.Node) { h.Notify("atto", ui.PlainText(tree), "info") }

func (h *Headless) UIWork(work func(*ui.Registry) error, done func(error)) {
	h.mu.Lock()
	if h.elements == nil {
		h.elements = ui.NewRegistry(nil, nil)
	}
	r := h.elements
	h.mu.Unlock()
	h.queue.Post(func() { done(work(r)) })
}
func (h *Headless) Store(ctx context.Context, owner, op, key string, value json.RawMessage) (json.RawMessage, error) {
	if h.StoreJSON != nil {
		return h.StoreJSON(ctx, owner, op, key, value)
	}
	return h.store.Do(owner, op, key, value)
}

func (h *Headless) DisposeUI(ext string) {
	h.UIWork(func(r *ui.Registry) error { r.Unload(ext); return nil }, func(error) {})
}
