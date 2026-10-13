package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/ui"
)

// Handle processes one JSON-RPC message and returns the response (nil for
// notifications sent by the client).
func (s *Server) Handle(ctx context.Context, raw []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: codeParse, Message: err.Error(), Data: &ErrorData{Reason: ReasonParse}}}
	}
	result, err := s.call(ctx, req.Method, req.Params)
	if req.ID == nil {
		return nil
	}
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	var rerr *rpcError
	switch {
	case errors.As(err, &rerr):
		copy := *rerr
		if copy.Data == nil {
			copy.Data = &ErrorData{Reason: errorReason(copy.Code)}
		}
		resp.Error = &copy
	case err != nil:
		resp.Error = failure(ReasonInternal, "%s", err)
	default:
		if result == nil {
			result = map[string]any{}
		}
		resp.Result = result
	}
	return resp
}

func (e *rpcError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return v, invalid("%v", err)
		}
	}
	return v, nil
}

type threadParams struct {
	Site                ui.Site         `json:"site"`
	Key                 string          `json:"key"`
	EventType           ui.EventType    `json:"type"`
	Value               *string         `json:"value"`
	Rev                 int64           `json:"rev"`
	UICapabilities      ui.Capabilities `json:"-"`
	Surface             string          `json:"surface"`
	Width               int             `json:"width"`
	Elements            []string        `json:"elements"`
	ThreadID            string          `json:"threadId"`
	ItemID              string          `json:"itemId"`
	Query               string          `json:"query"`
	Limit               int             `json:"limit"`
	Before              string          `json:"before"`
	Preview             bool            `json:"preview"`
	DeferStart          bool            `json:"deferStart"` // TUI waits for its startup project-trust decision
	Cwd                 string          `json:"cwd"`
	Model               string          `json:"model"`
	Provider            string          `json:"provider"`
	APIKey              string          `json:"apiKey"`
	OAuth               bool            `json:"oauth"`
	Effort              string          `json:"effort"`
	Input               string          `json:"input"`
	Archived            bool            `json:"archived"`
	IncludeAgents       bool            `json:"includeAgents"`
	IncludeClosedAgents bool            `json:"includeClosedAgents"`
	IncludeArchived     bool            `json:"includeArchived"`
	Stop                bool            `json:"stop"`
	NumTurns            int             `json:"numTurns"`
	// Images go with turn/start's and input/submit's input (see images.go).
	Images []ImageInput `json:"images"`
	// input/submit: auto, queue, replace or steer.
	Intent string `json:"intent"`
	// turn/interrupt: cancel (Ctrl+C: steers come back) or sendPending
	// (Esc: they go out at once, the default).
	Mode string `json:"mode"`
	// prompt/answer
	ID      string  `json:"id"`
	Prompt  *Prompt `json:"prompt"`
	Index   *int    `json:"index"`
	Indexes *[]int  `json:"indexes"`
	Text    *string `json:"text"`
	Cancel  bool    `json:"cancel"`
	// turn/unsteer: an input ID
	InputID string `json:"inputId"`
	// job/output, job/stop
	Turn    int    `json:"turn"` // agent/turn: the agent's turn to run
	Job     int    `json:"job"`
	Lines   int    `json:"lines"`
	Name    string `json:"name"`
	AgentID string `json:"agentId"` // agent/read: the agent's session ID or unique prefix
	// initialize
	ProtocolVersions []int         `json:"protocolVersions"`
	Client           *ClientInfo   `json:"clientInfo"`
	Capabilities     *Capabilities `json:"capabilities"`
	// thread/setModel, thread/setEffort: also make it the default.
	SaveDefault bool `json:"saveDefault"`
	// thread/setContextMode: normal or long
	ContextMode string `json:"contextMode"`
	// thread/navigate, thread/fork, thread/setLabel
	EntryID string         `json:"entryId"`
	Summary *summaryParams `json:"summary"`
	Label   string         `json:"label"`
	Reason  string         `json:"reason"`  // thread/detach, thread/close
	Open    bool           `json:"open"`    // client/gate
	View    string         `json:"view"`    // thread/context
	Offline bool           `json:"offline"` // thread/read: from the file, not loading it
	Args    string         `json:"args"`    // commands/run
	Command string         `json:"command"` // shell/start
	Exclude bool           `json:"exclude"` // shell/start
	When    string         `json:"when"`    // timer/create
	Message string         `json:"message"` // timer/create
}

type summaryParams struct {
	Mode         string `json:"mode"` // none, auto or custom
	Instructions string `json:"instructions"`
}

func (s *Server) call(ctx context.Context, method string, raw json.RawMessage) (out any, callErr error) {
	select {
	case <-s.stop:
		return nil, errThreadClosed
	default:
	}
	switch method {
	case "thread/start", "thread/resume", "thread/setModel", "thread/setEffort", "thread/compact", "thread/rollback", "thread/navigate", "thread/fork", "thread/reload", "thread/setContextMode", "input/submit", "turn/start", "turn/steer", "turn/unsteer", "turn/background", "turn/interrupt", "shell/start", "job/stop", "commands/run", "goal/set", "goal/edit", "goal/resume":
		if s.memory != nil {
			s.memory.Begin()
			defer s.memory.End()
		}
	}
	p, err := decode[threadParams](raw)
	if err != nil {
		return nil, err
	}
	defer func() {
		if callErr == nil {
			out, callErr = s.snapshotResult(ctx, method, p, out)
		}
	}()
	switch method {
	case "thread/archive", "thread/unarchive", "thread/delete":
		return s.mutateSession(ctx, method, p)
	}
	if out, err, ok := s.routeCall(ctx, method, raw, p); ok {
		return out, err
	}
	client := clientOf(ctx)
	if connOf(ctx) == nil {
		client = "legacy"
	}
	switch method {
	case "initialize":
		return s.initialize(ctx, p)
	case "ui/capabilities":
		return s.uiCapabilities(ctx, p)
	case "initialized", "ping":
		return nil, nil
	case "models/list":
		return s.listModels()
	case "thread/start":
		return s.startThread(client, p)
	case "thread/resume":
		return s.resumeThread(client, p)
	case "thread/attach":
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		return t.attach(client)
	case "thread/detach":
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		return s.detach(t, client, p.Reason), nil
	case "thread/close":
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		reason := p.Reason
		if reason == "" {
			reason = "other"
		}
		return s.closeThread(t, closeMode{reason: reason}), nil
	case "worker/retire":
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		return s.retireForUpgrade(t), nil
	case "thread/items", "thread/entry":
		if p.Offline {
			path, err := session.Find(p.ThreadID)
			if err != nil {
				return nil, err
			}
			if method == "thread/entry" {
				e, ok, err := session.ReadEntry(path, p.EntryID)
				if !ok && err == nil {
					err = failure(ReasonNotFound, "entry not found")
				}
				return e, err
			}
			b := transcript.Builder{IDPrefix: itemPrefix(p.ThreadID)}
			page, _, err := replayFile(&b, p.ThreadID, path, p.Before, p.Limit)
			return page, err
		}

	case "item/image", "item/output":
		if p.Offline {
			path, err := session.Find(p.ThreadID)
			if err != nil {
				return nil, err
			}
			n := itemNumber(p.ThreadID, p.ItemID)
			if n < 1 {
				return nil, invalid("itemId is not in this thread's transcript")
			}
			b := transcript.Builder{IDPrefix: itemPrefix(p.ThreadID)}
			page, _, err := replayFile(&b, p.ThreadID, path, itemPrefix(p.ThreadID)+strconv.Itoa(n+1), 1)
			if err != nil {
				return nil, err
			}
			kind := "output"
			if method == "item/image" {
				kind = "image"
			}
			out, err := itemResource(page.Items, p, kind)
			if err != nil {
				return nil, err
			}
			if r, ok := out.(resourceRequest); ok {
				return readResource(r)
			}
			return out, nil
		}
	case "thread/read":
		if p.Offline {
			return readOffline(p.ThreadID)
		}
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		var info ThreadInfo
		err = t.call(func() error { info = t.snapshot(); return nil })
		return info, err
	case "thread/tree":
		if p.Offline {
			path, err := session.Find(p.ThreadID)
			if err != nil {
				return nil, err
			}
			if p.Query != "" {
				matches, err := session.SearchTree(path, p.Query)
				return map[string]any{"matches": matches}, err
			}
			rows, err := session.ReadTreeRows(path)
			leaf := ""
			if len(rows) > 0 {
				leaf = rows[len(rows)-1].ID
			}
			return map[string]any{"entries": rows, "leaf": leaf}, err
		}
	case "thread/list":
		return s.listThreads(p)
	}
	if out, ok, err := s.threadCall(ctx, client, method, p); ok {
		return out, err
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "unknown method " + method}
}
