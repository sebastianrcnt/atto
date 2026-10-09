package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/ui"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/trust"
)

// Server holds threads (session runtimes, see thread) and dispatches
// JSON-RPC requests. Notifications go through its event hub to every
// transport (see hub.go); Notify, when set, observes them too (tests).
type Server struct {
	Workers *WorkerRoutes
	routing workerRouting
	Version string
	Cwd     string // default working directory for new threads
	Notify  func(method string, params map[string]any)

	memory core.IdleMemory

	// LockKind is the writer lease threads take (session.KindServer by
	// default; the terminal's own runtime takes session.KindTUI).
	LockKind string
	// AgentTurns lets this server run the turns of agents (atto agent task,
	// spawn...) for the agent sessions it holds, as a session worker does.
	AgentTurns bool
	// Retire closes a thread no client is attached to once it has been
	// idle for Retention: no run, queued input, active goal, running job,
	// pending timer or open prompt. Retention is 0 in-process; workers
	// should use DefaultSessionRetention.
	// Off, threads live until the server closes.
	Retire    bool
	Retention time.Duration
	// OnThreadClosed, when set, hears that a thread closed (the worker of
	// a daemon exits with its session).
	OnThreadClosed func(id string)

	opening  sync.Mutex // serialize session opens with shutdown
	mu       sync.Mutex
	threads  map[string]*thread
	stop     chan struct{}
	instance string // see newInstanceID
	events   *broker
	clients  map[string]*clientConn
}

// DefaultSessionRetention is the unattended idle grace period for workers.
const DefaultSessionRetention = time.Minute

func New(version, cwd string) *Server {
	core.ConfigureMemoryBudget()
	s := &Server{Version: version, Cwd: cwd, threads: map[string]*thread{}, stop: make(chan struct{}), instance: newInstanceID(), events: newBroker()}
	s.memory = core.NewIdleMemory()
	go s.watchInbox()
	return s
}

// Close ends every thread: runs are interrupted, SessionEnd hooks and
// extensions run, background jobs stop and session files close (Windows
// cannot delete or move a file that is still open).
func (s *Server) Close() { s.CloseWith("other") }

// CloseWith is Close with the reason SessionEnd hooks are given.
func (s *Server) CloseWith(reason string) {
	if s.Workers != nil {
		s.detachRoutes("", true)
	}
	s.opening.Lock()
	if s.memory != nil {
		s.memory.Close()
	}
	s.mu.Lock()
	select {
	case <-s.stop:
		s.mu.Unlock()
		s.opening.Unlock()
		return
	default:
		close(s.stop)
	}
	threads := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		threads = append(threads, t)
	}
	s.mu.Unlock()
	s.opening.Unlock()
	var wg sync.WaitGroup
	for _, t := range threads {
		wg.Go(func() { s.closeThread(t, closeMode{reason: reason}) })
	}
	wg.Wait()
}

// --- dispatch ---

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

func (s *Server) thread(id string) (*thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.threads[id]
	if t == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown thread %q (thread/start or thread/resume first)", id), Data: &ErrorData{Reason: ReasonNotFound}}
	}
	return t, nil
}

// Loaded reports whether thread id is loaded.
func (s *Server) Loaded(id string) bool {
	_, err := s.thread(id)
	return err == nil
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

// threadOptions build a thread.
type threadOptions struct {
	cwd                   string
	model                 config.ModelRef
	models                config.ModelsFile
	effort                string
	file                  *session.Writer
	release               func()
	start                 time.Time
	modelFrom, effortFrom core.Origin
	// worker, for the session of an agent this server runs turns of, is how
	// its system prompt describes it.
	worker *agent.Worker
}

// newThread wires an agent, its hooks, extensions and MCP and a session
// file for cwd, and starts the thread's lane.
func (s *Server) newThread(o threadOptions) (*thread, error) {
	trust.WarnProject(os.Stderr, o.cwd)
	ag, hk, src, err := core.NewAgentSources(o.cwd, o.model, o.effort)
	if err != nil {
		return nil, err
	}
	t := &thread{s: s, id: o.file.ID, cwd: o.cwd, models: o.models, agent: ag, sess: o.file, release: o.release,
		hooks: hk, hookSrc: src, blocks: blocks{}, modelFrom: o.modelFrom, effortFrom: o.effortFrom,
		itemMeta: map[string]userMeta{}, gates: map[string]int{}, attached: map[string]bool{}, lastActive: time.Now()}
	t.laneWake = make(chan struct{}, 1)
	t.done = make(chan struct{})
	t.tr.IDPrefix = itemPrefix(t.id)
	t.tr.MaxItems = DefaultItemLimit
	t.tr.Handler = t.handler()
	t.resetGoal()
	ag.SteerNote = t.goal.SteerNote // a message sent while the goal runs says so
	t.ext = core.LoadExtensions(ag, threadHost{t})
	t.mcp = core.LoadMCP(ag)
	if o.worker != nil {
		ag.Worker = o.worker
		t.mgd = newManagedAgent(t)
	}
	core.Bind(ag, hk, o.file, o.start, true)
	if o.worker != nil { // its commands know they are an agent's, as an agent turn's always did
		ag.SetSession(o.file.ID, append(core.Env(o.file.ID), config.EnvLegacyAgent+"=1"))
	}
	t.loaded = core.Collect(ag, src, o.modelFrom, o.effortFrom)
	t.catalogVer = t.catalogVersion()
	t.initUIStatus()
	t.initUIGoal()
	t.startLane()
	s.mu.Lock()
	s.threads[t.id] = t
	s.mu.Unlock()
	return t, nil
}

// pickModel chooses the model and effort for a thread: given, saved in
// the session, or the defaults. Without any model configured the thread
// starts anyway (first run: /login still works).
func pickModel(flagModel, flagEffort, savedModel, savedEffort string) (config.ModelRef, config.ModelsFile, string, core.Origin, core.Origin, error) {
	settings, models, err := core.Load()
	if err != nil {
		return config.ModelRef{}, models, "", "", "", err
	}
	model, modelFrom, err := core.PickModelFrom(models, settings, flagModel, savedModel)
	if err != nil && !errors.Is(err, core.ErrNoModels) {
		return model, models, "", "", "", invalid("%v", err)
	}
	effort, effortFrom := core.EffortFrom(settings, flagEffort, savedEffort)
	return model, models, effort, modelFrom, effortFrom, nil
}

func (s *Server) lockKind() string {
	if s.LockKind != "" {
		return s.LockKind
	}
	return session.KindServer
}

// startThread is thread/start: a new session for client.
func (s *Server) startThread(client string, p threadParams) (any, error) {
	s.opening.Lock()
	defer s.opening.Unlock()
	select {
	case <-s.stop:
		return nil, errThreadClosed
	default:
	}
	model, models, effort, modelFrom, effortFrom, err := pickModel(p.Model, p.Effort, "", "")
	if err != nil {
		return nil, err
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd = s.Cwd
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return nil, invalid("cwd %q is not a directory", cwd)
	}
	file := session.New(cwd)
	release, err := session.LockKind(file.Path, s.lockKind())
	if err != nil {
		return nil, err
	}
	t, err := s.newThread(threadOptions{cwd: cwd, model: model, models: models, effort: effort, file: file, release: release,
		start: time.Now(), modelFrom: modelFrom, effortFrom: effortFrom})
	if err != nil {
		file.Close()
		release()
		return nil, err
	}
	var info ThreadInfo
	err = t.call(func() error {
		t.attachClient(client)
		if model.Model.ID == "" {
			t.notice("", "%s", core.NoModelsHint())
		}
		t.showLoaded(false, nil, "")
		if p.DeferStart {
			t.startSource = "startup"
		} else {
			t.sessionStart("startup")
			t.askMCPApprovals()
		}
		info = t.snapshot()
		loaded := t.loaded
		info.Context = &loaded
		return nil
	})
	return info, err
}

// sessionStart runs SessionStart hooks (in the background) and tells the
// extensions.
func (t *thread) sessionStart(source string) {
	if t.ext != nil {
		t.ext.SessionStart(source)
	}
	hk := t.hooks
	if hk == nil {
		return
	}
	go func() {
		notices := hk.SessionStart(context.Background(), source)
		t.do(func() {
			for _, n := range notices {
				t.notice("", "%s", n)
				t.publish("hook", map[string]any{"event": "SessionStart", "message": n})
			}
		})
	}()
}

// showLoaded adds the "Loaded" item: what the session loaded, or after a
// reload what changed.
func (t *thread) showLoaded(reloaded bool, changes []core.Change, note string) {
	l := t.loaded
	text := strings.Join(append([]string{"Loaded"}, core.FormatRows(l.Summary(), "  ", 12)...), "\n")
	t.addNotice(Item{Level: "loaded", Text: text, Loaded: &l, Reloaded: reloaded, Changes: changes, Note: note})
	if notice := config.PriceTierNotice(t.model()); notice != "" {
		t.notice("", "%s", notice)
	}
}

// resumeThread is thread/resume: the thread when loaded (the client
// attaches to it), else the session opened from disk. A session another
// process writes (a background run) is read without loading it,
// read-only.
func (s *Server) resumeThread(client string, p threadParams) (any, error) {
	s.opening.Lock()
	defer s.opening.Unlock()
	select {
	case <-s.stop:
		return nil, errThreadClosed
	default:
	}
	s.mu.Lock()
	existing := s.threads[p.ThreadID]
	s.mu.Unlock()
	if existing != nil {
		return existing.attach(client)
	}
	path, err := session.Find(p.ThreadID)
	if err != nil {
		return nil, invalid("%v", err)
	}
	path, err = session.RestoreForWrite(path)
	if err != nil {
		return nil, err
	}
	release, err := session.LockKind(path, s.lockKind())
	if err != nil {
		if s.lockKind() == session.KindTUI {
			if lock, ok := session.LockedBy(path); ok && lock.Kind != session.KindTUI {
				info, readErr := readOffline(p.ThreadID)
				if readErr == nil {
					info.ReadOnly = session.ReadOnlyMessage(lock)
				}
				return info, readErr
			}
		}
		return nil, &rpcError{Code: codeServer, Message: err.Error(), Data: &ErrorData{Reason: ReasonOwnedElsewhere}}
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
	// The session of an agent run by atto agent: its model and effort are the
	// agent's, and its system prompt says what it is.
	var worker *agent.Worker
	savedModel, savedEffort := saved.Model, saved.Effort
	if s.AgentTurns && saved.Header.Agent != nil {
		if rec, err := agentstate.Load(saved.Header.ID); err == nil {
			worker = agent.WorkerOf(rec)
			if savedModel == "" {
				savedModel = rec.Model
			}
			if savedEffort == "" {
				savedEffort = rec.Effort
			}
		}
	}
	model, models, effort, modelFrom, effortFrom, err := pickModel("", "", savedModel, savedEffort)
	if err != nil {
		file.Close()
		return nil, err
	}
	// The session runs where it was started, unless the client says
	// otherwise (the terminal keeps its own directory, as it always did).
	cwd := saved.Header.Cwd
	if p.Cwd != "" {
		cwd = p.Cwd
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		cwd = s.Cwd
	}
	t, err := s.newThread(threadOptions{cwd: cwd, model: model, models: models, effort: effort, file: file, release: release,
		start: saved.Header.Time, modelFrom: modelFrom, effortFrom: effortFrom, worker: worker})
	if err != nil {
		file.Close()
		return nil, err
	}
	keep = true
	var info ThreadInfo
	err = t.call(func() error {
		t.attachClient(client)
		t.name = saved.Name
		branch := session.Active(saved.Entries)
		t.agent.Restore(session.Context(branch))
		t.agent.SetLongContext(saved.LongContext)
		t.ctx = t.agent.ContextTokens()
		t.total.Add(saved.Usage)
		last := saved.LastUsage
		t.total.Last = &last
		if ref, ok := models.Find("", saved.UsageModel); ok {
			t.total.LastCost = ref.Model.Cost
		}
		t.total.LastInputTokens, t.total.LastCachedInputTokens = saved.LastUsage.PromptTokens, saved.LastUsage.CachedTokens
		if m := t.model(); saved.Model != "" && m.ProviderName+"/"+m.Model.ID == saved.Model {
			t.recModel = saved.Model
		}
		if saved.Effort != "" {
			t.recEffort = saved.Effort
		}
		if p.DeferStart {
			t.startSource = "resume"
		} else {
			t.sessionStart("resume")
		}
		t.showLoaded(false, nil, "")
		kept := t.items
		if err := t.replayDisk(); err != nil {
			return err
		}
		t.items = append(kept, t.items...)
		t.resetItemOrder()
		t.restoreGoal(saved.Snapshots())
		if h := saved.Header; h.Cwd != t.cwd {
			t.notice("", "Resumed a session from %s; commands run in %s.", core.ShortPath(h.Cwd), core.ShortPath(t.cwd))
		}
		label := saved.Header.Time.Local().Format("2006-01-02 15:04")
		if t.name != "" {
			label = fmt.Sprintf("%q (%s)", t.name, label)
		}
		t.notice("", "Resumed session %s.", label)
		if !p.DeferStart {
			t.askMCPApprovals()
		}
		info = t.snapshot()
		loaded := t.loaded
		info.Context = &loaded
		return nil
	})
	return info, err
}

// ReadOffline reads a saved thread without taking its writer lease.
func ReadOffline(id string) (ThreadInfo, error) { return readOffline(id) }

// readOffline reads a saved session without loading it: its items, read-only.
func readOffline(id string) (ThreadInfo, error) {
	path, err := session.Find(id)
	if err != nil {
		return ThreadInfo{}, &rpcError{Code: codeInvalidParams, Message: err.Error(), Data: &ErrorData{Reason: ReasonNotFound}}
	}
	loaded, err := session.ReadContext(path)
	saved := core.Saved{Header: loaded.Header, Entries: loaded.Entries, State: loaded.State}
	for _, e := range loaded.State {
		switch e.Type {
		case session.TypeName:
			saved.Name = e.Name
		case session.TypeContext:
			saved.LongContext = e.LongContext
		case session.TypeModel:
			saved.Model = e.Provider + "/" + e.Model
		case session.TypeEffort:
			saved.Effort = e.Effort
		}
	}
	if err != nil {
		return ThreadInfo{}, err
	}
	info := ThreadInfo{ID: saved.Header.ID, Cwd: saved.Header.Cwd, Name: saved.Name, Model: saved.Model, Effort: saved.Effort, Offline: true, SessionPath: path, LongContext: saved.LongContext}
	b := transcript.Builder{IDPrefix: itemPrefix(info.ID)}
	page, _, pageErr := replayFile(&b, info.ID, path, "", DefaultItemLimit)
	if pageErr != nil {
		return ThreadInfo{}, pageErr
	}
	info.Items, info.HasMore, info.Before = page.Items, page.HasMore, page.Before
	var u Usage
	u.Add(loaded.Usage)
	last := loaded.LastUsage
	u.Last = &last
	if settings, models, err := core.Load(); err == nil {
		if model, _, err := core.PickModelFrom(models, settings, "", saved.Model); err == nil {
			SetModel(&info, model, models)
		}
		if model, ok := models.Find("", loaded.UsageModel); ok {
			u.LastCost = model.Model.Cost
		}
		if info.Effort == "" {
			info.Effort = core.Effort(settings, "")
		}
	}
	u.LastInputTokens, u.LastCachedInputTokens = loaded.LastUsage.PromptTokens, loaded.LastUsage.CachedTokens
	info.Usage = &u
	return info, nil
}

// --- attachment and lifetime ---

// attach is thread/attach: client follows the thread from its snapshot.
func (t *thread) attach(client string) (ThreadInfo, error) {
	var info ThreadInfo
	err := t.call(func() error {
		t.attachClient(client)
		if len(t.items) == 0 {
			if err := t.loadDisplay(); err != nil {
				return err
			}
		}
		info = t.snapshot()
		loaded := t.loaded
		info.Context = &loaded
		return nil
	})
	return info, err
}

func (t *thread) attachClient(client string) {
	if client == "" {
		return
	}
	t.attached[client] = true
	t.lastActive = time.Now()
	t.cancelRetire()
}

// detachResult is thread/detach's and thread/close's result.
type detachResult struct {
	Closed      bool     `json:"closed"`
	StoppedJobs int      `json:"stoppedJobs,omitempty"`
	Notices     []string `json:"notices,omitempty"`
}

// detach is thread/detach: client stops following the thread, which goes
// on. With retention 0, a thread left idle closes now (reason: what the
// client did: clear, resume or exit).
func (s *Server) detach(t *thread, client, reason string) detachResult {
	now := false
	_ = t.call(func() error {
		delete(t.attached, client)
		if len(t.attached) == 0 {
			t.dropDisplay()
		}
		delete(t.gates, client)
		if reason != "" {
			t.leaveReason = reason
		}
		now = s.Retire && s.Retention == 0 && t.retirable()
		if !now {
			t.maybeRetire()
		}
		return nil
	})
	if !now {
		return detachResult{}
	}
	return s.closeThread(t, closeMode{reason: t.leaveReasonOr("other"), retire: true})
}

// clientGone releases what a detached client held: its gates and its
// attachments. Nothing it started stops.
func (s *Server) clientGone(id string) {
	if s.Workers != nil {
		s.detachRoutes(id, false)
	}
	s.mu.Lock()
	threads := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		threads = append(threads, t)
	}
	s.mu.Unlock()
	for _, t := range threads {
		t.do(func() {
			if !t.attached[id] && t.gates[id] == 0 {
				return
			}
			delete(t.attached, id)
			if len(t.attached) == 0 {
				t.dropDisplay()
			}
			delete(t.gates, id)
			t.withdrawClientPrompt(id, "")
			t.deliverEvents()
			t.maybeSendNextQueued()
			t.maybeRetire()
		})
	}
}

// retirable reports whether the thread has nothing going on and no
// client: it may close.
func (t *thread) retirable() bool {
	if t.mgd != nil && t.mgd.busy() {
		return false
	}
	if t.closing || t.login != nil || len(t.attached) > 0 || t.turns.Busy || t.shell != nil || len(t.turns.Queued) > 0 || len(t.turns.PendingEvents) > 0 || t.turns.SendNow != nil {
		return false
	}
	if t.goal.Active() && !t.goal.Held() {
		return false
	}
	if t.prompt != nil || len(t.prompts) > 0 || t.retryTimer != nil || t.jobCount > 0 || t.timerCount > 0 || jobs.ActiveCount(t.id) > 0 || len(events.Timers(t.id)) > 0 {
		return false
	}
	return true
}

// maybeRetire closes the thread once it has been retirable for the
// retention period.
func (t *thread) maybeRetire() {
	if !t.s.Retire || !t.retirable() {
		t.cancelRetire()
		return
	}
	if t.retireTimer != nil {
		return
	}
	var tm *time.Timer
	tm = time.AfterFunc(t.s.Retention, func() {
		t.do(func() {
			if t.retireTimer != tm {
				return
			}
			t.retireTimer = nil
			if t.retirable() {
				go t.s.closeThread(t, closeMode{reason: t.leaveReasonOr("other"), retire: true})
			}
		})
	})
	t.retireTimer = tm
}

func (t *thread) cancelRetire() {
	if t.retireTimer != nil {
		t.retireTimer.Stop()
		t.retireTimer = nil
	}
}

func (t *thread) leaveReasonOr(def string) string {
	if t.leaveReason != "" {
		return t.leaveReason
	}
	return def
}

// closeMode says how a thread closes.
type closeMode struct {
	reason  string // for SessionEnd
	retire  bool   // recheck idle/unattended status on the lane
	handoff bool   // a background run took the session over: end nothing
}

var closeHandoff = closeMode{handoff: true}

// closeThread ends thread t: its run is interrupted and awaited, its
// prompt closed, SessionEnd hooks and extensions run, its background jobs
// stop (unless handed off), MCP servers end, and the session file and
// lease are let go. Clients are told (thread/closed). Safe to call twice.
func (s *Server) closeThread(t *thread, m closeMode) detachResult {
	var done, shellDone chan struct{}
	if t.call(func() error {
		if t.closing || m.retire && !t.retirable() {
			return errThreadClosed
		}
		t.closing = true
		if t.elements != nil {
			t.elements.Stop()
		}
		t.cancelRetire()
		t.cancelGoalRetry()
		t.cancelPrompt()
		if t.login != nil {
			t.login.cancel()
			t.login = nil
		}
		if t.shell != nil {
			shellDone = t.shell.done
		}
		t.dropShell()
		if t.turns.Cancel != nil {
			t.turns.Cancel(nil)
		}
		done = t.runDone
		return nil
	}) != nil {
		return detachResult{}
	}
	if done != nil {
		<-done
	}
	if shellDone != nil {
		<-shellDone
	}
	if t.mgd != nil {
		t.mgd.wait() // a turn being recorded finishes first
	}
	_ = t.call(func() error { return nil }) // the run's end reaches the lane first
	t.inboxOff.Store(true)
	var res detachResult
	if !m.handoff {
		if t.sess.ReadOnly() == "" && t.readOnly == "" {
			switch {
			case t.mgd != nil && m.retire:
				res.StoppedJobs = core.LeaveKeepingAgents(t.id) // idle: the agents it started go on
			case t.mgd != nil || t.sess.IsAgent():
				res.StoppedJobs = core.LeaveAgent(t.id)
			default:
				res.StoppedJobs = core.Leave(t.id)
			}
		}
		if t.hooks != nil {
			res.Notices = t.hooks.SessionEnd(context.Background(), m.reason)
		}
		if t.ext != nil {
			t.ext.SessionEnd(m.reason)
		}
	}
	if t.ext != nil {
		t.ext.Close()
	}
	if t.mcp != nil {
		_ = t.mcp.Close()
	}
	// Last: SessionEnd hooks and extensions may still write to the session.
	_ = t.call(func() error {
		t.sess.Close()
		if t.release != nil {
			t.release()
			t.release = nil
		}
		t.publish("thread/closed", map[string]any{"reason": m.reason, "handoff": m.handoff})
		return nil
	})
	t.stopLane()
	s.mu.Lock()
	if s.threads[t.id] == t {
		delete(s.threads, t.id)
	}
	s.mu.Unlock()
	res.Closed = true
	if s.OnThreadClosed != nil {
		s.OnThreadClosed(t.id)
	}
	return res
}

// notify publishes a notification of thread t.
func (s *Server) notify(t *thread, method string, params map[string]any) { t.publish(method, params) }

// eventSeq is the cursor shared by every transport.
func (s *Server) eventSeq() int64 {
	if s.events == nil {
		return 0
	}
	return s.events.last()
}
