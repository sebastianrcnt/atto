package extensions

import (
	"errors"
	"fmt"
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
	// SetStatus sets the status line item key ("" removes it).
	SetStatus(ext, key, text string)
	// SetWidget sets the band of lines key above the input (nil removes it).
	SetWidget(ext, key string, lines []string)
	// Ask shows q and calls answer once, from any goroutine, with the
	// choice (a string, nil when canceled) for "select" and "input", or a
	// bool for "confirm".
	Ask(ext string, q Question, answer func(any))
	// SetBlockStatus sets (text "" removes) the short status ext shows on
	// the header of the block id: display only. A block that does not exist
	// is ignored.
	SetBlockStatus(ext, id, text string)
	// SetBlockDisplay sets (text "" restores) what the block id shows in
	// place of its own text: display only, the model and the session's
	// messages never change. A block that does not exist is ignored.
	SetBlockDisplay(ext, id, text string)
	// ShowText adds a display-only block with title and text to the
	// transcript, saved in the session; see TextOptions.
	ShowText(ext, title, text string, o TextOptions)
	// ClearUI removes every status item and widget of ext.
	ClearUI(ext string)
	// SendMessage queues text as a user message for the model.
	SendMessage(text string)
	// SetSessionName names the session, as /name does. A front end that
	// cannot (atto -p) returns an error.
	SetSessionName(ext, name string) error
}

// TextOptions shape the block ShowText adds.
type TextOptions struct {
	// Lang says how to colour the text: "diff", or "" for plain text.
	Lang string
	// Preview is how many lines show while the block is collapsed; 0 is the
	// front end's default.
	Preview int
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
	mu   sync.Mutex
	Out  io.Writer
	Send func(text string)
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

func (h *Headless) SetStatus(string, string, string)   {}
func (h *Headless) SetWidget(string, string, []string) {}

// Blocks are for front ends that show them (the TUI, the server).
func (h *Headless) SetBlockStatus(string, string, string)  {}
func (h *Headless) SetBlockDisplay(string, string, string) {}
func (h *Headless) ClearUI(string)                         {}

// ShowText prints the text as a notice, under its title.
func (h *Headless) ShowText(ext, title, text string, _ TextOptions) {
	h.Notify(ext, title+"\n"+text, "info")
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
