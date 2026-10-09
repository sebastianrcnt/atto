package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/ui"
	"os"
	"strings"

	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/session"
)

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

func (h threadHost) UIWork(work func(*ui.Registry) error, done func(error)) {
	h.do(func() {
		if h.t.closing {
			done(fmt.Errorf("session closed"))
			return
		}
		done(work(h.t.uiRegistry()))
	})
}
func (h threadHost) Store(ctx context.Context, owner, op, key string, value json.RawMessage) (json.RawMessage, error) {
	type result struct {
		value json.RawMessage
		err   error
	}
	done := make(chan result, 1)
	work := func() {
		if ctx.Err() != nil {
			done <- result{err: ctx.Err()}
			return
		}
		var store extensions.MemoryStore
		err := session.VisitActive(h.t.sess.Path, func(e session.Entry) error {
			if e.Type == session.TypeUIStore && e.Ext == owner {
				action := "set"
				if e.StoreDeleted {
					action = "delete"
				}
				_, err := store.Do(owner, action, e.StoreKey, e.StoreValue)
				return err
			}
			return nil
		})
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		var v json.RawMessage
		if err == nil {
			v, err = store.Do(owner, op, key, value)
		}
		if err == nil && (op == "set" || op == "delete") {
			if h.t.readOnly != "" {
				err = fmt.Errorf("session is read-only")
			} else {
				h.t.sess.Append(session.Entry{Type: session.TypeUIStore, Ext: owner, StoreKey: key, StoreValue: value, StoreDeleted: op == "delete"})
				err = h.t.sess.Err()
			}
		}
		done <- result{v, err}
	}
	if op == "get" || op == "keys" {
		work()
	} else {
		h.do(work)
	}
	select {
	case r := <-done:
		return r.value, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h threadHost) DisposeUI(ext string) {
	h.do(func() {
		h.t.uiRegistry().Unload(ext)
		h.t.cancelExtensionPromptsFor(ext)
	})
}
