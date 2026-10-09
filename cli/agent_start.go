package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// agentStart creates an agent from preset and starts its first turn. The
// caller's session, if any, is the new agent's parent; from a plain shell
// there is none, and the agent is the root of a tree of its own.
//
// The order matters for crashes: the session ID is chosen first, a journal
// entry records what is about to be made (worktree, branch), and only the
// published record makes the agent addressable. A spawn that dies before
// that is rolled back by the next one (recoverSpawns).
func agentStart(out io.Writer, settings config.Settings, c caller, name, preset, task, model, effortOverride string, useWorktree bool) error {
	recoverSpawns(out)
	parent := c.session
	if c.implicit() {
		parent = ""
	}
	if parent != "" {
		release, err := agentstate.StartWork(parent)
		if err != nil {
			return err
		}
		defer release()
		if _, err := agentstate.LoadChild(parent, name); err == nil {
			return fmt.Errorf("an agent named %q exists: give it a follow-up with atto agent task %s, or pick another name", name, name)
		}
	}
	if task == "" {
		return fmt.Errorf("give the agent its task: atto agent start %s %s \"...\"", name, preset)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	presets, _ := agentstate.LoadPresets(agentstate.Dirs(cwd, agent.ProjectRoot(cwd)))
	p, err := agentstate.Find(presets, preset)
	if err != nil {
		return err
	}
	_, models, err := core.Load()
	if err != nil {
		return err
	}
	if model != "" {
		p.Model = model
	}
	if effortOverride != "" {
		p.Effort = effortOverride
	}
	pm, pe := "", ""
	if parent != "" {
		pm, pe = sessionModel(parent)
	}
	ref, effort, err := agentModel(models, settings, p, pm, pe)
	if err != nil {
		return err
	}

	// Where the new agent sits in its tree.
	id := session.NewID()
	meta := session.AgentMeta{Version: session.AgentMetaVersion, RootSessionID: id, Path: agentstate.RootPath, Name: name, Role: p.Name,
		SpawnCwd: cwd, Project: externalProject(cwd), Origin: session.OriginExternal}
	if parent != "" {
		meta.Origin = session.OriginAgent
		if c.outside {
			meta.Origin = session.OriginExternal
		}
		meta.ParentSessionID = &parent
		if ps, err := agentstate.Load(parent); err == nil {
			meta.RootSessionID, meta.Depth, meta.Path, meta.Project = ps.Root, ps.Depth+1, ps.Path+"/"+name, ps.Project
		} else {
			meta.RootSessionID, meta.Depth, meta.Path = parent, 1, agentstate.RootPath+"/"+name
		}
	}
	meta.SpawnedBy = spawnedBy(c, parent, cwd)

	journal, err := agentstate.BeginSpawn(agentstate.SpawnEntry{ID: id, Parent: parent, Name: name})
	if err != nil {
		return err
	}
	defer journal.Done()
	var wt worktree
	agentCwd := cwd
	if useWorktree {
		if wt, err = planWorktree(cwd, id); err != nil {
			return err
		}
		if err := journal.Update(agentstate.SpawnEntry{ID: id, Parent: parent, Name: name, Worktree: wt.Path, Branch: wt.Branch, Repo: wt.Repo}); err != nil {
			return err
		}
		if err := wt.create(); err != nil {
			return err
		}
		agentCwd = wt.Cwd
	}
	undo := func() {
		if useWorktree {
			wt.undo()
		}
	}
	w := session.NewManagedID(id, agentCwd, func(string) session.AgentMeta { return meta })
	// The session exists from the start, so every turn resumes it.
	w.Append(session.Entry{Type: session.TypeName, Name: name})
	w.Close()
	if err := w.Err(); err != nil {
		_ = os.Remove(w.Path)
		undo()
		return err
	}
	st := agentstate.State{
		Name: name, Parent: parent, Session: id, Preset: p.Name, Instructions: p.Instructions,
		Model: ref.String(), Effort: effort, Cwd: agentCwd, Task: task, Created: time.Now(),
		Worktree: wt.Path, Branch: wt.Branch, Base: wt.Base, Repo: wt.Repo,
		Root: meta.RootSessionID, Depth: meta.Depth, Path: meta.Path, Origin: meta.Origin, Project: meta.Project, SpawnCwd: cwd, SpawnedBy: meta.SpawnedBy,
	}
	if err := agentstate.Create(st); err != nil {
		_ = os.Remove(w.Path)
		undo()
		return err
	}
	if err := startTurnLocked(&st, task); err != nil {
		agentstate.Discard(id)
		_ = os.Remove(w.Path)
		undo()
		return err
	}
	address := name
	if c.outside {
		address = "@" + st.Session
	}
	label := agentLabel(st)
	jobText := fmt.Sprintf("job %d", st.Job)
	if st.IsRoot() {
		jobText = fmt.Sprintf("job %d of session %s", st.Job, st.Session)
	}
	fmt.Fprintf(out, "agent %s started (@%s, session %s, %s · %s, role %s, project %s, %s).\n", label, agentstate.ShortID(st.Session), st.Session, st.Model, orDash(effort), p.Name, orDash(st.Project), jobText)
	if useWorktree {
		fmt.Fprintf(out, "It works in worktree %s on branch %s (from %.7s); atto agent close %s removes the worktree and keeps the branch.\n", st.Worktree, st.Branch, st.Base, address)
	}
	if st.IsRoot() {
		fmt.Fprintf(out, "Nothing sends its answer anywhere: poll it with atto agent wait %s · atto agent report %s · new task: atto agent task %s \"...\" · message: atto agent send %s \"...\"\n", address, address, address, address)
	} else {
		fmt.Fprintf(out, "Its final answer reaches you when its turn ends. wait: atto agent wait %s · new task: atto agent task %s \"...\" · message: atto agent send %s \"...\"\n", address, address, address)
	}
	return nil
}

// spawnedBy records who runs the spawn: the model's session (read from the
// session's own record: its latest model and effort entries and its turn
// count, never its environment), the tool call the command ran in and where.
func spawnedBy(c caller, parent, cwd string) *session.SpawnedBy {
	by := &session.SpawnedBy{Origin: session.SpawnOutside, Cwd: cwd}
	self := ""
	if !c.outside {
		by.Origin = session.SpawnModel
		by.ToolCallID = os.Getenv(config.EnvToolCallID)
		self = os.Getenv("ATTO_SESSION_ID")
	}
	if self == "" {
		self = parent
		if parent != "" {
			by.Origin = session.SpawnExplicitSession
		}
	}
	if self == "" {
		return by
	}
	by.Session = &self
	by.Model, by.Effort = sessionModel(self)
	by.Turn = sessionTurn(self)
	return by
}

// sessionTurn is the number of the turn session id is in: for an agent its
// turn counter, for any other session how many user messages it holds.
func sessionTurn(id string) int {
	if st, err := agentstate.Load(id); err == nil {
		return st.Turns
	}
	path, err := session.Find(id)
	if err != nil {
		return 0
	}
	_, entries, err := session.Load(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range session.Active(entries) {
		if e.Type == session.TypeMessage && e.Message != nil && e.Message.Role == "user" {
			n++
		}
	}
	return n
}

// recoverSpawns rolls back spawns whose process died before publishing their
// record: the worktree and branch they made, and their session file.
func recoverSpawns(out io.Writer) {
	for _, e := range agentstate.StaleSpawns() {
		if !agentstate.Published(e.ID) {
			if e.Worktree != "" && e.Repo != "" {
				worktree{Repo: e.Repo, Path: e.Worktree, Branch: e.Branch}.undo()
			}
			if p, err := session.Find(e.ID); err == nil {
				_ = os.Remove(p)
			}
			fmt.Fprintf(out, "rolled back an unfinished spawn of agent %s (@%s)\n", e.Name, e.ID)
		}
		agentstate.ClearSpawn(e.ID)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// startTurn starts the next turn of st with message text, and saves st.
func startTurn(st *agentstate.State, text string) error {
	release, err := agentstate.StartWork(st.Session)
	if err != nil {
		return err
	}
	defer release()
	return startTurnLocked(st, text)
}

// startTurnLocked is startTurn with the tree held. The turn is a job of the
// parent session; a root, which has none, owns the job of its own turn. The
// job names the agent and the turn, not where the record is.
func startTurnLocked(st *agentstate.State, text string) error {
	cur, err := agentstate.Load(st.Session)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cur.Turns++
	cur.Prompt, cur.Job = text, 0
	cur.JobOwner = cur.Parent
	if cur.IsRoot() {
		cur.JobOwner = cur.Session
	}
	if err := agentstate.Save(cur); err != nil {
		return err
	}
	*st = cur
	args := []string{exe, "_agent-turn", cur.Session, fmt.Sprint(cur.Turns)}
	j, err := jobs.StartAgentArgs(cur.JobOwner, cur.Cwd, "agent "+cur.Name, args, cur.IsRoot())
	if err != nil {
		return fmt.Errorf("starting agent %s: %w", cur.Name, err)
	}
	st.Job = j.ID
	return agentstate.Save(*st)
}

// sessionModel is the model (provider/id) and effort session id last
// used, "" when it can't be read.
func sessionModel(id string) (model, effort string) {
	path, err := session.Find(id)
	if err != nil {
		return "", ""
	}
	saved, err := core.Read(path)
	if err != nil {
		return "", ""
	}
	model, effort = saved.Model, saved.Effort
	return model, effort
}

// agentModel picks an agent's model and effort: its role's, else
// settings.json's agents.model and .effort, else what the parent
// session uses. A model or effort named in a preset or the settings must
// exist; one inherited from the parent that the model doesn't offer gives
// way to the default.
func agentModel(models config.ModelsFile, settings config.Settings, p agentstate.Preset, parentModel, parentEffort string) (config.ModelRef, string, error) {
	cfgModel, cfgEffort := settings.AgentDefaults()
	model, from := p.Model, "role "+p.Name
	if model == "" && cfgModel != "" {
		model, from = cfgModel, "settings agents.model"
	}
	ref, _, err := core.PickModelFrom(models, settings, model, parentModel)
	if err != nil {
		if model != "" {
			return ref, "", fmt.Errorf("%s: %w", from, err)
		}
		return ref, "", err
	}
	effort, from := p.Effort, "role "+p.Name
	if effort == "" && cfgEffort != "" {
		effort, from = cfgEffort, "settings agents.effort"
	}
	if effort != "" {
		if err := core.CheckEffort(ref, effort); err != nil {
			return ref, "", fmt.Errorf("%s: %w", from, err)
		}
		return ref, effort, nil
	}
	cands := []string{parentEffort, settings.DefaultEffort, core.DefaultEffort}
	if lv := ref.Model.Levels(); len(lv) > 0 {
		cands = append(cands, lv[0])
	}
	for _, e := range cands {
		if e != "" && core.CheckEffort(ref, e) == nil {
			return ref, e, nil
		}
	}
	return ref, "", nil
}

// workerOf is the description of an agent's session for its system prompt.
func workerOf(st agentstate.State) *agent.Worker {
	w := &agent.Worker{Name: st.Name, Preset: st.Preset, Instructions: st.Instructions, ID: st.Session, Path: st.Path, Worktree: st.Worktree, Branch: st.Branch}
	if st.Parent != "" {
		w.Parent, w.ParentID = agentstate.PathOf(st.Parent), st.Parent
	}
	return w
}

// RunAgentTurn is the hidden entry point of an agent's turn (atto
// _agent-turn <agent session ID> <turn>), started by atto agent as a job:
// of the parent session, or of the agent's own for an agent started from a
// shell. There is no slot to wait for. It runs the turn as atto -p resuming
// the agent's session, records how it went and tells the parent, if any. It
// exits 0 once it has told the parent, however the turn went.
func RunAgentTurn(args []string, _ io.Writer) error {
	fs := newFlags("_agent-turn")
	if err := fs.Parse(args); err != nil || fs.NArg() != 2 {
		return fmt.Errorf("usage: atto _agent-turn <agent session ID> <turn> (started by atto agent)")
	}
	id := fs.Arg(0)
	st, err := agentstate.Load(id)
	if err != nil {
		return err
	}
	if fmt.Sprint(st.Turns) != fs.Arg(1) {
		return fmt.Errorf("agent %s is at turn %d, not %s", st.Name, st.Turns, fs.Arg(1))
	}
	t := agentstate.Turn{N: st.Turns, Status: agentstate.Queued, Queued: time.Now()}
	_ = agentstate.SaveTurn(id, t)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancelCause(sigCtx)
	defer cancel(nil)
	go watchAgentInterrupt(ctx, st, cancel)
	t.Status, t.Started = agentstate.Running, time.Now()
	_ = agentstate.SaveTurn(id, t)

	var res printResult
	runErr := RunPrint(PrintOptions{
		Prompt: st.Prompt, Model: st.Model, Effort: st.Effort, Resume: st.Session, Format: "text", Verbose: true,
		Worker: workerOf(st),
		done:   func(r printResult) { res = r }, turnContext: ctx,
	})
	return finishAgentTurn(ctx, st, t, res, runErr, startTurn)
}

// finishAgentTurn records how a turn went, tells the parent, and starts the
// successor when waking work arrived as the turn ended. It is the end of a
// turn however it ran (RunAgentTurn here, a session worker's driver).
func finishAgentTurn(ctx context.Context, st agentstate.State, t agentstate.Turn, res printResult, runErr error, start func(*agentstate.State, string) error) error {
	t.Ended = time.Now()
	t.PromptTokens, t.CachedTokens, t.OutputTokens = res.Usage.InputTokens, res.Usage.CachedInputTokens, res.Usage.OutputTokens
	t.Cost, t.Steps = res.cost, res.NumSteps
	t.Status = agentstate.Done
	switch {
	case errors.Is(context.Cause(ctx), agent.ErrUserInterrupt):
		t.Status = agentstate.Stopped
	case res.Error != "":
		t.Status, t.Error = agentstate.Failed, res.Error
	case runErr != nil && !errors.Is(runErr, ErrPrintFailed):
		t.Status, t.Error = agentstate.Failed, runErr.Error()
	}
	releaseTurn, err := agentstate.LockTurn(st.Session)
	if err != nil {
		return err
	}
	defer releaseTurn()
	// The answer first: a wait that sees the turn over takes it from the
	// inbox, so it is not delivered twice. Only a recorded parent gets one.
	if st.Parent != "" {
		if err := events.Push(st.Parent, turnEvent(st, t)); err != nil {
			return err
		}
	}
	if err := agentstate.SaveTurn(st.Session, t); err != nil {
		return err
	}
	if t.Status == agentstate.Stopped {
		return nil
	}
	// Tasks accepted after the final poll need a successor, not an idle inbox.
	_, evs := events.SplitReload(core.Poll(st.Session))
	if !events.Wakes(evs) {
		events.Requeue(st.Session, evs)
		return nil
	}
	if err := start(&st, events.Format(evs)); err != nil {
		events.Requeue(st.Session, evs)
		return err
	}
	return nil
}

// watchAgentInterrupt handles the portable, per-turn control request while
// a worker waits for a model response or a running shell command.
func watchAgentInterrupt(ctx context.Context, st agentstate.State, cancel context.CancelCauseFunc) {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if agentstate.Interrupted(st.Session, st.Turns) {
			cancel(agent.ErrUserInterrupt)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// finalAnswerMax caps the answer a turn's end delivers; the rest is in
// atto agent report.
const finalAnswerMax = 8000

// turnEvent tells the parent session that an agent's turn ended, with its
// final answer, as codex delivers FINAL_ANSWER.
func turnEvent(st agentstate.State, t agentstate.Turn) events.Event {
	from, to := st.Path, agentstate.PathOf(st.Parent)
	what := "finished"
	if t.Status == agentstate.Failed {
		what = "failed"
	} else if t.Status == agentstate.Stopped {
		what = "stopped"
	}
	detail := tui.FormatDuration(t.Duration())
	if n := t.PromptTokens + t.OutputTokens; n > 0 {
		detail += ", " + tui.FormatTokens(n) + " tokens"
	}
	body := fmt.Sprintf("Turn %d %s (%s).", t.N, what, detail)
	if t.Error != "" {
		body += " Error: " + t.Error
	}
	if msg := session.LastAssistant(st.Session); msg != "" {
		if len(msg) > finalAnswerMax {
			msg = msg[:finalAnswerMax] + fmt.Sprintf("\n[cut: atto agent report @%s has all of it]", st.Session)
		}
		body += "\n\n" + msg
	}
	return events.Event{Source: "agent", Text: agentstate.Envelope(agentstate.FinalAnswer, from, to, body),
		Title: fmt.Sprintf("◆ agent %s %s after %s", from, what, tui.FormatDuration(t.Duration()))}
}
