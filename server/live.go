package server

import (
	"strings"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
)

// Live is a conversation another front end runs, such as the TUI's own
// session under /remote. A server made with NewLive serves it as its only
// thread, with the same protocol and web client as its own threads: the
// thread ID is the session ID, and when the front end switches sessions
// (/clear, /resume) the server says so with thread/switched.
//
// The methods are called from HTTP handlers, concurrently: an
// implementation runs them on its own goroutine or under its own lock.
type Live interface {
	// Thread describes the conversation; with items, its transcript so
	// far, items still streaming included as they stand. at, when not nil,
	// is called while the snapshot is taken, with no notification of the
	// live thread published in between, so a client knows from which event
	// on to follow it.
	Thread(items bool, at func()) (ThreadInfo, error)
	// Model is the model in use, which decides whether images are taken.
	Model() config.ModelRef
	// Send delivers input as if typed in the front end: a new turn when
	// idle; while a turn runs, a steer or a queued turn. It reports which
	// ("started", "steered" or "queued") and the turn it started.
	Send(input string, images []provider.Image) (status, turnID string, err error)
	// Interrupt stops what runs, as Esc does; false when nothing runs.
	Interrupt() bool
	// Background moves the running command to the background, as Ctrl+B
	// does; false when no command runs.
	Background() bool
	SetModel(id string) (ThreadInfo, error)
	SetEffort(level string) (ThreadInfo, error)
	// Answer answers the open prompt id (see Prompt) as if in the front
	// end; an error when it is no longer open or the answer does not fit.
	Answer(id string, ans PromptAnswer) error
	// Unsteer takes back a pending steer equal to input (queued: a queued
	// follow-up), as editing it in the front end does; an error when the
	// turn has taken it.
	Unsteer(input string, queued bool) error
	// Rollback goes back to before the numTurns-th last user message, as
	// picking it in /tree does, and returns its text; idle only.
	Rollback(numTurns int) (string, error)
}

// NewLive makes a server for one live conversation. Its notifications
// come from the front end through Publish.
func NewLive(version string, live Live) *Server {
	return &Server{Version: version, live: live, threads: map[string]*thread{}, Notify: func(string, map[string]any) {}, stop: make(chan struct{}), instance: newInstanceID()}
}

// Publish sends a notification of the live thread to clients. The front
// end calls it in the order things happen, from one goroutine at a time.
func (s *Server) Publish(method string, params map[string]any) { s.Notify(method, params) }

// WireItem is the protocol form of a transcript item of session sid.
func WireItem(sid string, it *transcript.Item) Item { return wireItem(sid, it) }

// liveCall serves the protocol for a live conversation.
func (s *Server) liveCall(method string, p threadParams) (any, error) {
	l := s.live
	cur := func() (ThreadInfo, error) {
		info, err := l.Thread(false, nil)
		if err != nil {
			return info, err
		}
		if p.ThreadID != "" && p.ThreadID != info.ID {
			return info, invalid("thread %q is no longer the live session (now %q): thread/read it", p.ThreadID, info.ID)
		}
		return info, nil
	}
	switch method {
	case "initialize":
		info, err := l.Thread(false, nil)
		if err != nil {
			return nil, err
		}
		return s.initialize(p, map[string]any{"live": true, "threadId": info.ID})
	case "initialized":
		return nil, nil
	case "models/list":
		return s.listModels()
	case "thread/list":
		info, err := l.Thread(false, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"threads": []map[string]any{{
			"threadId": info.ID, "name": info.Name, "cwd": info.Cwd, "loaded": true, "live": true,
		}}}, nil
	case "thread/start":
		return nil, &rpcError{Code: codeServer, Message: "this is atto's live session: send /clear to start a new conversation"}
	case "thread/read", "thread/resume":
		var seq int64
		info, err := l.Thread(true, func() { seq = s.eventSeq() })
		if err != nil {
			return nil, err
		}
		if p.ThreadID != "" && p.ThreadID != info.ID {
			return nil, invalid("thread %q is no longer the live session (now %q)", p.ThreadID, info.ID)
		}
		info.EventID = seq
		info.Live = true
		return info, nil
	case "thread/setModel":
		if _, err := cur(); err != nil {
			return nil, err
		}
		return l.SetModel(p.Model)
	case "thread/setEffort":
		if _, err := cur(); err != nil {
			return nil, err
		}
		return l.SetEffort(p.Effort)
	case "thread/compact":
		if _, err := cur(); err != nil {
			return nil, err
		}
		_, turnID, err := l.Send("/compact", nil)
		return map[string]any{"turnId": turnID}, err
	case "turn/start", "turn/steer":
		if _, err := cur(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Input) == "" && len(p.Images) == 0 {
			return nil, invalid("input is required")
		}
		imgs, err := turnImages(p.Images, l.Model())
		if err != nil {
			return nil, err
		}
		status, turnID, err := l.Send(images.WithPlaceholders(p.Input, imgs), imgs)
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": status, "turnId": turnID}, nil
	case "turn/interrupt":
		if _, err := cur(); err != nil {
			return nil, err
		}
		l.Interrupt()
		return nil, nil
	case "turn/background":
		if _, err := cur(); err != nil {
			return nil, err
		}
		if !l.Background() {
			return nil, &rpcError{Code: codeServer, Message: "no command is running that can move to the background"}
		}
		return nil, nil
	case "thread/rollback":
		if _, err := cur(); err != nil {
			return nil, err
		}
		n := p.NumTurns
		if n == 0 {
			n = 1
		}
		if n < 0 {
			return nil, invalid("numTurns must be positive")
		}
		text, err := l.Rollback(n)
		if err != nil {
			return nil, err
		}
		// The front end says thread/switched; this is the result as
		// atto serve's, with the transcript as it is now.
		var seq int64
		info, err := l.Thread(true, func() { seq = s.eventSeq() })
		if err != nil {
			return nil, err
		}
		info.EventID, info.Live = seq, true
		return struct {
			ThreadInfo
			Input string `json:"input"`
		}{info, text}, nil
	case "turn/unsteer":
		if _, err := cur(); err != nil {
			return nil, err
		}
		if err := l.Unsteer(p.Input, p.Queued); err != nil {
			return nil, &rpcError{Code: codeServer, Message: err.Error()}
		}
		return nil, nil
	// Keep subagent/* dispatch for the frozen web client.
	case "job/list", "job/output", "job/stop", "agent/list", "agent/read", "subagent/list", "subagent/read":
		info, err := cur()
		if err != nil {
			return nil, err
		}
		return background(method, info.ID, p)
	case "prompt/answer":
		if _, err := cur(); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, invalid("id is required")
		}
		if !p.Cancel && p.Index == nil && p.Text == nil {
			return nil, invalid("index, text or cancel is required")
		}
		if err := l.Answer(p.ID, PromptAnswer{Index: p.Index, Text: p.Text, Cancel: p.Cancel}); err != nil {
			return nil, invalid("%v", err)
		}
		return nil, nil
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "unknown method " + method}
}
