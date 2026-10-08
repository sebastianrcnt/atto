package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/hooks"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/mcp"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// Server holds threads and dispatches JSON-RPC requests. Notifications
// go to the Notify function (a transport fan-out).
type Server struct {
	Version string
	Cwd     string // default working directory for new threads
	Notify  func(method string, params map[string]any)

	// OnClients, when set, hears how many clients follow the event stream
	// as it changes (set before HTTPHandler).
	OnClients func(n int)

	memory  core.IdleMemory
	mu      sync.Mutex
	threads map[string]*thread
	stop    chan struct{}
	live    Live    // set by NewLive: the one conversation served
	events  *broker // the HTTP transport's, for eventSeq
}

func New(version, cwd string) *Server {
	s := &Server{Version: version, Cwd: cwd, threads: map[string]*thread{}, Notify: func(string, map[string]any) {}, stop: make(chan struct{})}
	s.memory = core.NewIdleMemory()
	go s.watchInbox()
	return s
}

// watchInbox delivers inbox events (job exits, timers, monitors) to loaded
// threads: a new turn when idle, a steer while a turn runs.
func (s *Server) watchInbox() {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
		s.mu.Lock()
		threads := make([]*thread, 0, len(s.threads))
		for _, t := range s.threads {
			threads = append(threads, t)
		}
		s.mu.Unlock()
		for _, t := range threads {
			reload, evs := events.SplitReload(core.Poll(t.id))
			if reload {
				s.reload(t)
			}
			if len(evs) == 0 {
				continue
			}
			t.mu.Lock()
			busy := t.busy
			t.mu.Unlock()
			if !busy && !events.Wakes(evs) {
				events.Requeue(t.id, evs) // quiet: for the next turn
				continue
			}
			for _, e := range evs {
				s.notify(t, "event", map[string]any{"title": e.Title, "source": e.Source})
			}
			text := events.Format(evs)
			if busy {
				t.agent.Steer(text)
				continue
			}
			s.beginInboxTurn(t, evs)
		}
	}
}

// beginInboxTurn preserves the inbox if a concurrent request claimed the turn.
func (s *Server) beginInboxTurn(t *thread, evs []events.Event) {
	text := events.Format(evs)
	if _, err := s.begin(t, func(ctx context.Context, emit func(any)) error {
		emit(transcript.Input{Text: text})
		return t.agent.Run(ctx, text, emit)
	}); err != nil {
		events.Requeue(t.id, evs)
	}
}

type thread struct {
	mu sync.Mutex
	// feed is held while the transcript builder takes an event and its
	// item notifications go out, so a snapshot of the items (see
	// snapshot) and the event ID to follow them from agree. Take it
	// before mu.
	feed      sync.Mutex
	id        string
	cwd       string
	name      string
	agent     *agent.Agent
	sess      *session.Writer
	release   func()
	closing   bool
	done      chan struct{}
	hooks     *hooks.Runner
	ext       *extensions.Manager
	mcp       *mcp.Manager
	hookSrc   []config.HookSource
	models    config.ModelsFile  // the configured models, for the names clients show
	loaded    core.Loaded        // what it loaded, as of the last reload
	tr        transcript.Builder // used by the running turn, or by restore while idle
	items     []Item             // completed items
	busy      bool
	turnID    string
	cancel    context.CancelFunc
	turnSeq   int
	ctxTokens int
	usage     provider.Usage // totals for the running turn
	total     Usage          // the session's totals
	turn      TurnInfo       // the running turn, for the activity line
	steers    []string       // the user's steers the turn has not taken
	// What the extensions show (see extui.go): on the blocks of the
	// items, around the input, and how many text blocks they added.
	blocks   blocks
	ui       extensions.UIState
	extTexts int
}

// Close interrupts running turns, stops background jobs and closes session
// files (Windows cannot delete or move a file that is still open).
func (s *Server) Close() {
	if s.memory != nil {
		s.memory.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		return
	default:
		close(s.stop)
	}
	var ending sync.WaitGroup
	for _, t := range s.threads {
		core.Leave(t.id)
		t.mu.Lock()
		t.closing = true
		done := t.done
		if t.cancel != nil {
			t.cancel()
		}
		t.mu.Unlock()
		if done != nil {
			<-done
		}
		if t.hooks != nil { // threads end together, so one slow hook costs little
			ending.Go(func() {
				t.hooks.SessionEnd(context.Background(), "other")
			})
		}
		if t.mcp != nil {
			ending.Go(func() {
				_ = t.mcp.Close() // its servers end with the thread
			})
		}
		if t.ext != nil {
			ending.Go(func() {
				t.ext.SessionEnd("other")
				t.ext.Close()
			})
		}
	}
	ending.Wait()
	// Last: SessionEnd hooks and extensions may still write to the session.
	for _, t := range s.threads {
		t.sess.Close()
		if t.release != nil {
			t.release()
		}
	}
}

func (t *thread) info() ThreadInfo {
	m, effort := t.agent.Current()
	info := ThreadInfo{ID: t.id, Cwd: t.cwd, Name: t.name, Effort: effort, ContextTokens: t.ctxTokens, Busy: t.busy, TurnID: t.turnID}
	SetModel(&info, m, t.models)
	info.AutoCompactLimit, _ = t.agent.CompactionLimit()
	total := t.total
	info.Usage = &total
	if t.busy {
		turn := t.turn
		info.Turn = &turn
	}
	info.Pending = t.pending()
	return info
}

// pending is the input the turn has not taken (nil when none). Call with
// t.mu held.
func (t *thread) pending() *PendingInput {
	if len(t.steers) == 0 {
		return nil
	}
	return &PendingInput{Steers: slices.Clone(t.steers)}
}

// pendingChanged tells clients what input is pending now.
func (s *Server) pendingChanged(t *thread) {
	t.mu.Lock()
	p := t.pending()
	t.mu.Unlock()
	if p == nil {
		p = &PendingInput{Steers: []string{}}
	}
	s.notify(t, "turn/pending", map[string]any{"pending": p})
}

// eventSeq is the ID of the latest event the HTTP transport published (0
// without one): a client that reads a thread follows its events from there.
func (s *Server) eventSeq() int64 {
	if s.events == nil {
		return 0
	}
	return s.events.last()
}

// --- dispatch ---

// Handle processes one JSON-RPC message and returns the response (nil for
// notifications sent by the client).
func (s *Server) Handle(ctx context.Context, raw []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeParse, err.Error()}}
	}
	result, err := s.call(ctx, req.Method, req.Params)
	if req.ID == nil {
		return nil
	}
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	var rerr *rpcError
	switch {
	case errors.As(err, &rerr):
		resp.Error = rerr
	case err != nil:
		resp.Error = &rpcError{codeServer, err.Error()}
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
	return &rpcError{codeInvalidParams, fmt.Sprintf(format, args...)}
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
	ThreadID string `json:"threadId"`
	Cwd      string `json:"cwd"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Input    string `json:"input"`
	Archived bool   `json:"archived"`
	NumTurns int    `json:"numTurns"`
	// Images go with turn/start's input (see images.go).
	Images []ImageInput `json:"images"`
	// prompt/answer (live sessions)
	ID     string  `json:"id"`
	Index  *int    `json:"index"`
	Text   *string `json:"text"`
	Cancel bool    `json:"cancel"`
	// turn/unsteer: a queued follow-up rather than a steer
	Queued bool `json:"queued"`
	// job/output, job/stop; subagent/read
	Job   int    `json:"job"`
	Lines int    `json:"lines"`
	Name  string `json:"name"`
}

func (s *Server) call(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	// Read-only polling is not input: a browser following an idle thread
	// must not keep postponing its memory release.
	switch method {
	case "thread/start", "thread/resume", "thread/setModel", "thread/setEffort", "thread/compact", "thread/rollback",
		"turn/start", "turn/steer", "turn/unsteer", "turn/background", "turn/interrupt", "job/stop":
		if s.memory != nil {
			s.memory.Begin()
			defer s.memory.End()
		}
	}
	p, err := decode[threadParams](raw)
	if err != nil {
		return nil, err
	}
	if s.live != nil {
		return s.liveCall(method, p)
	}
	switch method {
	case "initialize":
		return map[string]any{"name": "atto", "version": s.Version, "protocolVersion": ProtocolVersion, "eventId": s.eventSeq(), "settings": clientSettings()}, nil
	case "models/list":
		return s.listModels()
	case "thread/start":
		return s.startThread(p)
	case "thread/resume":
		return s.resumeThread(p.ThreadID)
	case "thread/read":
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		return s.snapshot(t), nil
	case "thread/list":
		return s.listThreads(p)
	case "thread/setModel":
		return s.setModel(p)
	case "thread/setEffort":
		return s.setEffort(p)
	case "thread/compact":
		return s.startCompact(p.ThreadID)
	case "thread/rollback":
		return s.rollback(p)
	case "turn/start":
		return s.startTurn(p)
	case "turn/steer":
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		t.mu.Lock()
		busy := t.busy
		t.mu.Unlock()
		if !busy {
			return nil, &rpcError{codeServer, "no turn is running; use turn/start"}
		}
		if strings.TrimSpace(p.Input) == "" {
			return nil, invalid("input is required")
		}
		t.agent.Steer(p.Input)
		t.mu.Lock()
		t.steers = append(t.steers, p.Input)
		t.mu.Unlock()
		s.pendingChanged(t)
		return nil, nil
	case "turn/unsteer":
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		if p.Queued {
			return nil, invalid("only a live session queues follow-ups")
		}
		t.mu.Lock()
		i := slices.Index(t.steers, p.Input)
		t.mu.Unlock()
		if i < 0 || !t.agent.Unsteer(p.Input) {
			return nil, &rpcError{codeServer, "that message is no longer pending: the turn has taken it"}
		}
		t.mu.Lock()
		if i = slices.Index(t.steers, p.Input); i >= 0 {
			t.steers = slices.Delete(t.steers, i, i+1)
		}
		t.mu.Unlock()
		s.pendingChanged(t)
		return nil, nil
	case "job/list", "job/output", "job/stop", "subagent/list", "subagent/read":
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		return background(method, t.id, p)
	case "turn/background":
		// Ctrl+B: the running command moves to the background.
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		if !t.agent.Background() {
			return nil, &rpcError{codeServer, "no command is running that can move to the background"}
		}
		return nil, nil
	case "turn/interrupt":
		t, err := s.thread(p.ThreadID)
		if err != nil {
			return nil, err
		}
		t.mu.Lock()
		if t.cancel != nil {
			t.cancel()
		}
		t.mu.Unlock()
		return nil, nil
	}
	return nil, &rpcError{codeMethodNotFound, "unknown method " + method}
}

func (s *Server) thread(id string) (*thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.threads[id]
	if t == nil {
		return nil, invalid("unknown thread %q (thread/start or thread/resume first)", id)
	}
	return t, nil
}

// --- threads ---

func (s *Server) listModels() (any, error) {
	_, models, err := core.Load()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range models.List() {
		out = append(out, map[string]any{
			"id": r.ProviderName + "/" + r.Model.ID, "name": models.DisplayName(r),
			"contextWindow": r.Model.ContextWindow, "efforts": r.Model.Levels(), "hasKey": r.APIKey != "" || len(r.Provider.Env) == 0,
			"images": r.Model.Images(),
		})
	}
	return map[string]any{"models": out}, nil
}

// newThread wires an agent, its hooks and a session file for cwd.
// modelFrom and effortFrom say where model and effort came from.
func (s *Server) newThread(cwd string, model config.ModelRef, models config.ModelsFile, effort string, file *session.Writer, release func(), start time.Time, modelFrom, effortFrom core.Origin) (*thread, error) {
	ag, hk, src, err := core.NewAgentSources(cwd, model, effort)
	if err != nil {
		return nil, err
	}
	t := &thread{id: file.ID, cwd: cwd, models: models, agent: ag, sess: file, release: release, hooks: hk, hookSrc: src, blocks: blocks{}}
	// Extensions show what they show to the clients (see threadHost);
	// notices go to them as extension/notify, dialogs get their default
	// answers, and sendMessage steers the thread's turn.
	t.ext = core.LoadExtensions(ag, &threadHost{s: s, t: t, Headless: &extensions.Headless{Send: ag.Steer, OnNotify: func(ext, text, level string) {
		s.notify(t, "extension/notify", map[string]any{"extension": ext, "message": text, "level": level})
	}}})
	t.mcp = core.LoadMCP(ag)
	ag.NoGoals = true
	core.Bind(ag, hk, file, start, true)
	t.loaded = core.Collect(ag, src, modelFrom, effortFrom)
	t.tr.IDPrefix = itemPrefix(t.id)
	s.mu.Lock()
	s.threads[t.id] = t
	s.mu.Unlock()
	return t, nil
}

func (s *Server) startThread(p threadParams) (any, error) {
	settings, models, err := core.Load()
	if err != nil {
		return nil, err
	}
	model, modelFrom, err := core.PickModelFrom(models, settings, p.Model, "")
	if err != nil {
		return nil, invalid("%v", err)
	}
	effort, effortFrom := core.EffortFrom(settings, p.Effort, "")
	cwd := p.Cwd
	if cwd == "" {
		cwd = s.Cwd
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return nil, invalid("cwd %q is not a directory", cwd)
	}
	file := session.New(cwd)
	release, err := session.LockKind(file.Path, session.KindServer)
	if err != nil {
		return nil, err
	}
	t, err := s.newThread(cwd, model, models, effort, file, release, time.Now(), modelFrom, effortFrom)
	if err != nil {
		file.Close()
		release()
		return nil, err
	}
	s.sessionStart(t, "startup")
	info := s.snapshot(t)
	t.mu.Lock()
	loaded := t.loaded
	t.mu.Unlock()
	info.Context = &loaded
	return info, nil
}

func (s *Server) sessionStart(t *thread, source string) {
	if t.ext != nil {
		t.ext.SessionStart(source)
	}
	if t.hooks == nil {
		return
	}
	go func() {
		for _, n := range t.hooks.SessionStart(context.Background(), source) {
			s.notify(t, "hook", map[string]any{"event": "SessionStart", "message": n})
		}
	}()
}

func (s *Server) resumeThread(id string) (any, error) {
	s.mu.Lock()
	existing := s.threads[id]
	s.mu.Unlock()
	if existing != nil { // already loaded: same as read
		return s.snapshot(existing), nil
	}
	path, err := session.Find(id)
	if err != nil {
		return nil, invalid("%v", err)
	}
	release, err := session.LockKind(path, session.KindServer)
	if err != nil {
		return nil, invalid("%v", err)
	}
	keep := false
	defer func() {
		if !keep {
			release()
		}
	}()
	saved, file, err := core.Open(path)
	if err != nil {
		return nil, err
	}
	settings, models, err := core.Load()
	if err != nil {
		file.Close()
		return nil, err
	}
	model, modelFrom, err := core.PickModelFrom(models, settings, "", saved.Model)
	if err != nil {
		file.Close()
		return nil, err
	}
	effort, effortFrom := core.EffortFrom(settings, "", saved.Effort)
	t, err := s.newThread(saved.Header.Cwd, model, models, effort, file, release, saved.Header.Time, modelFrom, effortFrom)
	if err != nil {
		file.Close()
		return nil, err
	}
	keep = true
	t.name = saved.Name
	t.restore(saved.Entries)
	t.agent.SetLongContext(saved.LongContext)
	s.sessionStart(t, "resume")
	info := s.snapshot(t)
	t.mu.Lock()
	loaded := t.loaded
	t.mu.Unlock()
	info.Context = &loaded
	return info, nil
}

// snapshot describes a thread with its items, those still in progress
// included as they stand, and the event to follow them from: no item
// notification of the thread goes out while it is taken.
func (s *Server) snapshot(t *thread) ThreadInfo {
	t.feed.Lock()
	defer t.feed.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	info := t.info()
	info.Items = make([]Item, 0, len(t.items))
	for _, it := range t.items {
		info.Items = append(info.Items, t.withDisplay(it))
	}
	if t.busy {
		for _, it := range t.tr.Open() {
			info.Items = append(info.Items, t.withDisplay(wireItem(t.id, &it)))
		}
	}
	if !t.ui.Empty() {
		info.ExtensionUI = WireExtensionUI(&t.ui)
	}
	info.EventID = s.eventSeq()
	return info
}

func (s *Server) listThreads(p threadParams) (any, error) {
	list, err := session.List(p.Cwd, p.Archived)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, x := range list {
		s.mu.Lock()
		_, loaded := s.threads[x.ID]
		s.mu.Unlock()
		out = append(out, map[string]any{
			"threadId": x.ID, "name": x.Name, "preview": x.Preview, "cwd": x.Cwd,
			"updatedAt": x.Updated, "messages": x.Messages, "loaded": loaded,
		})
	}
	return map[string]any{"threads": out}, nil
}

func (s *Server) setModel(p threadParams) (any, error) {
	t, err := s.thread(p.ThreadID)
	if err != nil {
		return nil, err
	}
	settings, models, err := core.Load()
	if err != nil {
		return nil, err
	}
	model, err := core.PickModel(models, settings, p.Model)
	if err != nil {
		return nil, invalid("%v", err)
	}
	t.agent.SetModel(model)
	t.sess.Append(session.Entry{Type: session.TypeModel, Provider: model.ProviderName, Model: model.Model.ID})
	t.mu.Lock()
	defer t.mu.Unlock()
	t.models = models
	return t.info(), nil
}

func (s *Server) setEffort(p threadParams) (any, error) {
	t, err := s.thread(p.ThreadID)
	if err != nil {
		return nil, err
	}
	m, _ := t.agent.Current()
	if err := core.CheckEffort(m, p.Effort); err != nil {
		return nil, invalid("%v", err)
	}
	t.agent.SetEffort(p.Effort)
	t.sess.Append(session.Entry{Type: session.TypeEffort, Effort: p.Effort})
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.info(), nil
}

// --- turns ---

func (s *Server) notify(t *thread, method string, params map[string]any) {
	params["threadId"] = t.id
	s.Notify(method, params)
}

// begin marks the thread busy and starts fn in the background.
func (s *Server) begin(t *thread, fn func(ctx context.Context, emit func(any)) error) (string, error) {
	t.mu.Lock()
	if t.busy || t.closing {
		t.mu.Unlock()
		return "", &rpcError{codeServer, "a turn is already running; use turn/steer or turn/interrupt"}
	}
	if s.memory != nil {
		s.memory.Begin()
	}
	t.done = make(chan struct{})
	done := t.done
	t.turnSeq++
	turnID := fmt.Sprintf("%s-t%d", t.id, t.turnSeq)
	ctx, cancel := context.WithCancel(context.Background())
	t.busy, t.turnID, t.cancel, t.usage = true, turnID, cancel, provider.Usage{}
	t.turn = TurnInfo{StartedAt: time.Now().UnixMilli()}
	started := t.turn.StartedAt
	t.mu.Unlock()

	s.notify(t, "turn/started", map[string]any{"turnId": turnID, "startedAt": started})
	m := &itemMapper{s: s, t: t, turnID: turnID}
	t.feed.Lock()
	t.tr.Handler = m.handler()
	t.feed.Unlock()
	go func() {
		defer close(done)
		if s.memory != nil {
			defer s.memory.End()
		}
		err := fn(ctx, m.event)
		m.closeOpen()
		// A reload no step boundary reached runs now, while the thread is
		// still busy; its report for the model waits in the inbox.
		for _, f := range t.agent.TakeBoundary() {
			if text := f(); text != "" {
				_ = events.Push(t.id, events.Event{Source: sourceReloaded, Title: "reload applied", Text: strings.TrimPrefix(text, events.Prefix)})
			}
		}
		t.mu.Lock()
		t.busy, t.cancel, t.turnID = false, nil, ""
		t.ctxTokens = t.agent.ContextTokens()
		usage, ctxTokens := t.usage, t.ctxTokens
		t.mu.Unlock()
		cancel()
		status, msg := "completed", ""
		switch {
		case errors.Is(err, context.Canceled):
			status = "interrupted"
		case err != nil:
			status, msg = "failed", err.Error()
		}
		params := map[string]any{"turnId": turnID, "status": status, "contextTokens": ctxTokens,
			"usage": map[string]int{"inputTokens": usage.PromptTokens, "cachedInputTokens": usage.CachedTokens, "outputTokens": usage.CompletionTokens}}
		if msg != "" {
			params["error"] = msg
		}
		s.notify(t, "turn/completed", params)
	}()
	return turnID, nil
}

func (s *Server) startTurn(p threadParams) (any, error) {
	t, err := s.thread(p.ThreadID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Input) == "" && len(p.Images) == 0 {
		return nil, invalid("input is required")
	}
	m, _ := t.agent.Current()
	imgs, err := turnImages(p.Images, m)
	if err != nil {
		return nil, err
	}
	input := images.WithPlaceholders(p.Input, imgs)
	var turnID string
	turnID, err = s.begin(t, func(ctx context.Context, emit func(any)) error {
		emit(transcript.Input{Text: input, Images: imgs})
		return t.agent.RunWithImages(ctx, input, imgs, emit)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"turnId": turnID}, nil
}

func (s *Server) startCompact(id string) (any, error) {
	t, err := s.thread(id)
	if err != nil {
		return nil, err
	}
	turnID, err := s.begin(t, t.agent.Compact)
	if err != nil {
		return nil, err
	}
	return map[string]any{"turnId": turnID}, nil
}

// sourceReloaded is the source of the event that reports a reload.
const sourceReloaded = "reloaded"

// reload applies an `atto reload` the thread's agent asked for: between
// steps when a turn runs, else in a turn of its own that tells the model
// the result.
func (s *Server) reload(t *thread) {
	apply := func() string {
		t.mu.Lock()
		prev, path := t.loaded, t.sess.Path
		t.mu.Unlock()
		r, err := core.Reload(t.agent, t.id, path, prev)
		if err != nil {
			s.notify(t, "thread/reloaded", map[string]any{"error": err.Error()})
			return events.Format([]events.Event{{Text: "Reload failed, nothing changed: " + err.Error()}})
		}
		t.mu.Lock()
		t.loaded, t.hooks, t.hookSrc = r.Loaded, r.Hooks, r.HookSrc
		t.mu.Unlock()
		s.notify(t, "thread/reloaded", map[string]any{"context": r.Loaded, "changes": r.Changes, "promptChanged": r.PromptChanged})
		return events.Format([]events.Event{{Text: r.ForModel()}})
	}
	_, err := s.begin(t, func(ctx context.Context, emit func(any)) error {
		text := apply()
		emit(transcript.Input{Text: text})
		return t.agent.Run(ctx, text, emit)
	})
	if err != nil { // a turn is running: apply it at its next step boundary
		t.agent.AtBoundary(apply)
	}
}
