package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/tui"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/trust"
)

// PrintOptions configures a non-interactive run (atto -p).
type PrintOptions struct {
	Prompt string
	// Images go with the prompt (the first turn of a goal); the prompt
	// holds their placeholders (see ReadPromptInput).
	Images   []provider.Image
	Model    string // provider/id; default model if empty
	Effort   string // default effort if empty
	Format   string // "text" (default), "json" or "stream-json"
	Partial  bool   // stream-json: also emit text/reasoning deltas
	Verbose  bool   // text: show tool activity on stderr
	MaxSteps int    // stop after this many model calls (0: unlimited)
	Resume   string // continue the session with this ID
	NoSave   bool   // do not record the run as a session
	// Goal keeps running turns until the objective is done (or blocked,
	// or failing).
	Goal string
	// Background is atto _continue (experimental): continue the session
	// -session names without a new message, holding its lock, and notify
	// the Notification hook when done.
	Background bool
	// Worker runs a turn of an agent (atto _agent-turn): its prompt
	// says so, its commands can't start agents, and what arrives in the
	// session's inbox (the parent's steers, its jobs' events) is delivered
	// while it works, and in another turn after it.
	Worker *agent.Worker
	// done, if set, receives the result before RunPrint returns.
	done func(printResult)
	// turnContext carries the worker control request; plain -p uses signals.
	turnContext context.Context
}

// printResult is the final JSON object for --output-format json and the
// last line of stream-json.
type printResult struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"` // "success" or "error"
	IsError    bool   `json:"is_error"`
	Result     string `json:"result"`
	Error      string `json:"error,omitempty"`
	SessionID  string `json:"session_id"`
	Model      string `json:"model"`
	NumSteps   int    `json:"num_steps"`
	GoalStatus string `json:"goal_status,omitempty"`
	GoalNote   string `json:"goal_note,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Usage      struct {
		InputTokens       int `json:"input_tokens"`
		CachedInputTokens int `json:"cached_input_tokens"`
		OutputTokens      int `json:"output_tokens"`
	} `json:"usage"`
	cost float64 // US dollars, 0 when the model has no prices
}

// ErrPrintFailed signals a non-zero exit after output was already written.
var ErrPrintFailed = errors.New("run failed")

// ReadPromptInput combines the prompt arguments with piped stdin, as
// `cat file | atto -p "explain this"` does, and reads the images to attach:
// the files given with -image, then stdin itself when it is an image
// (`pngpaste - | atto -p "what is this?"`). With a prompt argument, stdin
// is only read if data shows up promptly: scripts and agents often leave
// an idle pipe open, which would otherwise block forever. The images'
// placeholders ("[image 1: 640x480 PNG]") are added to the text, as the
// TUI's editor has them.
func ReadPromptInput(args, imagePaths []string) (string, []provider.Image, error) {
	prompt := strings.TrimSpace(strings.Join(args, " "))
	var imgs []provider.Image
	for _, path := range imagePaths {
		im, err := images.ReadFile(path)
		if err != nil {
			return "", nil, fmt.Errorf("-image %s: %w", path, err)
		}
		imgs = append(imgs, im)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		wait := time.Duration(0) // no prompt: stdin is the prompt, wait for it
		if prompt != "" || len(imgs) > 0 {
			wait = time.Second
		}
		in, err := readStdinBytes(os.Stdin, wait)
		if err != nil {
			return "", nil, err
		}
		if images.Sniff(in) {
			im, err := images.Prepare(in)
			if err != nil {
				return "", nil, fmt.Errorf("image on stdin: %w", err)
			}
			imgs = append(imgs, im)
		} else if text := strings.TrimSpace(string(in)); text != "" {
			if prompt == "" {
				prompt = text
			} else {
				prompt += "\n\n" + text
			}
		}
	}
	if prompt == "" && len(imgs) == 0 {
		return "", nil, fmt.Errorf("no prompt: pass it as an argument or on stdin")
	}
	return images.WithPlaceholders(prompt, imgs), imgs, nil
}

// readStdin is readStdinBytes as text.
func readStdin(r io.Reader, wait time.Duration) (string, error) {
	b, err := readStdinBytes(r, wait)
	return string(b), err
}

// readStdinBytes reads r to EOF (at most images.MaxFileBytes after the
// first read). If wait > 0 and no data arrives within it, it gives up and
// returns nothing.
func readStdinBytes(r io.Reader, wait time.Duration) ([]byte, error) {
	type chunk struct {
		b   []byte
		err error
	}
	first := make(chan chunk, 1)
	go func() {
		buf := make([]byte, 64*1024)
		n, err := r.Read(buf)
		first <- chunk{buf[:n], err}
	}()
	var c chunk
	if wait > 0 {
		select {
		case c = <-first:
		case <-time.After(wait):
			return nil, nil
		}
	} else {
		c = <-first
	}
	if c.err != nil {
		if c.err == io.EOF {
			return c.b, nil
		}
		return nil, c.err
	}
	rest, err := io.ReadAll(io.LimitReader(r, images.MaxFileBytes))
	return append(c.b, rest...), err
}

// RunPrint runs one prompt without the TUI. Assistant text goes to stdout;
// in text mode tool activity goes to stderr with -v.
func RunPrint(o PrintOptions) error {
	if w, ok, err := workerFor(o); err != nil {
		return err
	} else if ok {
		return printViaWorker(o, w, os.Stdout, os.Stderr)
	}
	settings, models, err := core.Load()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Session: new or by ID (-session).
	var saved core.Saved
	var sess *session.Writer
	start, source := time.Now(), "startup"
	switch {
	case o.Resume != "":
		path, err := session.Find(o.Resume)
		if err != nil {
			return err
		}
		path, err = session.RestoreForWrite(path)
		if err != nil {
			return err
		}
		release, err := lockForRun(path, o.Background, !o.NoSave)
		if err != nil {
			return err
		}
		defer release()
		if saved, sess, err = core.Open(path); err != nil {
			return err
		}
		start, source = saved.Header.Time, "resume"
	default:
		sess = session.New(cwd)
		release, err := lockForRun(sess.Path, o.Background, !o.NoSave)
		if err != nil {
			return err
		}
		defer release()
	}
	defer sess.Close()

	// Model and effort: flags, else what the session last used, else the
	// defaults.
	model, modelFrom, err := core.PickModelFrom(models, settings, o.Model, saved.Model)
	if err != nil {
		return err
	}
	effort, effortFrom := core.EffortFrom(settings, o.Effort, saved.Effort)
	if err := core.CheckEffort(model, effort); err != nil {
		return err
	}

	if len(o.Images) > 0 {
		if !model.Model.Images() {
			return errors.New(images.Unsupported(model.Model.DisplayName(), "-m", config.ModelsPath()))
		}
		if !o.NoSave { // the session refers to them by file
			for _, im := range o.Images {
				if err := images.Save(im); err != nil {
					return fmt.Errorf("saving image: %w", err)
				}
			}
		}
	}

	trust.WarnProject(os.Stderr, cwd)
	ag, hk, hookSrc, err := core.NewAgentSources(cwd, model, effort)
	if err != nil {
		return err
	}
	ag.MaxSteps = o.MaxSteps
	ag.Worker = o.Worker
	// Extensions have no UI here: notices go to stderr, dialogs get their
	// default answers, and sendMessage steers the run.
	ext := core.LoadExtensions(ag, &extensions.Headless{Out: os.Stderr, Send: ag.Steer})
	defer ext.Close()
	mc := core.LoadMCP(ag) // servers start on first use and end with the run
	defer mc.Close()
	core.Bind(ag, hk, sess, start, !o.NoSave)
	if o.Worker != nil {
		ag.SetSession(sess.ID, append(core.Env(sess.ID), config.EnvLegacyAgent+"=1"))
	}
	ext.SessionStart(source)
	if hk != nil {
		for _, n := range hk.SessionStart(context.Background(), source) {
			fmt.Fprintln(os.Stderr, n)
		}
	}
	if notice := config.PriceTierNotice(model); notice != "" {
		fmt.Fprintln(os.Stderr, "atto: "+notice)
	}
	ag.Restore(saved.Branch())
	ag.SetLongContext(saved.LongContext)
	if !o.NoSave && !o.Background {
		sess.Append(session.Entry{Type: session.TypeModel, Provider: model.ProviderName, Model: model.Model.ID})
		sess.Append(session.Entry{Type: session.TypeEffort, Effort: ag.Effort()})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if o.turnContext != nil {
		ctx = o.turnContext
	}

	res := printResult{Type: "result", SessionID: sess.ID, Model: model.ProviderName + "/" + model.Model.ID}
	p := &printer{format: o.Format, partial: o.Partial, verbose: o.Verbose, out: os.Stdout, errOut: os.Stderr, res: &res}
	loaded := core.Collect(ag, hookSrc, modelFrom, effortFrom)
	p.emit(map[string]any{"type": "init", "session_id": sess.ID, "model": res.Model, "effort": ag.Effort(), "cwd": cwd, "context": loaded})
	if o.Verbose && p.textMode() {
		fmt.Fprintln(os.Stderr, "◇ Loaded")
		for _, line := range core.FormatRows(loaded.Summary(), "  ", 12) {
			fmt.Fprintln(os.Stderr, line)
		}
	}

	// The goal lives here; from the file only the model's complete/blocked
	// report is taken (see goal.Adopt). Its snapshot goes into the session
	// once, at the end.
	d := core.GoalDriver{Session: sess.ID, Steer: ag.Steer,
		Retrying: func(r core.Retry) { fmt.Fprintln(os.Stderr, "atto: "+r.Notice()) }}
	if o.Goal != "" {
		g, err := goal.New(o.Goal)
		if err != nil {
			return err
		}
		if err := goal.Save(sess.ID, g); err != nil {
			return err
		}
		d.Goal = g
	}

	mode := bgResumeTurn
	if o.Background {
		if mode = bgPrepare(&d, saved); mode == bgNothing {
			fmt.Fprintln(os.Stderr, "atto: nothing to continue")
			return nil
		}
	}
	if d.Active() && ag.LongContext() {
		fmt.Fprintln(os.Stderr, "atto: "+goal.LongContextNotice)
	}
	began := time.Now()
	input := o.Prompt
	if input == "" && d.Goal != nil {
		input = d.Goal.Continuation()
	}
	imgs := o.Images // with the first turn only
	if o.Worker != nil {
		// Messages that came before the turn go with its prompt.
		if text := inboxText(sess.ID); text != "" {
			input += "\n\n" + text
		}
	}
	// Later ones (an agent's messages and final answers, jobs ending) are
	// taken at each step boundary. Quiet messages don't keep a finished
	// turn going, and a plain run's finished turn takes nothing: what is
	// left waits in the inbox for the session's next turn.
	var turns core.TurnRunner[string]
	turns.BoundaryInbox(ag, sess.ID, o.Worker != nil)

	resume := o.Background && mode == bgResumeTurn // the first turn has its message already
	turn := func(parent context.Context, input string, emit func(any)) error {
		ctx, err := turns.Begin(parent)
		if err != nil {
			return err
		}
		defer turns.End()
		request := core.TurnRequest{Text: input, Images: imgs, Continue: resume}
		if resume {
			resume = false
		} else {
			emit(transcript.Input{Text: input, Images: imgs})
		}
		err = turns.Run(ctx, ag, request, emit)
		if !request.Continue {
			imgs = nil
		}
		p.tr.End()
		return err
	}
	runErr := d.Run(ctx, input, turn, p.event, func() {
		p.flushStep()
		if o.Verbose && (o.Format == "" || o.Format == "text") {
			fmt.Fprintf(os.Stderr, "\n◎ continuing goal · turn %d · %s\n", d.Goal.Turns+1, d.Goal.Usage())
		}
	})
	// What arrived as the turn ended gets a turn of its own.
	for o.Worker != nil && runErr == nil && ctx.Err() == nil {
		_, evs := events.SplitReload(core.Poll(sess.ID))
		turns.PendingEvents = append(turns.PendingEvents, evs...)
		delivery := turns.DeliverEvents(sess.ID, false, true)
		if !delivery.Start {
			break
		}
		p.flushStep()
		runErr = turn(ctx, delivery.Text, p.event)
	}
	if g := d.Goal; g != nil {
		res.GoalStatus, res.GoalNote = string(g.Status), g.Note
		if !o.NoSave { // so resuming the session shows the goal
			raw, _ := json.Marshal(g)
			sess.Append(session.Entry{Type: session.TypeGoal, Goal: raw})
		}
		if g.Status != goal.Complete && runErr == nil {
			runErr = fmt.Errorf("goal %s: %s", g.Status.Label(), g.Note)
		}
	}
	leave := core.Leave
	if o.Worker != nil {
		leave = core.LeaveKeepingAgents // the turns of its own agents go on
	}
	if n := leave(sess.ID); n > 0 { // jobs end with the run
		fmt.Fprintf(os.Stderr, "atto: stopped %d background job(s)\n", n)
	}
	if hk != nil { // the run is the whole session
		for _, n := range hk.SessionEnd(context.Background(), "other") {
			fmt.Fprintln(os.Stderr, n)
		}
	}
	ext.SessionEnd("other")
	p.flushStep()
	res.DurationMs = time.Since(began).Milliseconds()
	res.Result = strings.TrimSpace(p.lastText)
	res.Subtype = "success"
	if runErr != nil {
		res.Subtype, res.IsError, res.Error = "error", true, runErr.Error()
	}

	if o.done != nil {
		o.done(res)
	}
	if o.Background {
		name := saved.Name
		if name == "" {
			name = sess.ID
		}
		bgNotify(hk, name, runErr)
	}

	switch o.Format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	case "stream-json":
		p.emit(res)
	default:
		if p.wroteText {
			fmt.Fprintln(os.Stdout)
		}
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "atto:", runErr)
		}
	}
	if runErr != nil {
		return ErrPrintFailed
	}
	return nil
}

// printer renders a run for one output format, from the items the
// transcript builder makes of the agent's events.
type printer struct {
	format  string
	partial bool
	verbose bool
	out     io.Writer
	errOut  io.Writer
	res     *printResult

	tr transcript.Builder
	// Current step, flushed as one "assistant" event in stream-json.
	text, reasoning strings.Builder
	lastText        string
	wroteText       bool
}

func (p *printer) emit(v any) {
	if p.format != "stream-json" {
		return
	}
	b, _ := json.Marshal(v)
	fmt.Fprintf(p.out, "%s\n", b)
}

func (p *printer) textMode() bool { return p.format == "" || p.format == "text" }

func (p *printer) flushStep() {
	if p.text.Len() == 0 && p.reasoning.Len() == 0 {
		return
	}
	if t := strings.TrimSpace(p.text.String()); t != "" {
		p.lastText = t
	}
	p.emit(map[string]any{"type": "assistant", "text": p.text.String(), "reasoning": p.reasoning.String()})
	p.text.Reset()
	p.reasoning.Reset()
}

// event passes an agent event to the builder; a step's end flushes it and
// counts its usage.
func (p *printer) event(ev any) {
	if p.tr.Handler.Started == nil {
		p.tr.Handler = transcript.Handler{Started: p.started, Delta: p.delta, Updated: p.updated, Completed: p.completed}
	}
	p.tr.Event(ev)
	if e, ok := ev.(agent.StepEnd); ok {
		p.flushStep()
		p.res.NumSteps++
		addUsage(p.res, e.Usage)
	}
}

func (p *printer) started(it *transcript.Item) {
	switch it.Kind {
	case transcript.Tool:
		if !it.Pending { // a call the model is writing is announced when it runs
			p.toolUse(it)
		}
	case transcript.Hook:
		p.emit(map[string]any{"type": "hook", "event": it.HookEvent, "message": it.Text, "blocked": it.Blocked})
		if p.textMode() {
			fmt.Fprintf(p.errOut, "⚑ %s: %s\n", it.HookEvent, it.Text)
		}
	}
}

// updated announces a call the model wrote once it is complete: tool_use
// carries the final arguments, one per call.
func (p *printer) updated(it *transcript.Item) {
	if it.Kind == transcript.Tool && !it.Pending {
		p.toolUse(it)
	}
}

func (p *printer) toolUse(it *transcript.Item) {
	p.flushStep()
	p.emit(map[string]any{"type": "tool_use", "id": it.CallID, "description": it.Description, "command": it.Command})
	if p.verbose && p.textMode() {
		fmt.Fprintf(p.errOut, "\n● %s  $ %s\n", it.Description, tui.FirstLineWithEllipsis(it.Command))
	}
}

func (p *printer) delta(it *transcript.Item, d string) {
	switch it.Kind {
	case transcript.Reasoning:
		p.reasoning.WriteString(d)
		if p.partial {
			p.emit(map[string]any{"type": "delta", "kind": "reasoning", "text": d})
		}
	case transcript.Assistant:
		p.text.WriteString(d)
		if p.partial {
			p.emit(map[string]any{"type": "delta", "kind": "text", "text": d})
		}
		if p.textMode() {
			fmt.Fprint(p.out, d)
			p.wroteText = true
		}
	}
}

func (p *printer) completed(it *transcript.Item) {
	switch it.Kind {
	case transcript.Tool:
		r := it.Result
		if r == nil || it.CallID == "" { // a call the model wrote that never ran
			return
		}
		out := r.Text
		if len(out) > 4000 {
			out = out[:4000] + "\n[truncated]"
		}
		ev := map[string]any{
			"type": "tool_result", "id": it.CallID, "description": it.Description, "exit_code": r.ExitCode,
			"timed_out": r.TimedOut, "duration_ms": it.Duration.Milliseconds(), "output": out,
		}
		if r.Job > 0 {
			ev["background_job"] = r.Job
		}
		p.emit(ev)
		if p.verbose && p.textMode() {
			status := fmt.Sprintf("exit %d", r.ExitCode)
			switch {
			case r.TimedOut:
				status = "timed out"
			case r.Job > 0:
				status = fmt.Sprintf("background job %d", r.Job)
			}
			fmt.Fprintf(p.errOut, "  └ %s · %s\n", status, tui.FormatDuration(it.Duration))
		}
	case transcript.Compaction:
		if it.Status == transcript.Completed {
			p.emit(map[string]any{"type": "compaction", "tokens_before": it.TokensBefore, "tokens_after": it.TokensAfter})
		}
	}
}

func addUsage(res *printResult, u provider.Usage) {
	res.Usage.InputTokens += u.PromptTokens
	res.Usage.CachedInputTokens += u.CachedTokens
	res.Usage.OutputTokens += u.CompletionTokens
	res.cost += u.Cost
}

// inboxText takes the events waiting in the session's inbox, as one
// message for the model ("" for none). Reload requests are dropped: a
// print run reads its configuration once.
func inboxText(id string) string {
	_, evs := events.SplitReload(core.Poll(id))
	if len(evs) == 0 {
		return ""
	}
	return events.Format(evs)
}
