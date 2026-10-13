package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/trust"
)

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
	t.initUIJobs()
	t.initUIQueue()
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
