package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
)

// The TUI is a client of atto's session runtime (package server): it
// owns the editor, the screen and its pickers, and sends everything else
// as requests over the protocol: input, interrupts, goal and session
// changes. What it shows comes from the runtime's notifications: items
// become blocks, and the state the footer shows (busy, pending input, the
// goal, usage) is a mirror of the thread's. Without the daemon the
// runtime runs in this process (server.Connect: the same JSON, dispatcher
// and event hub as any client). Daemon panes also keep this in-process
// runtime until session workers are introduced in phase 3.

// conn is the TUI's connection to its runtime.
type conn struct {
	c *server.Client
	// own is the runtime this process runs (nil for a worker's).
	own *server.Server
	id  string // the client ID initialize gave

	mu    sync.Mutex
	queue []rpcCall
	wake  chan struct{}
}

// call is one request waiting to go out.
type rpcCall struct {
	method string
	params map[string]any
	done   func(json.RawMessage, error) // on the UI goroutine; may be nil
}

// connect starts the TUI's connection: requests go out one at a time, in
// order, from a goroutine of their own (the UI never waits for the
// runtime), and notifications are applied on the UI goroutine.
func (a *App) connect(c *server.Client, own *server.Server) error {
	a.conn = &conn{c: c, own: own, wake: make(chan struct{}, 1)}
	var init struct {
		ClientID string `json:"clientId"`
		Version  int    `json:"protocolVersion"`
	}
	err := c.Call(context.Background(), "initialize", map[string]any{
		"protocolVersions": []int{server.ProtocolVersion},
		"clientInfo":       map[string]string{"name": "atto-tui", "version": Version},
		"capabilities":     map[string]bool{"interactive": true, "images": true},
	}, &init)
	if err != nil {
		return err
	}
	if init.Version != server.ProtocolVersion {
		return fmt.Errorf("the session runtime speaks protocol %d, this terminal %d: update the older side", init.Version, server.ProtocolVersion)
	}
	if err := c.Call(context.Background(), "initialized", nil, nil); err != nil {
		return err
	}
	a.conn.id = init.ClientID
	go a.sendLoop()
	go a.eventLoop()
	return nil
}

// rpc sends a request; done, when set, gets the result on the UI
// goroutine. Requests go out in the order rpc is called.
func (a *App) rpc(method string, params map[string]any, done func(json.RawMessage, error)) {
	if params == nil {
		params = map[string]any{}
	}
	if _, ok := params["threadId"]; !ok && a.threadID != "" {
		params["threadId"] = a.threadID
	}
	cn := a.conn
	if cn == nil || cn.c == nil {
		if done != nil {
			done(nil, errors.New("not connected"))
		}
		return
	}
	select {
	case <-cn.c.Done():
		if done != nil {
			done(nil, server.ErrClosed)
		}
		return
	default:
	}
	snapshot := method == "thread/read" || method == "thread/resume" || method == "thread/start" || method == "thread/attach"
	if snapshot {
		a.snapshotPending++
		callback := done
		done = func(raw json.RawMessage, err error) {
			a.snapshotPending--
			if callback != nil {
				callback(raw, err)
			}
			if a.snapshotPending == 0 {
				waiting := a.snapshotEvents
				a.snapshotEvents = nil
				for _, n := range waiting {
					a.onNotification(n)
				}
			}
		}
	}
	cn.mu.Lock()
	cn.queue = append(cn.queue, rpcCall{method, params, done})
	cn.mu.Unlock()
	select {
	case cn.wake <- struct{}{}:
	default:
	}
}

// rpcErr is rpc whose failures are shown as error notices.
func (a *App) rpcErr(method string, params map[string]any) {
	a.rpc(method, params, func(_ json.RawMessage, err error) {
		if err != nil {
			a.errorNotice(err)
		}
	})
}

func (a *App) sendLoop() {
	cn := a.conn
	for {
		cn.mu.Lock()
		if len(cn.queue) == 0 {
			cn.mu.Unlock()
			select {
			case <-cn.wake:
				continue
			case <-cn.c.Done():
				return
			}
		}
		x := cn.queue[0]
		cn.queue = cn.queue[1:]
		cn.mu.Unlock()
		var raw json.RawMessage
		err := cn.c.Call(context.Background(), x.method, x.params, &raw)
		if x.done != nil {
			a.ui.Do(func() { x.done(raw, err) })
		}
	}
}

// syncRPC waits until every request sent so far has been answered
// (tests). Call it off the UI goroutine.
func (a *App) syncRPC(timeout time.Duration) {
	done := make(chan struct{})
	a.ui.Do(func() { a.rpc("ping", nil, func(json.RawMessage, error) { close(done) }) })
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func (a *App) eventLoop() {
	for n := range a.conn.c.Events() {
		a.ui.Do(func() { a.onNotification(n) })
	}
	a.ui.Do(a.disconnected)
}

// disconnected is the runtime gone: a worker that crashed or exited.
func (a *App) disconnected() {
	if a.quitting {
		return
	}
	a.notice("The connection to the session runtime ended. Restart atto to continue (atto resume %s).", a.threadID)
	a.busy = false
}

// --- the mirror ---

// model is the thread's model, as the runtime said, resolved in this
// client's models (the name it shows).
func (a *App) model() config.ModelRef {
	if a.info.Model == "" {
		return config.ModelRef{}
	}
	if ref, ok := a.models.Find("", a.info.Model); ok {
		return ref
	}
	return config.ModelRef{}
}

func (a *App) effort() string { return a.info.Effort }

// setInfo takes the thread's state from info (thread/read, thread/updated).
func (a *App) setInfo(info server.ThreadInfo) {
	info.Items = nil
	oldGoal := a.info.Goal
	a.info = info
	if info.Goal != nil && oldGoal != nil && info.Goal.Goal != nil && oldGoal.Goal != nil {
		*oldGoal.Goal = *info.Goal.Goal
		copy := *info.Goal
		copy.Goal = oldGoal.Goal
		a.info.Goal = &copy
	}
	a.threadID, a.sessName, a.sessPath = info.ID, info.Name, info.SessionPath
	a.editor.Title = a.sessName
	a.ctxTokens = info.ContextTokens
	if info.Usage != nil {
		a.usage.set(*info.Usage)
	}
	a.jobCount, a.timerCount = info.Jobs, info.Timers
	a.readOnly = info.ReadOnly
	a.statusTrigger()
}

// set takes the session's usage totals.
func (u *usageStats) set(x server.Usage) {
	u.input, u.cached, u.cacheWrite, u.output, u.cost = x.InputTokens, x.CachedInputTokens, x.CacheWriteTokens, x.OutputTokens, x.Cost
	u.last.PromptTokens, u.last.CachedTokens = x.LastInputTokens, x.LastCachedInputTokens
	if x.Last != nil {
		u.last = *x.Last
	}
	u.lastCost = x.LastCost
}

// attachments are images the runtime gave back, as editor attachments.
func attachments(imgs []server.ItemImage) []tui.Attachment {
	var out []tui.Attachment
	for _, w := range imgs {
		im := provider.Image{File: w.File, MIME: w.MIME, Width: w.Width, Height: w.Height, Name: w.Name}
		if loaded, err := images.Load(im); err == nil {
			im = loaded
		}
		out = append(out, tui.Attachment{Label: images.Label(im), Value: im})
	}
	return out
}

// imageInputs stores the editor's images in the image store and names
// them for input/submit: the runtime reads them from there.
func imageInputs(att []tui.Attachment) ([]map[string]any, error) {
	var out []map[string]any
	for _, im := range attachedImages(att) {
		if len(im.Data) > 0 {
			if err := images.Save(im); err != nil {
				return nil, err
			}
		}
		out = append(out, map[string]any{"file": im.File, "mimeType": im.MIME, "width": im.Width, "height": im.Height, "name": im.Name})
	}
	return out, nil
}
