package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/core"
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

// notify publishes a notification of thread t.
func (s *Server) notify(t *thread, method string, params map[string]any) { t.publish(method, params) }

// eventSeq is the cursor shared by every transport.
func (s *Server) eventSeq() int64 {
	if s.events == nil {
		return 0
	}
	return s.events.last()
}
