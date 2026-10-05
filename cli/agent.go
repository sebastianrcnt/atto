package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/subagent"
	"github.com/sebastianrcnt/atto/tui"
)

const agentUsage = `usage:
  atto agent start NAME PRESET "<task>"     start a subagent in the background; returns at once
  atto agent steer NAME "<message>"         add instructions to its running turn
  atto agent next NAME "<message>"          give an idle subagent a follow-up turn
  atto agent wait NAME [-timeout 10m]       block until its turn ends, then print its report
  atto agent wait-any [NAME...] [-timeout 10m]
                                            block until the first running one ends (default: any)
  atto agent report NAME                    its last message, status, duration and tokens
  atto agent list                           this session's subagents
  atto agent stop NAME                      interrupt its running turn
  atto agent rm NAME... | rm -done          remove finished subagents (their sessions are archived)
  atto agent presets                        the presets subagents start from

A subagent is a separate atto session working for this one: it sees only
the task (and later messages) you give it, not this conversation, and you
see only its final message. NAME is yours to pick (lowercase letters,
digits and dashes, unique in this session). PRESET is one of the presets
(atto agent presets): it fixes the subagent's model, effort and
instructions. Each turn runs in the background; when one ends you get an
[atto event] and read the result with atto agent report NAME. wait exits
with status 124 when -timeout passes first. Once you have a subagent's
result and no more work for it, remove it with atto agent rm NAME.

Subagents are off unless settings.json has "subagents": {"enabled": true};
"maxConcurrent" (default 3) caps the turns running at once, more wait in
a queue. Subagents can't start subagents of their own.`

// RunAgent implements "atto agent".
func RunAgent(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(out, agentUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	fs := newFlags("agent " + sub)
	session := sessionFlag(fs)
	timeout := fs.Duration("timeout", 0, "give up after this long")
	done := fs.Bool("done", false, "rm: every subagent that is not running or queued")
	words, err := parseWords(fs, rest)
	if err != nil {
		return fmt.Errorf("%v\n%s", err, agentUsage)
	}
	if err := requireSession(*session); err != nil {
		return err
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return fmt.Errorf("%s: %w", config.SettingsPath(), err)
	}
	switch sub {
	case "start", "next", "steer":
		if config.InSubagent() {
			return fmt.Errorf("a subagent can't start or steer subagents (%s is set): do the work yourself and report it", config.EnvSubagent)
		}
		if !settings.SubagentsEnabled() {
			return fmt.Errorf(`subagents are off. Only the user can turn them on: "subagents": {"enabled": true} in %s`, config.SettingsPath())
		}
	}
	if sub == "rm" {
		return agentRemove(out, *session, words, *done)
	}
	name := ""
	if sub != "list" && sub != "ls" && sub != "wait-any" && sub != "presets" {
		if len(words) == 0 {
			return fmt.Errorf("atto agent %s needs a NAME\n%s", sub, agentUsage)
		}
		name, words = words[0], words[1:]
		if err := subagent.ValidName(name); err != nil {
			return err
		}
	}
	text := strings.TrimSpace(strings.Join(words, " "))

	switch sub {
	case "start":
		if len(words) < 2 {
			return fmt.Errorf(`usage: atto agent start NAME PRESET "<task>" (presets: atto agent presets)`)
		}
		return agentStart(out, settings, *session, name, words[0], strings.TrimSpace(strings.Join(words[1:], " ")))
	case "next":
		if text == "" {
			return fmt.Errorf(`usage: atto agent next NAME "<message>"`)
		}
		st, err := subagent.Load(*session, name)
		if err != nil {
			return err
		}
		if t := st.Latest(); t.Status.Active() {
			return fmt.Errorf("subagent %s is %s: add to its turn with atto agent steer %s \"...\", or wait for it", name, t.Status, name)
		}
		if err := startTurn(&st, text); err != nil {
			return err
		}
		fmt.Fprintf(out, "subagent %s: turn %d started (job %d). You will get an [atto event] when it ends; wait: atto agent wait %s\n", name, st.Turns, st.Job, name)
	case "steer":
		if text == "" {
			return fmt.Errorf(`usage: atto agent steer NAME "<message>"`)
		}
		st, err := subagent.Load(*session, name)
		if err != nil {
			return err
		}
		if t := st.Latest(); !t.Status.Active() {
			return fmt.Errorf("subagent %s is %s, not running: give it a follow-up turn with atto agent next %s \"...\"", name, t.Status, name)
		}
		if err := events.Push(st.Session, events.Event{Source: "parent", Text: "Message from the parent agent: " + text, Title: "message from the parent agent"}); err != nil {
			return err
		}
		fmt.Fprintf(out, "sent to subagent %s; it reads it after its current step.\n", name)
	case "wait":
		st, err := subagent.Load(*session, name)
		if err != nil {
			return err
		}
		return agentWait(out, *session, []subagent.State{st}, *timeout)
	case "wait-any":
		var cands []subagent.State
		for _, s := range subagent.List(*session) {
			if len(words) == 0 || contains(words, s.Name) {
				cands = append(cands, s)
			}
		}
		for _, w := range words {
			if _, err := subagent.Load(*session, w); err != nil {
				return err
			}
		}
		return agentWait(out, *session, cands, *timeout)
	case "report":
		st, err := subagent.Load(*session, name)
		if err != nil {
			return err
		}
		fmt.Fprint(out, agentReport(st))
	case "list", "ls":
		return agentList(out, *session)
	case "stop":
		st, err := subagent.Load(*session, name)
		if err != nil {
			return err
		}
		if t := st.Latest(); !t.Status.Active() {
			return fmt.Errorf("subagent %s is %s, not running", name, t.Status)
		}
		if _, err := jobs.Kill(*session, st.Job); err != nil {
			return err
		}
		jobs.KillAll(st.Session) // the turn's own jobs
		fmt.Fprintf(out, "subagent %s stopped. Continue it with atto agent next %s \"...\"\n", name, name)
	case "presets":
		cwd, _ := os.Getwd()
		presets, warns := subagent.LoadPresets(subagent.Dirs(cwd, agent.ProjectRoot(cwd)))
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
	default:
		return fmt.Errorf("unknown subcommand %q\n%s", sub, agentUsage)
	}
	return nil
}

// agentRemove removes subagents that are done (not running or queued):
// their state goes, freeing the name, and their sessions are archived.
func agentRemove(out io.Writer, parent string, names []string, done bool) error {
	var subs []subagent.State
	switch {
	case done && len(names) > 0:
		return fmt.Errorf("usage: atto agent rm NAME... | atto agent rm -done")
	case done:
		for _, s := range subagent.List(parent) {
			if !s.Latest().Status.Active() {
				subs = append(subs, s)
			}
		}
		if len(subs) == 0 {
			fmt.Fprintln(out, "no finished subagents")
			return nil
		}
	case len(names) == 0:
		return fmt.Errorf("usage: atto agent rm NAME... | atto agent rm -done")
	default:
		for _, n := range names {
			s, err := subagent.Load(parent, n)
			if err != nil {
				return err
			}
			if t := s.Latest(); t.Status.Active() {
				return fmt.Errorf("subagent %s is %s: stop it first (atto agent stop %s)", n, t.Status, n)
			}
			subs = append(subs, s)
		}
	}
	for _, s := range subs {
		if path, err := session.Find(s.Session); err == nil && !isArchived(path) {
			if _, err := session.Archive(path); err != nil {
				fmt.Fprintf(out, "warning: subagent %s: archiving its session: %v\n", s.Name, err)
			}
		}
		subagent.Remove(parent, s.Name)
		fmt.Fprintf(out, "removed subagent %s (session %s archived)\n", s.Name, s.Session)
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
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// agentStart creates subagent name from preset and starts its first turn.
func agentStart(out io.Writer, settings config.Settings, parent, name, preset, task string) error {
	if task == "" {
		return fmt.Errorf("give the subagent its task: atto agent start %s %s \"...\"", name, preset)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	presets, _ := subagent.LoadPresets(subagent.Dirs(cwd, agent.ProjectRoot(cwd)))
	p, err := subagent.Find(presets, preset)
	if err != nil {
		return err
	}
	_, models, err := core.Load()
	if err != nil {
		return err
	}
	pm, pe := sessionModel(parent)
	ref, effort, err := subagentModel(models, settings, p, pm, pe)
	if err != nil {
		return err
	}
	w := session.NewSubagent(cwd, parent)
	st := subagent.State{
		Name: name, Parent: parent, Session: w.ID, Preset: p.Name, Instructions: p.Instructions,
		Model: ref.String(), Effort: effort, Cwd: cwd, Task: task, Created: time.Now(),
	}
	if err := subagent.Create(st); err != nil {
		return err
	}
	// The session exists from the start, so every turn resumes it.
	w.Append(session.Entry{Type: session.TypeName, Name: name})
	w.Close()
	if err := w.Err(); err != nil {
		subagent.Remove(parent, name)
		return err
	}
	if err := startTurn(&st, task); err != nil {
		subagent.Remove(parent, name)
		return err
	}
	fmt.Fprintf(out, "subagent %s started (session %s, %s · %s, preset %s, job %d).\n", name, st.Session, st.Model, orDash(effort), p.Name, st.Job)
	fmt.Fprintf(out, "You will get an [atto event] when its turn ends. Report: atto agent report %s · wait: atto agent wait %s · add instructions: atto agent steer %s \"...\"\n", name, name, name)
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
func startTurn(st *subagent.State, text string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	st.Turns++
	st.Prompt, st.Job = text, 0
	if err := subagent.Save(*st); err != nil {
		return err
	}
	args := []string{exe, "_agent-turn", "-session", st.Parent, st.Name, fmt.Sprint(st.Turns)}
	j, err := jobs.StartArgs(st.Parent, st.Cwd, "agent "+st.Name, args, true)
	if err != nil {
		return fmt.Errorf("starting subagent %s: %w", st.Name, err)
	}
	st.Job = j.ID
	return subagent.Save(*st)
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

// subagentModel picks a subagent's model and effort: the preset's, else
// settings.json's subagents.model and .effort, else what the parent
// session uses. A model or effort named in a preset or the settings must
// exist; one inherited from the parent that the model doesn't offer gives
// way to the default.
func subagentModel(models config.ModelsFile, settings config.Settings, p subagent.Preset, parentModel, parentEffort string) (config.ModelRef, string, error) {
	var cfg config.SubagentSettings
	if settings.Subagents != nil {
		cfg = *settings.Subagents
	}
	model, from := p.Model, "preset "+p.Name
	if model == "" && cfg.Model != "" {
		model, from = cfg.Model, "settings subagents.model"
	}
	ref, _, err := core.PickModelFrom(models, settings, model, parentModel)
	if err != nil {
		if model != "" {
			return ref, "", fmt.Errorf("%s: %w", from, err)
		}
		return ref, "", err
	}
	effort, from := p.Effort, "preset "+p.Name
	if effort == "" && cfg.Effort != "" {
		effort, from = cfg.Effort, "settings subagents.effort"
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

// agentWait blocks until the first of cands that is running ends, then
// prints its report.
func agentWait(out io.Writer, parent string, cands []subagent.State, timeout time.Duration) error {
	if len(cands) == 1 && !cands[0].Latest().Status.Active() {
		fmt.Fprint(out, agentReport(cands[0])) // already over
		return nil
	}
	var running []subagent.State
	for _, s := range cands {
		if s.Latest().Status.Active() {
			running = append(running, s)
		}
	}
	if len(running) == 0 {
		return fmt.Errorf("no subagent is running (see atto agent list)")
	}
	start := time.Now()
	for {
		for _, s := range running {
			if !s.Latest().Status.Active() {
				fmt.Fprint(out, agentReport(s))
				return nil
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

func stateNames(sts []subagent.State) string {
	var names []string
	for _, s := range sts {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}

// agentReport is what atto agent report prints: a status line and the
// subagent's last message.
func agentReport(st subagent.State) string {
	t := st.Latest()
	var b strings.Builder
	fmt.Fprintf(&b, "subagent %s · turn %d %s", st.Name, t.N, t.Status)
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
	if t.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", t.Error)
	}
	msg := lastAssistant(st.Session)
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

// lastAssistant is the text of the last assistant message on the
// session's active branch.
func lastAssistant(id string) string {
	path, err := session.Find(id)
	if err != nil {
		return ""
	}
	_, entries, err := session.Load(path)
	if err != nil {
		return ""
	}
	branch := session.Active(entries)
	for i := len(branch) - 1; i >= 0; i-- {
		if m := branch[i].Message; branch[i].Type == session.TypeMessage && m != nil && m.Role == "assistant" {
			if text := strings.TrimSpace(m.Content); text != "" {
				return text
			}
		}
	}
	return ""
}

func agentList(out io.Writer, parent string) error {
	list := subagent.List(parent)
	if len(list) == 0 {
		fmt.Fprintln(out, "no subagents")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPRESET\tMODEL\tSTATUS\tTURN\tTASK")
	for _, s := range list {
		t := s.Latest()
		d := "-"
		if t.Duration() > 0 {
			d = tui.FormatDuration(t.Duration())
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Preset, s.Model, t.Status, d, tui.FirstLine(s.Task))
	}
	return tw.Flush()
}

// ExitCode makes atto exit with this status after what the command
// printed, and print nothing more.
type ExitCode int

func (e ExitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// RunAgentTurn is the hidden entry point of a subagent's turn (atto
// _agent-turn -session <parent> <name> <turn>), started by atto agent as
// a job of the parent session. It waits for a slot, runs the turn as
// atto -p resuming the subagent's session, records how it went and tells
// the parent. It exits 0 once it has told the parent, however the turn
// went.
func RunAgentTurn(args []string, _ io.Writer) error {
	fs := newFlags("_agent-turn")
	parent := fs.String("session", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 2 || *parent == "" {
		return fmt.Errorf("usage: atto _agent-turn -session <parent> <name> <turn> (started by atto agent)")
	}
	if config.InSubagent() {
		return fmt.Errorf("a subagent can't run subagent turns (%s is set)", config.EnvSubagent)
	}
	name := fs.Arg(0)
	st, err := subagent.Load(*parent, name)
	if err != nil {
		return err
	}
	if fmt.Sprint(st.Turns) != fs.Arg(1) {
		return fmt.Errorf("subagent %s is at turn %d, not %s", name, st.Turns, fs.Arg(1))
	}
	t := subagent.Turn{N: st.Turns, Status: subagent.Queued, Queued: time.Now()}
	_ = subagent.SaveTurn(*parent, name, t)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	release, err := subagent.Acquire(ctx, *parent, func() int {
		s, _ := config.LoadSettings()
		return s.SubagentLimit()
	})
	stop()
	if err != nil {
		return err
	}
	defer release()
	t.Status, t.Started = subagent.Running, time.Now()
	_ = subagent.SaveTurn(*parent, name, t)

	var res printResult
	runErr := RunPrint(PrintOptions{
		Prompt: st.Prompt, Model: st.Model, Effort: st.Effort, Resume: st.Session, Format: "text", Verbose: true,
		Subagent: &agent.Subagent{Name: name, Preset: st.Preset, Instructions: st.Instructions},
		done:     func(r printResult) { res = r },
	})
	t.Ended = time.Now()
	t.PromptTokens, t.CachedTokens, t.OutputTokens = res.Usage.InputTokens, res.Usage.CachedInputTokens, res.Usage.OutputTokens
	t.Cost, t.Steps = res.cost, res.NumSteps
	t.Status = subagent.Done
	switch {
	case res.Error != "":
		t.Status, t.Error = subagent.Failed, res.Error
	case runErr != nil && !errors.Is(runErr, ErrPrintFailed):
		t.Status, t.Error = subagent.Failed, runErr.Error()
	}
	if err := subagent.SaveTurn(*parent, name, t); err != nil {
		return err
	}
	return events.Push(*parent, turnEvent(st, t))
}

// turnEvent tells the parent session that a subagent's turn ended.
func turnEvent(st subagent.State, t subagent.Turn) events.Event {
	what := "finished"
	if t.Status == subagent.Failed {
		what = "failed"
	}
	detail := tui.FormatDuration(t.Duration())
	if n := t.PromptTokens + t.OutputTokens; n > 0 {
		detail += ", " + tui.FormatTokens(n) + " tokens"
	}
	text := fmt.Sprintf("Subagent %s %s turn %d (%s).", st.Name, what, t.N, detail)
	if t.Error != "" {
		text += " Error: " + t.Error
	}
	text += fmt.Sprintf("\nRead its report with: atto agent report %s", st.Name)
	return events.Event{Source: "agent", Text: text, Title: fmt.Sprintf("◆ subagent %s %s after %s", st.Name, what, tui.FormatDuration(t.Duration()))}
}
