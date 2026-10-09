package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// agentUsage: agents need no setting; old keys are read and ignored.
const agentUsage = `Agents are always available, from a plain shell or from a model's shell: no setting
turns them on, and no running atto session or atto -p root is needed.

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
  atto agent list [-all]               the agents you started, and theirs; IDs and addresses
  atto agent report AGENT              its last answer, status, duration and tokens
  atto agent interrupt AGENT           stop its running turn (it stays, for new tasks)
  atto agent close AGENT... | close -done [-force]
                                       remove agents you are done with, and theirs
  atto agent roles                     the roles -role picks from
  atto agent migrate                   convert agent data of the old layout (see below)

Agents are atto sessions other agents start, as in codex: equally capable,
with the same tools, and an agent is its session: its session ID is its identity.
Each has a path from the root of its tree: the session that started the first ones
is /root, its agent "tests" is /root/tests. AGENT is a name you gave (tests), a
path below you (tests/lint), ".." for the agent that started you, or a full path
(/root, /root/tests), or @ID (its own session ID or a unique prefix of at least
6 characters). Names and paths are labels that can be reused after an agent is
closed; the @ID never is. Inside atto, @ID stays within your own tree; outside atto
it works from any directory. An agent sees only what it is sent. When its turn ends,
its final answer reaches the agent that started it by itself, wrapped in
<atto_internal_context source="agent"> with Message Type FINAL_ANSWER; messages and
tasks arrive the same way (MESSAGE, NEW_TASK). wait exits with status 124 when
-timeout passes first, and returns early when the user sends a message. Close
agents once you have their result and no more work for them.

Agents work in this directory. With -worktree, an agent gets its own git
worktree (under the atto dir, worktrees/<session ID>) on a new branch
atto/<session ID> made from HEAD, and commits its work there: use it when agents
edit files in parallel. close removes the worktree, refusing while it has
uncommitted changes unless -force, and keeps the branch for you to merge.

Agents may start agents of their own at any depth, and every turn starts at
once. In the agents setting, "model" and "effort" apply to agents whose role
names none (else they use their parent's); older "enabled", "maxDepth" and
"maxConcurrent" keys are ignored.

From a plain shell (no ATTO_SESSION_ID, no -session), spawn starts an agent
with no parent: the root of a tree of its own (/root), recorded with the
project (git root, else cwd). Nothing sends its answers anywhere: poll it with
wait and report. Its NAME is a label among the open agents started from a shell
in the project: a unique name works, duplicates report their @id, status and
age. Paths first resolve that label, then follow its own tree: tests/lint. Use
the exact @id spawn prints to avoid ambiguity. list shows those agents and
their trees in this project; -all shows every project. wait without addresses
waits for any of them, and close -done closes those whose whole tree is done.
With -session ID the agent is a child of that session, and answers reach it.
spawn accepts -m provider/model and -effort LEVEL for outside callers only; in
atto's model shell (ATTO_SESSION_ID / ATTO_AGENT set), use -role instead.
Every command accepts -session ID (default: $ATTO_SESSION_ID).
wait and report accept -json: one object; duration is in seconds.
Old names still work: start (spawn), next (task, idle), steer (task,
running), wait-any (wait), stop (interrupt), rm (close), presets (roles), read/show (report).

Agent data written by older versions (one directory per parent session) must be
converted once, by atto agent migrate, which backs ~/.atto up first. Every machine
that shares the data must run this atto or a newer one afterwards.`

// agentInterruptWait gives a worker time to record a graceful stop before
// an unresponsive process is force-stopped. Tests shorten it.
var agentInterruptWait = 10 * time.Second

// caller is who runs an atto agent command.
type caller struct {
	// session is the session the command acts for: the model's own, the one
	// -session names, or "" from a plain shell.
	session string
	// outside: run from a plain shell (by a person or an orchestrator), not
	// by a model's shell. Names are then labels of the project's agents.
	outside bool
}

// implicit reports an outside caller with no session: it addresses the
// agents started from a shell in this project.
func (c caller) implicit() bool { return c.session == "" && c.outside }

// sender is who a message from this caller is from.
func (c caller) sender() string {
	if c.implicit() {
		return agentstate.OutsideSender
	}
	return agentstate.PathOf(c.session)
}

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
	sessionID := sessionFlag(fs)
	timeout := fs.Duration("timeout", 0, "give up after this long")
	model, effort, role := "", "", ""
	useWorktree, force := false, false
	if sub == "spawn" {
		fs.StringVar(&model, "m", "", "model (outside callers only)")
		fs.StringVar(&effort, "effort", "", "effort (outside callers only)")
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
	all := false
	if sub == "list" {
		fs.BoolVar(&all, "all", false, "outside atto: agents of every project")
	}
	done := fs.Bool("done", false, "close: every agent that is not running or queued")
	words, err := parseInterleaved(fs, rest)
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
	case "spawn", "task", "send", "wait", "report", "list", "interrupt", "close", "roles", "migrate":
	default:
		return fmt.Errorf("unknown subcommand %q\n%s", sub, agentUsage)
	}
	if sub == "migrate" {
		return runAgentMigrate(out)
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return fmt.Errorf("%s: %w", config.SettingsPath(), err)
	}
	if sub != "roles" {
		if err := ensureAgentLayout(out); err != nil {
			return err
		}
	}
	c := caller{session: *sessionID, outside: outsideAgentCaller()}
	if all {
		if !c.outside {
			return errors.New("atto agent list -all is for external callers only; inside atto use atto agent list")
		}
		return agentListAll(out)
	}
	if c.session == "" && !c.outside && sub != "roles" {
		return requireSession(c.session)
	}

	switch sub {
	case "close":
		return agentRemove(out, c, words, *done, force)
	case "list":
		return agentList(out, c)
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
			if c.implicit() {
				cands = agentstate.ExternalRoots(externalProjectHere(), false)
			} else {
				cands = agentstate.Children(c.session)
			}
		}
		for _, w := range words {
			st, err := agentAt(c, w)
			if err != nil {
				return err
			}
			cands = append(cands, st)
		}
		return agentWait(out, c, cands, *timeout, jsonOut)
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
		if role == "" {
			role = "general"
		}
		return agentStart(out, settings, c, addr, role, text, model, effort, useWorktree)
	case "report":
		st, err := agentAt(c, addr)
		if err != nil {
			return err
		}
		return writeAgentReport(out, st, jsonOut)
	case "interrupt":
		st, err := agentAt(c, addr)
		if err != nil {
			return err
		}
		if t := st.Latest(); !t.Status.Active() {
			return fmt.Errorf("agent %s is %s, not running", addr, t.Status)
		}
		return interruptTurn(out, st, addr)
	case "task":
		if text == "" {
			return fmt.Errorf(`usage: atto agent task AGENT "<text>"`)
		}
		st, err := agentAt(c, addr)
		if err != nil {
			return err
		}
		release, err := agentstate.LockTurn(st.Session)
		if err != nil {
			return err
		}
		defer release()
		st, err = agentstate.Load(st.Session)
		if err != nil {
			return err
		}
		from, to := c.sender(), st.Path
		msg := agentstate.Envelope(agentstate.NewTask, from, to, text)
		if t := st.Latest(); t.Status.Active() || args[0] == "steer" {
			if err := events.Push(st.Session, events.Event{Source: "agent", Text: msg, Title: "◆ new task from " + from}); err != nil {
				return err
			}
			fmt.Fprintf(out, "agent %s is %s: it takes the task after its current step.\n", agentLabel(st), t.Status)
			return nil
		}
		if err := startTurn(&st, msg); err != nil {
			return err
		}
		if st.IsRoot() {
			fmt.Fprintf(out, "agent %s: turn %d started (job %d of session %s). Poll it: atto agent wait %s · atto agent report %s\n", agentLabel(st), st.Turns, st.Job, st.Session, addr, addr)
		} else {
			fmt.Fprintf(out, "agent %s: turn %d started (job %d). Its final answer reaches you when it ends; wait: atto agent wait %s\n", agentLabel(st), st.Turns, st.Job, addr)
		}
	case "send":
		if text == "" {
			return fmt.Errorf(`usage: atto agent send AGENT "<text>"`)
		}
		t, err := resolveAgentAddress(c, addr)
		if err != nil {
			return err
		}
		if t.Session == c.session {
			return errors.New("that is you: send messages to other agents")
		}
		from := c.sender()
		if err := events.Push(t.Session, events.Event{Source: "agent", Quiet: true, Text: agentstate.Envelope(agentstate.Message, from, t.Path, text), Title: "◆ message from " + from}); err != nil {
			return err
		}
		when := "with its next turn"
		if t.State != nil && t.State.Latest().Status.Active() {
			when = "after its current step"
		}
		fmt.Fprintf(out, "sent to %s: it reads it %s.\n", targetLabel(t), when)
	}
	return nil
}

// interruptTurn asks st's running turn to stop and waits for it to say so.
func interruptTurn(out io.Writer, st agentstate.State, addr string) error {
	if err := agentstate.RequestInterrupt(st.Session, st.Turns); err != nil {
		return err
	}
	// Wait for the turn to record its stop, so an immediate task or
	// close sees an idle agent rather than racing the cancellation.
	for deadline := time.Now().Add(agentInterruptWait); st.Latest().Status.Active(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			return forceStop(out, st, addr)
		}
	}
	fmt.Fprintf(out, "agent %s interrupted. Give it a new task with atto agent task %s \"...\"\n", addr, addr)
	return nil
}

// forceStop ends a turn that did not answer the interrupt request.
func forceStop(out io.Writer, st agentstate.State, addr string) error {
	owner, id := st.JobRef()
	if _, err := jobs.Kill(owner, id); err != nil {
		return err
	}
	jobs.KillAll(st.Session)
	t := st.Latest()
	t.Status, t.Ended = agentstate.Stopped, time.Now()
	if err := agentstate.SaveTurn(st.Session, t); err != nil {
		return err
	}
	fmt.Fprintf(out, "agent %s force-stopped after it did not respond to the interrupt. Give it a new task with atto agent task %s \"...\"\n", addr, addr)
	return nil
}

// agentAliases maps the commands' old names to the new ones.
var agentAliases = map[string]string{
	"start": "spawn", "next": "task", "steer": "task", "wait-any": "wait",
	"stop": "interrupt", "rm": "close", "presets": "roles", "ls": "list",
	"read": "report", "show": "report",
}

// ID scope follows the actual caller, never a -session override. Names and
// paths still resolve relative to the selected session as before.
func outsideAgentCaller() bool {
	return os.Getenv("ATTO_SESSION_ID") == "" && !config.InAgentCommand()
}

func resolveAgentAddress(c caller, addr string) (agentstate.Target, error) {
	if strings.HasPrefix(addr, "@") {
		if c.outside {
			return agentstate.Resolve("", addr)
		}
		self := os.Getenv("ATTO_SESSION_ID")
		if self == "" {
			self = c.session
		}
		if err := requireSession(self); err != nil {
			return agentstate.Target{}, err
		}
		return agentstate.Resolve(self, addr)
	}
	if c.implicit() {
		return agentstate.ResolveOutside(externalProjectHere(), addr)
	}
	return agentstate.Resolve(c.session, addr)
}

// agentAt is the agent addr names, seen from the caller: a managed agent,
// not an ordinary session.
func agentAt(c caller, addr string) (agentstate.State, error) {
	t, err := resolveAgentAddress(c, addr)
	if err != nil {
		return agentstate.State{}, err
	}
	if t.State == nil {
		return agentstate.State{}, fmt.Errorf("%s is not an agent anyone started (message it with atto agent send %s)", t.Path, addr)
	}
	return *t.State, nil
}

// agentLabel is how output spells an agent for this caller.
func agentLabel(st agentstate.State) string { return agentstate.Label(st, outsideAgentCaller()) }

func targetLabel(t agentstate.Target) string {
	if t.State != nil {
		return agentLabel(*t.State)
	}
	return t.Path
}

func isPreset(name string) bool {
	cwd, _ := os.Getwd()
	presets, _ := agentstate.LoadPresets(agentstate.Dirs(cwd, agent.ProjectRoot(cwd)))
	_, err := agentstate.Find(presets, name)
	return err == nil
}

// agentRemove closes agents that are done (not running or queued), with
// the agents they started: they become closed records (their IDs stay
// reserved, their names free), their sessions are archived. An agent's
// worktree goes too, unless it has uncommitted changes and not force; its
// branch stays. Named agents are all closed or none; -done closes those it
// can and errs about the rest. The tree stays locked while this runs, and
// each agent is marked closing before anything is taken away, so nothing
// new starts below it.
func agentRemove(out io.Writer, c caller, names []string, done, force bool) error {
	var trees [][]agentstate.State // each: an agent and its descendants, deepest first
	var refused []string
	check := func(tree []agentstate.State) error {
		for _, s := range tree {
			if t := s.Latest(); t.Status.Active() {
				return fmt.Errorf("agent %s is %s: interrupt it first (atto agent interrupt @%s)", agentLabel(s), t.Status, s.Session)
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
		rows := agentstate.Children(c.session)
		if c.implicit() {
			rows = agentstate.ExternalRoots(externalProjectHere(), false)
		}
		for _, s := range rows {
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
			s, err := agentAt(c, n)
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
		if err := closeTree(out, tree, force, &refused); err != nil {
			refused = append(refused, err.Error())
		}
	}
	if len(refused) > 0 {
		return errors.New(strings.Join(refused, "\n"))
	}
	return nil
}

// closeTree closes one agent and what is below it (deepest first).
func closeTree(out io.Writer, tree []agentstate.State, force bool, refused *[]string) error {
	if len(tree) == 0 {
		return nil
	}
	release, err := agentstate.LockTree(tree[len(tree)-1].Session)
	if err != nil {
		return err
	}
	defer release()
	for _, s := range tree {
		if err := agentstate.MarkClosing(s.Session); err != nil {
			return fmt.Errorf("agent %s: closing: %v", agentLabel(s), err)
		}
	}
	blocked := make(map[string]bool)
	for _, s := range tree {
		if blocked[s.Session] {
			blocked[s.Parent] = true
			continue
		}
		label := agentLabel(s)
		if s.Worktree != "" {
			line, err := removeWorktree(s, force)
			if err != nil {
				*refused = append(*refused, fmt.Sprintf("agent %s: removing its worktree: %v (ancestors kept)", label, err))
				blocked[s.Parent] = true
				continue
			}
			fmt.Fprintln(out, line)
		}
		archive := ""
		if p, err := session.Find(s.Session); err == nil && !isArchived(p) {
			if dst, err := session.Archive(p); err != nil {
				fmt.Fprintf(out, "warning: agent %s: archiving its session: %v\n", label, err)
			} else {
				archive = dst
			}
		} else if err == nil {
			archive = p
		}
		if err := agentstate.MarkClosed(s.Session, archive); err != nil {
			*refused = append(*refused, fmt.Sprintf("agent %s: closing: %v", label, err))
			blocked[s.Parent] = true
			continue
		}
		fmt.Fprintf(out, "closed agent %s (@%s, session %s archived)\n", label, agentstate.ShortID(s.Session), s.Session)
	}
	// An agent whose teardown failed is open again: nothing was lost.
	for _, s := range tree {
		if cur, err := agentstate.Load(s.Session); err == nil && cur.Lifecycle == agentstate.Closing {
			cur.Lifecycle = agentstate.Open
			_ = agentstate.Save(cur)
		}
	}
	return nil
}

// subtree is s and the agents below it, deepest first.
func subtree(s agentstate.State) []agentstate.State {
	var out []agentstate.State
	for _, c := range agentstate.Children(s.Session) {
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
		return fmt.Errorf("agent %s: its worktree %s has uncommitted changes:\n%s\ncommit them (or have it commit: atto agent task @%s \"commit your work\"), or close it anyway with atto agent close -force @%s", s.Name, s.Worktree, dirt, s.Session, s.Session)
	}
	return nil
}

// takeFinalAnswer removes from the inbox of st's parent the final answers of
// st that wait there: its report says the same, once.
func takeFinalAnswer(st agentstate.State) {
	parent := st.Parent
	if parent == "" {
		return // a root has no recipient
	}
	evs := events.Drain(parent)
	var keep []events.Event
	mark := "◆ agent " + st.Path + " "
	for _, e := range evs {
		if e.Source == "agent" && strings.HasPrefix(e.Title, mark) {
			continue
		}
		keep = append(keep, e)
	}
	events.Requeue(parent, keep)
}

// refreshed is st as the agent's record has it now.
func refreshed(st agentstate.State) agentstate.State {
	if cur, err := agentstate.Load(st.Session); err == nil {
		return cur
	}
	return st
}

// agentWait blocks until the first of cands that is running ends, then
// prints its report.
func agentWait(out io.Writer, c caller, cands []agentstate.State, timeout time.Duration, jsonOut bool) error {
	if len(cands) == 1 && !cands[0].Latest().Status.Active() {
		takeFinalAnswer(cands[0])
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
		for i, s := range running {
			s = refreshed(s)
			running[i] = s
			if !s.Latest().Status.Active() {
				takeFinalAnswer(s)
				return writeAgentReport(out, s, jsonOut)
			}
		}
		if timeout > 0 && time.Since(start) >= timeout {
			fmt.Fprintf(out, "still running after %s: %s\n", timeout, stateNames(running))
			return ExitCode(124)
		}
		if c.session != "" && events.WokenSince(c.session, start) {
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
	fmt.Fprintf(&b, "agent %s · turn %d %s", agentLabel(st), t.N, t.Status)
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
	if st.SpawnedBy != nil {
		fmt.Fprintf(&b, "started by %s\n", spawnedByText(st.SpawnedBy))
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
	owner, job := st.JobRef()
	return json.NewEncoder(out).Encode(struct {
		Name      string               `json:"name"`
		Status    agentstate.Status    `json:"status"`
		Turn      int                  `json:"turn"`
		Duration  float64              `json:"duration"`
		Tokens    tokens               `json:"tokens"`
		Cost      float64              `json:"cost,omitempty"`
		Session   string               `json:"session"`
		Model     string               `json:"model"`
		Message   string               `json:"message"`
		Error     string               `json:"error,omitempty"`
		Worktree  string               `json:"worktree,omitempty"`
		Branch    string               `json:"branch,omitempty"`
		Path      string               `json:"path"`
		Parent    string               `json:"parent,omitempty"`
		Root      string               `json:"root"`
		Depth     int                  `json:"depth"`
		Role      string               `json:"role,omitempty"`
		Project   string               `json:"project,omitempty"`
		Origin    string               `json:"origin,omitempty"`
		Lifecycle agentstate.Lifecycle `json:"lifecycle"`
		JobOwner  string               `json:"jobOwner,omitempty"`
		Job       int                  `json:"job,omitempty"`
		SpawnedBy *session.SpawnedBy   `json:"spawnedBy,omitempty"`
	}{st.Name, t.Status, t.N, t.Duration().Seconds(), tokens{t.PromptTokens, t.CachedTokens, t.OutputTokens}, t.Cost, st.Session, st.Model, session.LastAssistant(st.Session), t.Error, st.Worktree, st.Branch,
		st.Path, st.Parent, st.Root, st.Depth, st.Preset, st.Project, st.Origin, st.Lifecycle, owner, job, st.SpawnedBy})
}

// spawnedByText is who started an agent, compactly: "sol·high t3".
func spawnedByText(b *session.SpawnedBy) string { return b.Brief() }

func agentList(out io.Writer, c caller) error {
	if c.implicit() {
		return agentListRows(out, c, outsideRows(externalProjectHere()))
	}
	return agentListRows(out, c, agentstate.Tree(c.session))
}

func agentListAll(out io.Writer) error {
	return agentListRows(out, caller{outside: true}, outsideRows(""))
}

// outsideRows are the agents started from a shell in project ("": any) and
// everything below them, each root followed by its tree.
func outsideRows(project string) []agentstate.State {
	var rows []agentstate.State
	for _, root := range agentstate.ExternalRoots(project, false) {
		rows = append(rows, root)
		rows = append(rows, agentstate.Tree(root.Session)...)
	}
	return rows
}

func agentListRows(out io.Writer, c caller, rows []agentstate.State) error {
	if len(rows) == 0 {
		fmt.Fprintln(out, "no agents")
		return listAgentWorkers(out, c.session, rows)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tID\tADDRESS\tROLE\tMODEL\tSTATUS\tTURN\tBY\tTASK")
	for _, s := range rows {
		t := s.Latest()
		d := "-"
		if t.Duration() > 0 {
			d = tui.FormatDuration(t.Duration())
		}
		fmt.Fprintf(tw, "%s\t%s\t@%s\t%s\t%s\t%s\t%s\t%s\t%s\n", agentstate.Label(s, c.outside), agentstate.ShortID(s.Session), agentstate.ShortID(s.Session), s.Preset, s.Model, t.Status, d, spawnedByText(s.SpawnedBy), tui.Truncate(tui.FirstLineWithEllipsis(s.Task), 60, "…"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, s := range rows {
		if s.Worktree != "" {
			fmt.Fprintf(out, "%s: worktree %s · branch %s\n", agentstate.Label(s, c.outside), s.Worktree, s.Branch)
		}
	}
	return listAgentWorkers(out, c.session, rows)
}

// listAgentWorkers adds execution state for sessions represented in the tree.
func listAgentWorkers(out io.Writer, parent string, rows []agentstate.State) error {
	if !daemon.Enabled() {
		return nil
	}
	workers, err := daemon.Workers()
	if err != nil {
		return err
	}
	for _, w := range workers {
		if w.Session != parent && !containsSession(rows, w.Session) {
			continue
		}
		state := "idle"
		if w.Busy {
			state = "working"
		}
		fmt.Fprintf(out, "session %s: worker %d · %s · %d client(s) · %s\n", w.Session, w.PID, state, w.Clients, w.Version)
	}
	return nil
}

func containsSession(rows []agentstate.State, id string) bool {
	for _, st := range rows {
		if st.Session == id {
			return true
		}
	}
	return false
}

// ExitCode makes atto exit with this status after what the command
// printed, and print nothing more.
type ExitCode int

func (e ExitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
