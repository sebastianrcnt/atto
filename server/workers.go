package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// WorkerRoutes lets a transport route execution to session workers without
// depending on their process supervisor. Open finds or starts one worker and
// returns a new client of it; the facade owns that client, not the worker.
// Configure it before serving clients. Closing the facade only detaches them.
type WorkerRoutes struct {
	Open func(context.Context, string, string, string, string, bool) (*Client, string, error)
	List func() ([]WorkerSummary, error)
}

type WorkerSummary struct {
	ID      string `json:"threadId"`
	Cwd     string `json:"cwd"`
	Busy    bool   `json:"busy"`
	Clients int    `json:"clients"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
}

type workerRoute struct {
	c        *Client
	thread   string
	instance string
}

type workerRouting struct {
	mu       sync.Mutex
	opening  sync.Mutex
	routes   map[string]map[string]*workerRoute // frontend client, thread
	watchers map[string]*workerRoute            // one ordered relay per worker incarnation
	owners   map[string]string                  // worker instance + client ID -> frontend ID
	cursors  map[string]map[int64]int64         // upstream cursor -> facade cursor
	progress map[string]int64                   // last upstream event relayed, per worker
	closed   bool
}

func (s *Server) routeCall(ctx context.Context, method string, raw json.RawMessage, p threadParams) (any, error, bool) {
	if s.Workers == nil {
		return nil, nil, false
	}
	if p.Offline {
		return nil, nil, false
	}
	if method == "thread/list" {
		out, err := s.listThreads(p)
		if err != nil {
			return nil, err, true
		}
		workers, err := s.Workers.List()
		if err != nil {
			return nil, err, true
		}
		rows := out.(map[string]any)["threads"].([]map[string]any)
		seen := map[string]bool{}
		for _, row := range rows {
			id, _ := row["threadId"].(string)
			for _, w := range workers {
				if w.ID == id {
					row["loaded"], row["busy"], row["clients"], row["version"], row["pid"] = true, w.Busy, w.Clients, w.Version, w.PID
					seen[id] = true
				}
			}
		}
		for _, w := range workers {
			if !seen[w.ID] && (p.Cwd == "" || p.Cwd == w.Cwd) && !p.Archived {
				rows = append(rows, map[string]any{"threadId": w.ID, "cwd": w.Cwd, "loaded": true, "busy": w.Busy, "clients": w.Clients, "version": w.Version, "pid": w.PID})
			}
		}
		return map[string]any{"threads": rows}, nil, true
	}
	if method != "thread/start" && p.ThreadID == "" {
		return nil, nil, false
	}
	if method == "initialize" || method == "initialized" || method == "ping" || method == "models/list" {
		return nil, nil, false
	}
	if method != "thread/start" && method != "thread/resume" && method != "thread/read" && method != "thread/attach" && method != "thread/detach" && method != "thread/close" {
		if _, ok := threadMethods[method]; !ok {
			return nil, nil, false
		}
	}
	r, err := s.workerRoute(ctx, p, method == "thread/start")
	if err != nil {
		return nil, err, true
	}
	params := map[string]any{}
	_ = json.Unmarshal(raw, &params)
	if params == nil {
		params = map[string]any{}
	}
	params["threadId"] = r.thread
	if method == "thread/start" || method == "thread/resume" {
		method = "thread/attach"
	}
	if method == "thread/close" {
		s.forgetRoute(clientOf(ctx), r)
	}
	var out map[string]any
	if err := r.c.Call(ctx, method, params, &out); err != nil {
		// A worker ends with its session: losing it while closing is
		// the close having happened.
		if method != "thread/close" || !errors.Is(err, ErrClosed) {
			return nil, err, true
		}
	}
	if method == "thread/detach" || method == "thread/close" {
		s.forgetRoute(clientOf(ctx), r)
		r.c.Close()
	}
	if out == nil {
		out = map[string]any{}
	}
	if seq, ok := out["eventId"].(float64); ok {
		// A snapshot's cursor must refer to the facade's stream, not to a
		// different worker's sequence. Wait for its covered events first.
		for {
			s.routing.mu.Lock()
			caught := s.routing.progress[r.instance] >= int64(seq)
			s.routing.mu.Unlock()
			if caught {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err(), true
			case <-r.c.Done():
				return nil, ErrClosed, true
			case <-time.After(time.Millisecond):
			}
		}
		s.routing.mu.Lock()
		out["eventId"], out["serverInstanceId"] = s.routing.cursors[r.instance][int64(seq)], s.instance
		s.routing.mu.Unlock()
	}
	s.routing.mu.Lock()
	s.remapWorkerClients(out, r.instance)
	s.routing.mu.Unlock()
	return out, nil, true
}

func (s *Server) workerRoute(ctx context.Context, p threadParams, start bool) (*workerRoute, error) {
	routing := &s.routing
	routing.opening.Lock()
	defer routing.opening.Unlock()
	client := clientOf(ctx)
	routing.mu.Lock()
	if routing.closed {
		routing.mu.Unlock()
		return nil, ErrClosed
	}
	if !start {
		if r := routing.routes[client][p.ThreadID]; r != nil {
			select {
			case <-r.c.Done():
				r.c.Close()
				delete(routing.routes[client], p.ThreadID)
			default:
				routing.mu.Unlock()
				return r, nil
			}
		}
	}
	routing.mu.Unlock()
	cwd := p.Cwd
	if cwd == "" {
		cwd = s.Cwd
	}
	id := p.ThreadID
	if start {
		id = ""
	}
	c, id, err := s.Workers.Open(ctx, id, cwd, p.Model, p.Effort, p.DeferStart)
	if err != nil {
		return nil, err
	}
	var init struct {
		Client   string `json:"clientId"`
		Instance string `json:"serverInstanceId"`
		EventID  int64  `json:"eventId"`
		Protocol int    `json:"protocolVersion"`
	}
	var capabilities *Capabilities
	if cc := connOf(ctx); cc != nil {
		s.mu.Lock()
		capabilities = &Capabilities{Interactive: cc.interactive || s.httpStreams.Load() > 0, Images: true}
		s.mu.Unlock()
	}
	err = c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{ProtocolVersion}, "clientInfo": ClientInfo{Name: "atto-gateway", Version: s.Version}, "capabilities": capabilities}, &init)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("session worker negotiation: %w", err)
	}
	if init.Protocol != ProtocolVersion || init.Instance == "" {
		c.Close()
		return nil, failure(ReasonUnsupportedProtocol, "the session worker cannot negotiate protocol %d; close it with atto daemon stop -force and reconnect", ProtocolVersion)
	}
	if err = c.Call(ctx, "initialized", nil, nil); err != nil {
		c.Close()
		return nil, err
	}
	r := &workerRoute{c: c, thread: id, instance: init.Instance}
	routing.mu.Lock()
	if routing.routes == nil {
		routing.routes = map[string]map[string]*workerRoute{}
		routing.watchers = map[string]*workerRoute{}
		routing.owners = map[string]string{}
		routing.progress = map[string]int64{}
		routing.cursors = map[string]map[int64]int64{}
	}
	if routing.routes[client] == nil {
		routing.routes[client] = map[string]*workerRoute{}
	}
	routing.routes[client][id] = r
	routing.owners[init.Instance+"/"+init.Client] = client
	watcher := routing.watchers[init.Instance]
	if watcher != nil {
		select {
		case <-watcher.c.Done():
			watcher = nil
		default:
		}
	}
	routing.mu.Unlock()
	if watcher == nil {
		// Independent RPC connections preserve client identity, but their
		// subscriptions can start at different cursors. One observer relays
		// the stream in order; otherwise a newer subscription could outrun
		// and accidentally suppress older notifications still being read.
		watch, _, err := s.Workers.Open(ctx, id, cwd, "", "", false)
		if err != nil {
			s.forgetRoute(client, r)
			c.Close()
			return nil, err
		}
		var baseline struct {
			Instance string `json:"serverInstanceId"`
			EventID  int64  `json:"eventId"`
		}
		err = watch.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{ProtocolVersion}, "clientInfo": ClientInfo{Name: "atto-gateway-observer", Version: s.Version}}, &baseline)
		if err != nil || baseline.Instance != init.Instance {
			watch.Close()
			s.forgetRoute(client, r)
			c.Close()
			if err == nil {
				err = ErrClosed
			}
			return nil, err
		}
		watcher = &workerRoute{c: watch, thread: id, instance: init.Instance}
		routing.mu.Lock()
		routing.watchers[init.Instance] = watcher
		routing.progress[init.Instance] = baseline.EventID
		routing.cursors[init.Instance] = map[int64]int64{baseline.EventID: s.eventSeq()}
		routing.mu.Unlock()
		go s.forwardWorker(watcher)
	}
	go func() {
		for range c.Events() {
		}
	}()
	return r, nil
}

func (s *Server) forwardWorker(r *workerRoute) {
	closedThread := false
	for n := range r.c.Events() {
		var p map[string]any
		if json.Unmarshal(n.Params, &p) != nil {
			continue
		}
		s.routing.mu.Lock()
		if n.Method != "events/reset" && n.EventID > 0 && n.EventID <= s.routing.progress[r.instance] {
			s.routing.mu.Unlock()
			continue
		}
		s.remapWorkerClients(p, r.instance)
		delete(p, "eventId")
		if n.Method == "thread/updated" {
			if th, ok := p["thread"].(map[string]any); ok {
				th["eventId"], th["serverInstanceId"] = s.eventSeq()+1, s.instance
			}
		}
		if n.Method == "thread/closed" {
			closedThread = true
		}
		s.publish(n.Method, p)
		if n.EventID > 0 {
			s.routing.progress[r.instance] = n.EventID
			s.routing.cursors[r.instance][n.EventID] = s.eventSeq()
			delete(s.routing.cursors[r.instance], n.EventID-10000)
		}
		s.routing.mu.Unlock()
	}
	// A crashed upstream keeps no valid live view. Ask surviving frontend
	// clients to read again; their next request finds or restarts the worker.
	s.routing.mu.Lock()
	present := false
	for _, routes := range s.routing.routes {
		for _, current := range routes {
			if current.instance == r.instance {
				present = true
			}
		}
	}
	if s.routing.watchers[r.instance] == r {
		delete(s.routing.watchers, r.instance)
	}
	if present && !closedThread && !s.routing.closed {
		s.publish("events/reset", map[string]any{"threadId": r.thread, "serverInstanceId": s.instance, "eventId": s.eventSeq() + 1})
	}
	s.routing.mu.Unlock()

}

func (s *Server) remapWorkerClients(v any, instance string) {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "clientId" || k == "by" {
				if id, ok := v.(string); ok {
					if mapped, found := s.routing.owners[instance+"/"+id]; found {
						x[k] = mapped
					} else if id != "" {
						x[k] = instance + "/" + id
					}
				}
			} else {
				s.remapWorkerClients(v, instance)
			}
		}
	case []any:
		for _, v := range x {
			s.remapWorkerClients(v, instance)
		}
	}
}

func (s *Server) forgetRoute(client string, route *workerRoute) {
	s.routing.mu.Lock()
	if s.routing.routes[client][route.thread] == route {
		delete(s.routing.routes[client], route.thread)
	}
	s.routing.mu.Unlock()
}

func (s *Server) detachRoutes(client string, all bool) {
	s.routing.opening.Lock()
	defer s.routing.opening.Unlock()
	s.routing.mu.Lock()
	var clients []*Client
	for id, routes := range s.routing.routes {
		if !all && id != client {
			continue
		}
		for _, r := range routes {
			clients = append(clients, r.c)
		}
		delete(s.routing.routes, id)
	}
	if all {
		s.routing.closed = true
		for _, watch := range s.routing.watchers {
			clients = append(clients, watch.c)
		}
		s.routing.watchers = map[string]*workerRoute{}
	}
	s.routing.mu.Unlock()
	for _, c := range clients {
		c.Close()
	}
}

// routedScope preserves /remote's live shapes and frontend-only commands while
// the execution itself belongs to a worker. The same facade can switch threads.
func (s *Server) routedScope(ctx context.Context, sc *Scope, method string, p threadParams) (any, error) {
	if sc.Thread == nil {
		return nil, invalid("the live session is unavailable")
	}
	id := sc.Thread()
	if p.ThreadID != "" && p.ThreadID != id && method != "initialize" && method != "models/list" {
		return nil, invalid("thread %q is no longer the live session (now %q): thread/read it", p.ThreadID, id)
	}
	p.ThreadID = id
	p.Offline = false // a live scope always reads its current runtime
	original := method
	switch method {
	case "initialize":
		return s.initialize(ctx, p, map[string]any{"live": true, "threadId": id})
	case "initialized", "ping":
		return nil, nil
	case "models/list":
		return s.listModels()
	case "thread/start":
		return nil, failure(ReasonUnsupported, "this is atto's live session: send /clear to start a new conversation")
	case "thread/list":
		return map[string]any{"threads": []map[string]any{{"threadId": id, "cwd": s.Cwd, "loaded": true, "live": true}}}, nil
	case "turn/start", "turn/steer":
		if strings.TrimSpace(p.Input) == "" && len(p.Images) == 0 {
			return nil, invalid("input is required")
		}
		if strings.HasPrefix(p.Input, "/") && len(p.Images) == 0 && sc.Local != nil && sc.Local(p.Input) {
			return map[string]any{"status": StatusDone}, nil
		}
		method = "input/submit"
	case "thread/compact":
		method, p.Input = "input/submit", "/compact"
	}
	raw, _ := json.Marshal(p)
	out, err, ok := s.routeCall(ctx, method, raw, p)
	if !ok {
		return nil, &rpcError{Code: codeMethodNotFound, Message: "unknown method " + method}
	}
	if err == nil && original == "thread/rollback" {
		s.Switched(id, id)
	}
	if method == "thread/read" || method == "thread/resume" || method == "thread/attach" || original == "thread/rollback" {
		if m, ok := out.(map[string]any); ok {
			m["live"] = true
		}
	}
	return out, err
}

// HTTP revision-1 clients share the handler identity and become interactive
// when they follow its SSE stream, even without initialize capabilities.
func (s *Server) routeInteractive(client string, interactive bool) {
	if s.Workers == nil {
		return
	}
	s.routing.mu.Lock()
	var clients []*Client
	for _, r := range s.routing.routes[client] {
		clients = append(clients, r.c)
	}
	s.routing.mu.Unlock()
	for _, c := range clients {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{ProtocolVersion}, "capabilities": Capabilities{Interactive: interactive, Images: true}}, nil)
		}()
	}
}
