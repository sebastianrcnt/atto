package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
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

// agentUsage includes the older settings spelling for compatibility with old installs.
const agentUsage = `Agents are off unless settings.json has "agents": {"enabled": true}
(the older "subagents" key works too); spawn/task/send fail with "agents are off".
Works from a plain shell: no running atto session or atto -p root is needed.
A model-run root is only needed when a model should orchestrate the agents.

usage:
  atto agent spawn NAME "<task>" [-role R] [-worktree]
                                       start an agent in the background; returns at once
  atto agent task AGENT "<text>"       give an agent a new task: a turn now if it is
                                       idle, else after its current step
  atto agent send AGENT "<text>"       pass a message without starting a turn: a running
                                       agent reads it after its current step, an idle one
                                       with its next turn
  atto agent wait [AGENT...] [-timeout 10m]
                                       block until one of them (default: any you started)
                                       finishes a turn, and print its report
  atto agent list                      the agents you started, and theirs
  atto agent report AGENT              its last answer, status, duration and tokens
  atto agent interrupt AGENT           stop its running turn (it stays, for new tasks)
  atto agent close AGENT... | close -done [-force]
                                       remove agents you are done with, and theirs
  atto agent roles                     the roles -role picks from

Agents are atto sessions other agents start, as in codex: equally capable,
with the same tools. Each has a path from the root of its tree: the
session that started the first ones is /root, its agent "tests" is
/root/tests. AGENT is a name you gave (tests), a path below you
(tests/lint), ".." for the agent that started you, or a full path (/root,
/root/tests). An agent sees only what it is sent. When its turn ends, its
final answer reaches the agent that started it by itself, wrapped in
<atto_internal_context source="agent"> with Message Type FINAL_ANSWER;
messages and tasks arrive the same way (MESSAGE, NEW_TASK). wait exits with
status 124 when -timeout passes first, and returns early when the user
sends a message. Close agents once you have their result and no more work
for them.

Agents work in this directory. With -worktree, an agent gets its own git
worktree (under the atto dir) on a new branch atto/<session>/NAME made from
HEAD, and commits its work there: use it when agents edit files in
parallel. close removes the worktree, refusing while it has uncommitted
changes unless -force, and keeps the branch for you to merge.

In the agents setting, "maxDepth" (default 1) is how deep agents may start
agents of their own, "maxConcurrent" (default 3) caps the turns each
session's agents run at once (more wait in a queue), "model" and "effort"
apply to agents whose role names none (else they use their parent's).

Every command accepts -session ID (default: $ATTO_SESSION_ID). Outside
atto, without -session or ATTO_SESSION_ID, a lightweight parent is created
without a model call and reused for this project (git root, else cwd);
the last close archives it.
spawn accepts -m provider/model and -effort LEVEL for external callers only;
in atto's model shell (ATTO_SESSION_ID / ATTO_AGENT set), use -role instead.
wait and report accept -json: one object; duration is in seconds.
Old names still work: start (spawn), next (task, idle), steer (task,
running), wait-any (wait), stop (interrupt), rm (close), presets (roles).`

// agentInterruptWait gives a worker time to record a graceful stop before
// an unresponsive process is force-stopped. Tests shorten it.
var agentInterruptWait = 10 * time.Second

// RunAgent implements "atto agent".
func RunAgent(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(out, agentUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	if to, ok := agentAliases[sub]; ok {
		sub = to
	}
	fs := newFlags("agent " + sub)
	session := sessionFlag(fs)
	timeout := fs.Duration("timeout", 0, "give up after this long")
	model, effort, role := "", "", ""
	useWorktree, force := false, false
	if sub == "spawn" {
		fs.StringVar(&model, "m", "", "model (external callers only)")
		fs.StringVar(&effort, "effort", "", "effort (external callers only)")
		fs.StringVar(&role, "role", "", "the role to start from (atto agent roles)")
		fs.BoolVar(&useWorktree, "worktree", false, "work in a git worktree of its own, on a new branch")
	}
	if sub == "close" {
		fs.BoolVar(&force, "force", false, "remove worktrees with uncommitted changes")
	}
	jsonOut := false
	if sub == "wait" || sub == "report" {
		fs.BoolVar(&jsonOut, "json", false, "JSON report")
	}
	done := fs.Bool("done", false, "close: every agent that is not running or queued")
	words, err := parseWords(fs, rest)
	if err != nil {
		return fmt.Errorf("%v\n%s", err, agentUsage)
	}
	override := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "m" || f.Name == "effort" {
			override = true
		}
	})
	if override && (os.Getenv("ATTO_SESSION_ID") != "" || config.InAgent() || config.InAgentCommand()) {
		return fmt.Errorf("-m and -effort are for external callers only; inside atto use -role")
	}
	switch sub {
	case "spawn", "task", "send", "wait", "report", "list", "interrupt", "close", "roles":
	default:
		return fmt.Errorf("unknown subcommand %q\n%s", sub, agentUsage)
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return fmt.Errorf("%s: %w", config.SettingsPath(), err)
	}
	switch sub {
	case "spawn", "task", "send":
		if !settings.AgentsEnabled() {
			return fmt.Errorf(`agents are off. Only the user can turn them on: "agents": {"enabled": true} in %s`, config.SettingsPath())
		}
	}
	if *session == "" {
		if config.InAgent() || config.InAgentCommand() { // atto's own commands always name their session
			return requireSession(*session)
		}
		parentOut := out
		if jsonOut {
			parentOut = os.Stderr
		}
		id, release, err := externalParent(parentOut, sub == "spawn")
		if err != nil {
			return err
		}
		if id == "" && sub != "roles" {
			release()
			if sub == "list" {
				fmt.Fprintln(out, "no agents")
				return nil
			}
			return errors.New("no agents started from this project (atto agent spawn starts one)")
		}
		*session = id
		if sub == "spawn" || sub == "close" {
			defer release()
		} else {
			release()
		}
	}
	if sub == "close" {
		err := agentRemove(out, *session, words, *done, force)
		if ferr := forgetExternalParent(*session); err == nil {
			err = ferr
		}
		return err
	}
	switch sub {
	case "list":
		return agentList(out, *session)
	case "roles":
		cwd, _ := os.Getwd()
		presets, warns := agentstate.LoadPresets(agentstate.Dirs(cwd, agent.ProjectRoot(cwd)))
		for _, p := range presets {
			src := "built-in"
			if p.Path != "" {
				src = core.ShortPath(p.Path)
			}
			fmt.Fprintf(out, "%s: %s (%s)\n", p.Name, p.Description, src)
		}
		for _, w := range warns {
			fmt.Fprintf(out, "warning: %s\n", w)
		}
		return nil
	case "wait":
		var cands []agentstate.State
		if len(words) == 0 {
			cands = agentstate.List(*session)
		}
		for _, w := range words {
			st, err := agentAt(*session, w)
			if err != nil {
				return err
			}
			cands = append(cands, st)
		}
		return agentWait(out, *session, cands, *timeout, jsonOut)
	}
	if len(words) == 0 {
		return fmt.Errorf("atto agent %s needs an AGENT\n%s", sub, agentUsage)
	}
	addr, words := words[0], words[1:]
	text := strings.TrimSpace(strings.Join(words, " "))

	switch sub {
	case "spawn":
		if err := agentstate.ValidName(addr); err != nil {
			return err
		}
		if role == "" && args[0] == "start" && len(words) >= 2 && isPreset(words[0]) { // start NAME PRESET "task"
			role, text = words[0], strings.TrimSpace(strings.Join(words[1:], " "))
		}
		if text == "" {
			return fmt.Errorf(`give the agent its task: atto agent spawn %s "..."`, addr)
		}
		if d, maxd := agentstate.Depth(*session), settings.AgentMaxDepth(); d >= maxd {
			return fmt.Errorf("agents may nest %d deep (\"agents\": {\"maxDepth\": N} in settings.json raises it), and this session is %s: do the work yourself", maxd, agentstate.PathOf(*session))
		}
		if role == "" {
			role = "general"
		}
		return agentStart(out, settings, *session, addr, role, text, model, effort, useWorktree)
	case "report":
		st, err := agentAt(*session, addr)
		if err != nil {
			return err
		}
		return writeAgentReport(out, st, jsonOut)
	case "interrupt":
		st, err := agentAt(*session, addr)
		if err != nil {
			return err
		}
		if t := st.Latest(); !t.Status.Active() {
			return fmt.Errorf("agent %s is %s, not running", addr, t.Status)
		}
		if err := agentstate.RequestInterrupt(st.Parent, st.Name, st.Turns); err != nil {
			return err
		}
		// Wait for the turn to record its stop, so an immediate task or
		// close sees an idle agent rather than racing the cancellation.
		for deadline := time.Now().Add(agentInterruptWait); st.Latest().Status.Active(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				if _, err := jobs.Kill(st.Parent, st.Job); err != nil {
					return err
				}
				jobs.KillAll(st.Session)
				t := st.Latest()
				t.Status, t.Ended = agentstate.Stopped, time.Now()
				if err := agentstate.SaveTurn(st.Parent, st.Name, t); err != nil {
					return err
				}
				fmt.Fprintf(out, "agent %s force-stopped after it did not respond to the interrupt. Give it a new task with atto agent task %s \"...\"\n", addr, addr)
				return nil
			}
		}
		fmt.Fprintf(out, "agent %s interrupted. Give it a new task with atto agent task %s \"...\"\n", addr, addr)
	case "task":
		if text == "" {
			return fmt.Errorf(`usage: atto agent task AGENT "<text>"`)
		}
		st, err := agentAt(*session, addr)
		if err != nil {
			return err
		}
		release, err := agentstate.LockTurn(st.Parent, st.Name)
		if err != nil {
			return err
		}
		defer release()
		st, err = agentstate.Load(st.Parent, st.Name)
		if err != nil {
			return err
		}
		from, to := agentstate.PathOf(*session), agentstate.PathOf(st.Session)
		msg := agentstate.Envelope(agentstate.NewTask, from, to, text)
		if t := st.Latest(); t.Status.Active() || args[0] == "steer" {
			if err := events.Push(st.Session, events.Event{Source: "agent", Text: msg, Title: "◆ new task from " + from}); err != nil {
				return err
			}
			fmt.Fprintf(out, "agent %s is %s: it takes the task after its current step.\n", to, t.Status)
			return nil
		}
		if err := startTurn(&st, msg); err != nil {
			return err
		}
		fmt.Fprintf(out, "agent %s: turn %d started (job %d). Its final answer reaches you when it ends; wait: atto agent wait %s\n", to, st.Turns, st.Job, addr)
	case "send":
		if text == "" {
			return fmt.Errorf(`usage: atto agent send AGENT "<text>"`)
		}
		t, err := agentstate.Resolve(*session, addr)
		if err != nil {
			return err
		}
		if t.Session == *session {
			return errors.New("that is you: send messages to other agents")
		}
		from := agentstate.PathOf(*session)
		if err := events.Push(t.Session, events.Event{Source: "agent", Quiet: true, Text: agentstate.Envelope(agentstate.Message, from, t.Path, text), Title: "◆ message from " + from}); err != nil {
			return err
		}
		when := "with its next turn"
		if t.State != nil && t.State.Latest().Status.Active() {
			when = "after its current step"
		}
		fmt.Fprintf(out, "sent to %s: it reads it %s.\n", t.Path, when)
	}
	return nil
}

// agentAliases maps the commands' old names to the new ones.
var agentAliases = map[string]string{
	"start": "spawn", "next": "task", "steer": "task", "wait-any": "wait",
	"stop": "interrupt", "rm": "close", "presets": "roles", "ls": "list",
}

// agentAt is the agent addr names, seen from session: one that some
// session started, not a root.
func agentAt(session, addr string) (agentstate.State, error) {
	t, err := agentstate.Resolve(session, addr)
	if err != nil {
		return agentstate.State{}, err
	}
	if t.State == nil {
		return agentstate.State{}, fmt.Errorf("%s is not an agent anyone started (message it with atto agent send %s)", t.Path, addr)
	}
	return *t.State, nil
}

func isPreset(name string) bool {
	cwd, _ := os.Getwd()
	presets, _ := agentstate.LoadPresets(agentstate.Dirs(cwd, agent.ProjectRoot(cwd)))
	_, err := agentstate.Find(presets, name)
	return err == nil
}

// agentRemove closes agents that are done (not running or queued), with
// the agents they started: their state goes, freeing the names, and their
// sessions are archived. An agent's worktree goes too, unless it has
// uncommitted changes and not force; its branch stays. Named agents are
// all closed or none; -done closes those it can and errs about the rest.
func agentRemove(out io.Writer, parent string, names []string, done, force bool) error {
	var trees [][]agentstate.State // each: an agent and its descendants, deepest first
	var refused []string
	check := func(tree []agentstate.State) error {
		for _, s := range tree {
			if t := s.Latest(); t.Status.Active() {
				return fmt.Errorf("agent %s is %s: interrupt it first (atto agent interrupt %s)", agentstate.PathOf(s.Session), t.Status, agentstate.PathOf(s.Session))
			}
		}
		for _, s := range tree {
			if err := checkWorktree(s, force); err != nil {
				return err
			}
		}
		return nil
	}
	switch {
	case done && len(names) > 0, !done && len(names) == 0:
		return fmt.Errorf("usage: atto agent close AGENT... | atto agent close -done")
	case done:
		for _, s := range agentstate.List(parent) {
			tree := subtree(s)
			if err := check(tree); err != nil {
				if !strings.Contains(err.Error(), "interrupt it first") {
					refused = append(refused, err.Error())
				}
				continue
			}
			trees = append(trees, tree)
		}
		if len(trees) == 0 && len(refused) == 0 {
			fmt.Fprintln(out, "no finished agents")
			return nil
		}
	default:
		for _, n := range names {
			s, err := agentAt(parent, n)
			if err != nil {
				return err
			}
			tree := subtree(s)
			if err := check(tree); err != nil {
				return err
			}
			trees = append(trees, tree)
		}
	}
	for _, tree := range trees {
		blocked := make(map[string]bool)
		for _, s := range tree {
			if blocked[s.Session] {
				blocked[s.Parent] = true
				continue
			}
			path := agentstate.PathOf(s.Session)
			if s.Worktree != "" {
				line, err := removeWorktree(s, force)
				if err != nil {
					refused = append(refused, fmt.Sprintf("agent %s: removing its worktree: %v (ancestors kept)", path, err))
					blocked[s.Parent] = true
					continue
				}
				fmt.Fprintln(out, line)
			}
			if p, err := session.Find(s.Session); err == nil && !isArchived(p) {
				if _, err := session.Archive(p); err != nil {
					fmt.Fprintf(out, "warning: agent %s: archiving its session: %v\n", path, err)
				}
			}
			agentstate.Remove(s.Parent, s.Name)
			fmt.Fprintf(out, "closed agent %s (session %s archived)\n", path, s.Session)
		}
	}
	if len(refused) > 0 {
		return errors.New(strings.Join(refused, "\n"))
	}
	return nil
}

// subtree is s and the agents below it, deepest first.
func subtree(s agentstate.State) []agentstate.State {
	var out []agentstate.State
	for _, c := range agentstate.List(s.Session) {
		out = append(out, subtree(c)...)
	}
	return append(out, s)
}

// checkWorktree refuses to remove an agent whose worktree has
// uncommitted changes, unless force.
func checkWorktree(s agentstate.State, force bool) error {
	if force {
		return nil
	}
	dirt, err := worktreeDirt(s)
	if err != nil {
		return fmt.Errorf("agent %s: checking its worktree: %v", s.Name, err)
	}
	if dirt != "" {
		return fmt.Errorf("agent %s: its worktree %s has uncommitted changes:\n%s\ncommit them (or have it commit: atto agent task %s \"commit your work\"), or close it anyway with atto agent close -force %s", s.Name, s.Worktree, dirt, s.Name, s.Name)
	}
	return nil
}

// parseWords parses flags anywhere among the words (atto agent wait a
// -timeout 5m) and returns the words.
func parseWords(fs *flag.FlagSet, args []string) ([]string, error) {
	var words []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return words, nil
		}
		words = append(words, args[0])
		args = args[1:]
	}
}

func contains(list []string, s string) bool {
	return slices.Contains(list, s)
}

// agentStart creates agent name from preset and starts its first turn.
func agentStart(out io.Writer, settings config.Settings, parent, name, preset, task, model, effortOverride string, useWorktree bool) error {
	release, err := agentstate.StartWork(parent)
	if err != nil {
		return err
	}
	defer release()

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
	pm, pe := sessionModel(parent)
	ref, effort, err := agentModel(models, settings, p, pm, pe)
	if err != nil {
		return err
	}
	var wt worktree
	if useWorktree {
		if _, err := agentstate.Load(parent, name); err == nil {
			return fmt.Errorf("an agent named %q exists: give it a follow-up with atto agent next %s, or pick another name", name, name)
		}
		if wt, err = planWorktree(cwd, parent, name); err != nil {
			return err
		}
		if err := wt.create(); err != nil {
			return err
		}
		cwd = wt.Cwd
	}
	undo := func() {
		if useWorktree {
			wt.undo()
		}
	}
	w := session.NewAgent(cwd, parent)
	st := agentstate.State{
		Name: name, Parent: parent, Session: w.ID, Preset: p.Name, Instructions: p.Instructions,
		Model: ref.String(), Effort: effort, Cwd: cwd, Task: task, Created: time.Now(),
		Worktree: wt.Path, Branch: wt.Branch, Base: wt.Base, Repo: wt.Repo,
	}
	if err := agentstate.Create(st); err != nil {
		undo()
		return err
	}
	// The session exists from the start, so every turn resumes it.
	w.Append(session.Entry{Type: session.TypeName, Name: name})
	w.Close()
	if err := w.Err(); err != nil {
		agentstate.Remove(parent, name)
		undo()
		return err
	}
	if err := startTurnLocked(&st, task); err != nil {
		agentstate.Remove(parent, name)
		undo()
		return err
	}
	fmt.Fprintf(out, "agent %s started (session %s, %s · %s, role %s, job %d).\n", agentstate.PathOf(st.Session), st.Session, st.Model, orDash(effort), p.Name, st.Job)
	if useWorktree {
		fmt.Fprintf(out, "It works in worktree %s on branch %s (from %.7s); atto agent close %s removes the worktree and keeps the branch.\n", st.Worktree, st.Branch, st.Base, name)
	}
	fmt.Fprintf(out, "Its final answer reaches you when its turn ends. wait: atto agent wait %s · new task: atto agent task %s \"...\" · message: atto agent send %s \"...\"\n", name, name, name)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// startTurn starts the next turn of st with message text, as a job of the
// parent session, and saves st.
func startTurn(st *agentstate.State, text string) error {
	release, err := agentstate.StartWork(st.Session)
	if err != nil {
		return err
	}
	defer release()
	return startTurnLocked(st, text)
}

func startTurnLocked(st *agentstate.State, text string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	st.Turns++
	st.Prompt, st.Job = text, 0
	if err := agentstate.Save(*st); err != nil {
		return err
	}
	args := []string{exe, "_agent-turn", "-session", st.Parent, st.Name, fmt.Sprint(st.Turns)}
	j, err := jobs.StartAgentArgs(st.Parent, st.Cwd, "agent "+st.Name, args)
	if err != nil {
		return fmt.Errorf("starting agent %s: %w", st.Name, err)
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
	_, entries, err := session.Load(path)
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		switch e.Type {
		case session.TypeModel:
			model = e.Provider + "/" + e.Model
		case session.TypeEffort:
			effort = e.Effort
		}
	}
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

// takeFinalAnswer removes from parent's inbox the final answers of st that
// wait there: its report says the same, once.
func takeFinalAnswer(parent string, st agentstate.State) {
	evs := events.Drain(parent)
	var keep []events.Event
	mark := "◆ agent " + agentstate.PathOf(st.Session) + " "
	for _, e := range evs {
		if e.Source == "agent" && strings.HasPrefix(e.Title, mark) {
			continue
		}
		keep = append(keep, e)
	}
	events.Requeue(parent, keep)
}

// agentWait blocks until the first of cands that is running ends, then
// prints its report.
func agentWait(out io.Writer, parent string, cands []agentstate.State, timeout time.Duration, jsonOut bool) error {
	if len(cands) == 1 && !cands[0].Latest().Status.Active() {
		takeFinalAnswer(parent, cands[0])
		return writeAgentReport(out, cands[0], jsonOut) // already over
	}
	var running []agentstate.State
	for _, s := range cands {
		if s.Latest().Status.Active() {
			running = append(running, s)
		}
	}
	if len(running) == 0 {
		return fmt.Errorf("no agent is running (see atto agent list)")
	}
	start := time.Now()
	for {
		for _, s := range running {
			if !s.Latest().Status.Active() {
				takeFinalAnswer(parent, s)
				return writeAgentReport(out, s, jsonOut)
			}
		}
		if timeout > 0 && time.Since(start) >= timeout {
			fmt.Fprintf(out, "still running after %s: %s\n", timeout, stateNames(running))
			return ExitCode(124)
		}
		if events.WokenSince(parent, start) {
			fmt.Fprintf(out, "stopped waiting: the user sent a message. still running: %s\n", stateNames(running))
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func stateNames(sts []agentstate.State) string {
	var names []string
	for _, s := range sts {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}

// agentReport is what atto agent report prints: a status line and the
// agent's last message.
func agentReport(st agentstate.State) string {
	t := st.Latest()
	var b strings.Builder
	fmt.Fprintf(&b, "agent %s · turn %d %s", agentstate.PathOf(st.Session), t.N, t.Status)
	if d := t.Duration(); d > 0 {
		fmt.Fprintf(&b, " · %s", tui.FormatDuration(d))
	}
	if t.PromptTokens+t.OutputTokens > 0 {
		fmt.Fprintf(&b, " · tokens %s in (%s cached), %s out", tui.FormatTokens(t.PromptTokens), tui.FormatTokens(t.CachedTokens), tui.FormatTokens(t.OutputTokens))
	}
	if t.Cost > 0 {
		fmt.Fprintf(&b, " · ≈$%.2f", t.Cost)
	}
	fmt.Fprintf(&b, "\n%s · session %s\n", st.Model, st.Session)
	if st.Worktree != "" {
		fmt.Fprintf(&b, "worktree %s · branch %s\n", st.Worktree, st.Branch)
	}
	if t.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", t.Error)
	}
	msg := session.LastAssistant(st.Session)
	switch {
	case msg != "":
		b.WriteString("\n" + msg + "\n")
	case t.Status.Active():
		b.WriteString("\n(no message yet)\n")
	default:
		b.WriteString("\n(no message)\n")
	}
	return b.String()
}

// writeAgentReport keeps the machine report independent of text formatting.
func writeAgentReport(out io.Writer, st agentstate.State, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprint(out, agentReport(st))
		return err
	}
	type tokens struct {
		In     int `json:"in"`
		Cached int `json:"cached"`
		Out    int `json:"out"`
	}
	t := st.Latest()
	return json.NewEncoder(out).Encode(struct {
		Name     string            `json:"name"`
		Status   agentstate.Status `json:"status"`
		Turn     int               `json:"turn"`
		Duration float64           `json:"duration"`
		Tokens   tokens            `json:"tokens"`
		Cost     float64           `json:"cost,omitempty"`
		Session  string            `json:"session"`
		Model    string            `json:"model"`
		Message  string            `json:"message"`
		Error    string            `json:"error,omitempty"`
		Worktree string            `json:"worktree,omitempty"`
		Branch   string            `json:"branch,omitempty"`
	}{st.Name, t.Status, t.N, t.Duration().Seconds(), tokens{t.PromptTokens, t.CachedTokens, t.OutputTokens}, t.Cost, st.Session, st.Model, session.LastAssistant(st.Session), t.Error, st.Worktree, st.Branch})
}

func agentList(out io.Writer, parent string) error {
	var rows []agentstate.State
	var walk func(p string)
	walk = func(p string) {
		for _, s := range agentstate.List(p) {
			rows = append(rows, s)
			walk(s.Session)
		}
	}
	walk(parent)
	if len(rows) == 0 {
		fmt.Fprintln(out, "no agents")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tROLE\tMODEL\tSTATUS\tTURN\tTASK")
	for _, s := range rows {
		t := s.Latest()
		d := "-"
		if t.Duration() > 0 {
			d = tui.FormatDuration(t.Duration())
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", agentstate.PathOf(s.Session), s.Preset, s.Model, t.Status, d, tui.Truncate(tui.FirstLine(s.Task), 60, "…"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, s := range rows {
		if s.Worktree != "" {
			fmt.Fprintf(out, "%s: worktree %s · branch %s\n", agentstate.PathOf(s.Session), s.Worktree, s.Branch)
		}
	}
	return nil
}

// ExitCode makes atto exit with this status after what the command
// printed, and print nothing more.
type ExitCode int

func (e ExitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// RunAgentTurn is the hidden entry point of an agent's turn (atto
// _agent-turn -session <parent> <name> <turn>), started by atto agent as
// a job of the parent session. It waits for a slot, runs the turn as
// atto -p resuming the agent's session, records how it went and tells
// the parent. It exits 0 once it has told the parent, however the turn
// went.
func RunAgentTurn(args []string, _ io.Writer) error {
	fs := newFlags("_agent-turn")
	parent := fs.String("session", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 2 || *parent == "" {
		return fmt.Errorf("usage: atto _agent-turn -session <parent> <name> <turn> (started by atto agent)")
	}
	name := fs.Arg(0)
	st, err := agentstate.Load(*parent, name)
	if err != nil {
		return err
	}
	if fmt.Sprint(st.Turns) != fs.Arg(1) {
		return fmt.Errorf("agent %s is at turn %d, not %s", name, st.Turns, fs.Arg(1))
	}
	t := agentstate.Turn{N: st.Turns, Status: agentstate.Queued, Queued: time.Now()}
	_ = agentstate.SaveTurn(*parent, name, t)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancelCause(sigCtx)
	defer cancel(nil)
	go watchAgentInterrupt(ctx, st, cancel)
	release, err := agentstate.Acquire(ctx, *parent, func() int {
		s, _ := config.LoadSettings()
		return s.AgentLimit()
	})
	if err != nil {
		if errors.Is(context.Cause(ctx), agent.ErrUserInterrupt) {
			t.Status, t.Ended = agentstate.Stopped, time.Now()
			return agentstate.SaveTurn(*parent, name, t)
		}
		return err
	}
	defer release()
	t.Status, t.Started = agentstate.Running, time.Now()
	_ = agentstate.SaveTurn(*parent, name, t)

	var res printResult
	runErr := RunPrint(PrintOptions{
		Prompt: st.Prompt, Model: st.Model, Effort: st.Effort, Resume: st.Session, Format: "text", Verbose: true,
		Worker: &agent.Worker{Name: name, Preset: st.Preset, Instructions: st.Instructions, Worktree: st.Worktree, Branch: st.Branch,
			Path: agentstate.PathOf(st.Session), Parent: agentstate.PathOf(*parent), CanSpawn: canSpawn(st.Session)},
		done: func(r printResult) { res = r }, turnContext: ctx,
	})
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
	releaseTurn, err := agentstate.LockTurn(st.Parent, st.Name)
	if err != nil {
		return err
	}
	defer releaseTurn()
	// The answer first: a wait that sees the turn over takes it from the
	// inbox, so it is not delivered twice.
	if err := events.Push(*parent, turnEvent(st, t)); err != nil {
		return err
	}
	if err := agentstate.SaveTurn(*parent, name, t); err != nil {
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
	if err := startTurn(&st, events.Format(evs)); err != nil {
		events.Requeue(st.Session, evs)
		return err
	}
	return nil
}

// watchAgentInterrupt handles the portable, per-turn control request while
// a worker waits for a slot, a model response, or a running shell command.
func watchAgentInterrupt(ctx context.Context, st agentstate.State, cancel context.CancelCauseFunc) {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if agentstate.Interrupted(st.Parent, st.Name, st.Turns) {
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
	from, to := agentstate.PathOf(st.Session), agentstate.PathOf(st.Parent)
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
			msg = msg[:finalAnswerMax] + fmt.Sprintf("\n[cut: atto agent report %s has all of it]", from)
		}
		body += "\n\n" + msg
	}
	return events.Event{Source: "agent", Text: agentstate.Envelope(agentstate.FinalAnswer, from, to, body),
		Title: fmt.Sprintf("◆ agent %s %s after %s", from, what, tui.FormatDuration(t.Duration()))}
}

// canSpawn reports whether agent session may start agents of its own.
func canSpawn(session string) bool {
	s, _ := config.LoadSettings()
	return s.AgentsEnabled() && agentstate.Depth(session) < s.AgentMaxDepth()
}
