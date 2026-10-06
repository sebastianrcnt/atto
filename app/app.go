// Package app is the interactive terminal front end: it wires the agent's
// events into TUI components and handles input and slash commands.
package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
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
	"github.com/sebastianrcnt/atto/tui"
	"github.com/sebastianrcnt/atto/update"
)

// Version is the release this binary was built from (see update.Current).
var Version = update.Current()

type Options struct {
	Inline   bool   // render inline instead of fullscreen
	Continue bool   // resume the latest session in this directory
	Resume   bool   // open the resume picker at startup
	Model    string // provider/id to use instead of the default
	Effort   string // effort to use instead of the default
	Session  string // resume the session with this ID
	Prompt   string // first message, submitted once the UI is up (atto "fix the build")
}

// modal is a picker shown in place of the editor.
type modal interface {
	tui.Component
	tui.InputHandler
}

type App struct {
	ui     *tui.TUI
	models config.ModelsFile
	agent  *agent.Agent
	sess   *session.Writer
	unlock func()        // releases this terminal's lock on sess
	hooks  *hooks.Runner // nil when no hooks are configured
	// ext runs the session's extensions (nil in tests that need none);
	// extUI is what they show.
	ext *extensions.Manager
	mcp *mcp.Manager
	// mcpAsked are the approval prompts shown this run (name#hash).
	mcpAsked map[string]bool
	extUI    extensions.UIState // touched on the UI goroutine only
	// hookSrc are the settings files the hooks came from, loaded what the
	// session loaded (the "Loaded" block), and modelFrom/effortFrom where
	// the model and effort in use came from.
	hookSrc               []config.HookSource
	loaded                core.Loaded
	modelFrom, effortFrom core.Origin

	editor *tui.Editor
	modal  modal
	// login replaces the browser, clipboard and device ID in tests.
	login loginHooks
	// clipboard reads an image for Ctrl+V / Alt+V.
	clipboard func(context.Context) (provider.Image, error)
	// copyEnv writes copied text (the zero value is the real system).
	copyEnv copyEnv
	toast   toast

	busy     bool
	runKind  string // "turn", "compact" or "branchSummary" while busy
	cancel   context.CancelFunc
	runStart time.Time
	activity string
	// Activity line state (activity.go): the turn's verb, the setting it
	// comes from, when the run last sent an event and how many commands
	// run; now and verbRand are replaced by tests.
	turnVerb     string
	spinnerVerbs string
	spinnerScan  bool // settings.json spinnerScanner
	// turnOut counts the run's output tokens: the finished model calls'
	// usage, plus streamChars (text and thinking so far of the call in
	// progress) at about four characters a token.
	turnOut, streamChars int
	// draftChars is how much of each tool call (by index) the call in
	// progress has written.
	draftChars map[int]int
	// turnIn counts the run's input tokens the server had not cached
	// (as the status line's ↑): what each call added to the context.
	turnIn       int
	lastEvent    time.Time
	toolsRunning int
	now          func() time.Time
	verbRand     *rand.Rand
	// ctxTokens mirrors the agent's context estimate; updated from events so
	// rendering never reads agent state while a turn runs.
	ctxTokens int
	usage     usageStats

	// Codex-style pending input: Enter during a turn steers it (delivered
	// after the next tool call); Tab queues a follow-up turn.
	pendingSteers            []string
	queued                   []queuedInput
	sendSteersAfterInterrupt bool
	sendNow                  *queuedInput // Ctrl+Enter's message, sent once the turn it interrupted ends
	queuePaused              bool

	// items makes the transcript's items from agent events and session
	// entries (see items.go); these are the blocks of the items being
	// streamed, tools by item ID.
	items    transcript.Builder
	thinking *thinkingBlock
	text     *textBlock
	tools    map[string]*toolBlock
	compact  *compactBlock
	// summaryBlk is the branch summary block being streamed.
	summaryBlk *summaryBlock
	// shell is the command the user is running with "!", shellBlk the
	// block of the latest one, and pendingShell those that finished during
	// a run (see usershell.go).
	shell        *shellRun
	shellBlk     *shellBlock
	pendingShell []pendingShell
	// steered collects the user messages of a committed steer, shown as
	// one block; replaying is set while blocks come from saved entries.
	steered   []string
	replaying bool

	// details expands every collapsible block (ctrl+t); origView shows the
	// original text of blocks an extension replaced (ctrl+o).
	details  details
	origView details
	// itemBlocks are the blocks of streamed reasoning and assistant items
	// by item ID, until they are saved; blocks are those saved, by block ID
	// (see blockdisplay.go).
	itemBlocks map[string]displayBlock
	blocks     map[string]displayBlock
	// Last model/effort written to the session, to record changes.
	recModel, recEffort string
	sessName            string
	// pendingResume is a session to switch to once the running turn stops.
	pendingResume string
	// pendingTree is a /tree entry to move to once the running turn stops,
	// and pendingSummary how to summarize the branch left (nil: no summary).
	pendingTree    string
	pendingSummary *summaryRequest
	// summary is the branch summary being written (runKind "branchSummary"),
	// and skipSummary settings.json's branchSummary.skipPrompt.
	summary     *summaryRun
	skipSummary bool
	// noToolGroups is settings.json's "toolGroups": false (see toolRun).
	noToolGroups bool
	// esc detects Esc twice on an empty prompt; escAction is what it opens.
	esc       doubleEsc
	escAction string

	// Inbox: events waiting for delivery, and counts for the status line.
	pendingEvents        []events.Event
	jobCount, timerCount int

	goal core.GoalDriver
	// bgx is the experimental exit menu (background_exit.go).
	bgx bgExit

	// Slash command list: selection, the text it belongs to, and the text
	// for which Esc closed it.
	sugList      *tui.SelectList
	sugDismissed string
	sugGen       int      // cmds.gen the list was built from
	cmds         cmdCache // see allCommands
	statusRows   statusCache
	// mention is the "@" file list (mention.go).
	mention mentions

	// Status line state.
	gitBranch   string
	statusCmd   bool     // a custom statusLine command is configured
	statusLines []string // its latest output
	statusWake  chan struct{}

	// remote is the /remote server while it runs (remote.go); remoteHost
	// and remotePort override where it listens (tests). fromRemote is set
	// while input from it is submitted, and remoteSteers are its steers
	// not yet delivered: their user messages get a "from remote" mark.
	remote       *remote
	remoteHost   string
	remotePort   *int
	fromRemote   bool
	remoteSteers map[string]int
	// prompt is the open modal as /remote's clients see it
	// (remoteprompt.go), whether or not /remote is on; promptHow and
	// promptByRemote say how it is being closed.
	prompt         *openPrompt
	promptSeq      int
	promptHow      string
	promptByRemote bool

	cwd      string
	quit     chan struct{}
	quitOnce sync.Once
}

func Run(opts Options) error {
	settings, models, err := core.Load()
	if err != nil {
		return err
	}
	model, modelFrom, err := core.PickModelFrom(models, settings, opts.Model, "")
	noModels := errors.Is(err, core.ErrNoModels)
	if err != nil && !noModels {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	effort, effortFrom := core.EffortFrom(settings, opts.Effort, "")
	ag, hk, hookSrc, err := core.NewAgentSources(cwd, model, effort)
	if err != nil {
		return err
	}

	a := &App{
		ui:         tui.New(tui.NewProcessTerminal()),
		models:     models,
		agent:      ag,
		hooks:      hk,
		hookSrc:    hookSrc,
		modelFrom:  modelFrom,
		effortFrom: effortFrom,
		tools:      map[string]*toolBlock{},
		cwd:        cwd,
		quit:       make(chan struct{}),

		clipboard: images.SystemClipboardImage,
	}
	ag.SteerNote = a.goal.SteerNote // a message sent while the goal runs says so
	if opts.Inline || rendererMode(settings.Renderer) == tui.Inline {
		a.ui.Mode = tui.Inline
	}
	a.escAction = settings.DoubleEscapeAction
	a.spinnerVerbs, a.spinnerScan = settings.SpinnerVerbs, settings.SpinnerScanner
	a.bgx.off = settings.BackgroundExit != nil && !*settings.BackgroundExit
	a.skipSummary = settings.BranchSummary != nil && settings.BranchSummary.SkipPrompt
	a.ui.NoMouse = mouseDisabled(settings.Mouse, os.Getenv)
	a.noToolGroups = settings.ToolGroups != nil && !*settings.ToolGroups
	a.build()
	a.ext = core.LoadExtensions(ag, newTUIHost(a))
	a.mcp = core.LoadMCP(ag)
	if noModels {
		// First run: start anyway and say how to get a model, like pi.
		a.notice("%s", core.NoModelsHint())
	}
	a.newSession("") // its "Loaded" block lists skill files that were skipped
	a.sessionStartHook("startup")
	a.statusCmd = settings.StatusLine != nil && settings.StatusLine.Command != ""
	a.startStatusLine(settings.StatusLine)
	if config.CatalogStale() {
		go a.refreshCatalog()
	}

	switch {
	case opts.Session != "":
		if path, err := session.Find(opts.Session); err == nil {
			a.resume(path)
		} else {
			a.errorNotice(err)
		}
	case opts.Continue:
		if s, ok := session.Latest(cwd); ok {
			a.resume(s.Path)
		} else {
			a.notice("No previous session in this directory.")
		}
	case opts.Resume:
		a.cmdResume("")
	}

	if err := a.ui.Start(); err != nil {
		return err
	}
	if opts.Prompt != "" {
		// As if typed: goes through submit, so a leading "/" is a command too.
		a.ui.Do(func() { a.submit(opts.Prompt, nil) })
	}
	a.ui.Do(a.askMCPApprovals)
	go a.watchInbox()
	if a.ui.Mode == tui.Fullscreen && !a.ui.NoMouse {
		go func() {
			if tmuxMouseOff(os.Getenv, runTmux) {
				a.ui.Do(func() { a.notice("%s", tmuxMouseHint) })
			}
		}()
	}
	if settings.UpdateCheck == nil || *settings.UpdateCheck {
		go a.checkUpdate()
	}
	<-a.quit
	a.ui.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		a.stopRemote()
	})
	a.ui.Stop()
	a.closeSession()
	if a.printExit() { // the run goes on in the background
		if a.ext != nil {
			a.ext.Close() // the session's extensions run on there; no session_end
		}
		_ = a.mcp.Close()
		return nil
	}
	if n := a.leaveCore(); n > 0 {
		fmt.Printf("atto: stopped %d background job(s)\n", n)
	}
	if a.hooks != nil {
		for _, n := range a.hooks.SessionEnd(context.Background(), "exit") {
			fmt.Fprintln(os.Stderr, n)
		}
	}
	if a.ext != nil {
		a.ext.SessionEnd("exit")
		a.ext.Close()
	}
	_ = a.mcp.Close()
	return nil
}

func (a *App) build() {
	a.editor = tui.NewEditor(tui.FG(6, "› "))
	a.editor.Rule = tui.Dim
	a.editor.OnSubmit = a.submit
	a.editor.OnSendNow = a.sendNowFromEditor
	a.editor.OnPaste = a.pasteImagePath

	// The command list sits above the input, as in Claude Code, so the
	// input and the status line keep their place as it opens and closes.
	a.ui.Footer.Add(tui.Func(a.renderActivity), tui.Func(a.renderPending), jumpPill{a}, tui.Func(a.renderReadOnly), tui.Func(a.renderWidgets), tui.Func(a.renderSuggestions), tui.Func(a.renderInput), tui.Func(a.renderStatus))
	a.ui.SetFocus(a.editor)
	a.ui.OnInput = a.onInput
	a.ui.OnCopy = a.copySelection
	a.ui.PaddingX = 1
	a.ui.GapY = 1
	a.ui.Pin = a.pinnedPrompt
	a.addHeader()
}

// leaveSession ends the session being left (/clear, /resume): its
// SessionEnd hooks run with reason, and like codex, its background
// processes stop, as they belong to their session.
func (a *App) leaveSession(reason string) {
	if a.sess == nil {
		return
	}
	a.sessionEndHook(reason)
	if n := a.leaveCore(); n > 0 {
		a.notice("Stopped %d background job(s) of the previous conversation.", n)
	}
	a.jobCount, a.timerCount, a.pendingEvents = 0, 0, nil
	a.dropShell()
}

// lockSession holds the open session for this terminal, so another atto
// does not open it as well: two processes writing one session (and its
// goal) undo each other's work.
func (a *App) lockSession() {
	release, err := session.LockTUI(a.sess.Path)
	if err != nil {
		a.errorNotice(err)
		return
	}
	a.unlock = release
}

// closeSession closes the session file and releases this terminal's lock.
func (a *App) closeSession() {
	a.sess.Close()
	if a.unlock != nil {
		a.unlock()
		a.unlock = nil
	}
}

// newSession starts recording into a fresh session file.
func (a *App) newSession(reason string) {
	a.leaveSession(reason)
	a.closeSession()
	a.sess = session.New(a.cwd)
	a.lockSession()
	a.items.IDPrefix = a.sess.ID + "-i"
	core.Bind(a.agent, a.hooks, a.sess, time.Now(), true)
	a.setLiveSession(a.sess.ID)
	a.resetGoal()
	a.recModel, a.recEffort, a.sessName = "", "", ""
	a.showLoaded()
	a.statusTrigger()
	a.remoteSwitched()
}

// sessionEndHook runs SessionEnd hooks and waits for them (they are bounded
// by a short timeout): the hooks must see the session being left, which the
// next Bind replaces.
func (a *App) sessionEndHook(reason string) {
	if a.ext != nil {
		a.ext.SessionEnd(reason)
	}
	if a.hooks == nil {
		return
	}
	for _, n := range a.hooks.SessionEnd(context.Background(), reason) {
		a.notice("%s", n)
	}
}

// notifyAfter is how long a turn must have run for atto to notify the user
// when it finishes; shorter turns end while the user is likely still looking.
var notifyAfter = 15 * time.Second

// notify runs Notification hooks in the background, for when atto needs
// the user's attention (kind is the notification type).
func (a *App) notify(kind, message string) {
	hk := a.hooks // /reload may replace a.hooks while this runs
	if hk == nil {
		return
	}
	go func() {
		notices := hk.Notification(context.Background(), kind, message)
		a.ui.Do(func() {
			for _, n := range notices {
				a.notice("%s", n)
			}
		})
	}()
}

// sessionStartHook runs SessionStart hooks in the background.
func (a *App) sessionStartHook(source string) {
	if a.ext != nil {
		a.ext.SessionStart(source)
	}
	hk := a.hooks // /reload may replace a.hooks while this runs
	if hk == nil {
		return
	}
	go func() {
		notices := hk.SessionStart(context.Background(), source)
		a.ui.Do(func() {
			for _, n := range notices {
				a.notice("%s", n)
			}
		})
	}()
}

func (a *App) model() config.ModelRef {
	m, _ := a.agent.Current()
	return m
}

func (a *App) effort() string {
	_, e := a.agent.Current()
	return e
}

func (a *App) addHeader() {
	a.ui.Body.Add(tui.Func(func(width int) []string {
		return []string{
			tui.Truncate(tui.Bold("atto")+tui.Dim(" "+Version+"  ·  "+a.headerModel()), width, "…"),
			tui.Truncate(tui.Dim("/ commands · enter steer · tab queue · shift+tab effort · ctrl+t details · esc interrupt · esc esc go back"), width, "…"),
		}
	}))
}

func (a *App) headerModel() string {
	if m := a.model(); m.Model.ID != "" {
		return a.models.DisplayName(m)
	}
	return "no model (/login)"
}

func (a *App) add(c tui.Component) { a.ui.Body.Add(gap{c}) }

func (a *App) notice(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	a.add(&noticeBlock{text: text, style: tui.Dim})
	a.remoteNotice(text)
}

func (a *App) errorNotice(err error) {
	a.add(&noticeBlock{text: "Error: " + err.Error(), style: func(s string) string { return tui.FG(1, s) }})
	a.remoteNotice("Error: " + err.Error())
}

func (a *App) doQuit() { a.quitOnce.Do(func() { close(a.quit) }) }

// --- input ---

func (a *App) onInput(data string) bool {
	if a.modal != nil {
		return false // the focused modal handles everything
	}
	if a.readOnlyKey(data) {
		return true
	}
	if a.suggestionKey(tui.Key(data)) {
		a.esc.reset() // an Esc that closed the "/" list is not a first Esc
		return true
	}
	if tui.Key(data) != "escape" {
		a.esc.reset()
	}
	switch tui.Key(data) {
	case "shift+tab":
		a.cycleEffort()
		return true
	case "ctrl+t":
		a.details.on = !a.details.on
		a.details.gen++ // the expanded blocks are confirmation enough
		return true
	case "ctrl+o": // original text of the blocks an extension replaced
		a.origView.on = !a.origView.on
		a.origView.gen++
		return true
	case "escape":
		if a.interrupt() {
			a.esc.reset()
			return true
		}
		return a.onEscape()
	case "ctrl+c":
		switch {
		case a.cancelShell():
		case a.busy:
			a.cancel()
		case a.editor.Text() != "":
			a.editor.SetText("")
		default:
			a.requestQuit()
		}
		return true
	case "ctrl+d":
		if a.editor.Text() == "" && (!a.busy || a.exitMenuAvailable()) {
			a.requestQuit()
			return true
		}
	case "ctrl+b":
		// As in Claude Code: the running command moves to the background
		// and the turn goes on. Otherwise it is the editor's cursor-left.
		if a.busy && a.agent.Background() {
			return true
		}
	case "ctrl+l":
		a.ui.Redraw()
		return true
	case "ctrl+v":
		a.pasteClipboardImage()
		return true
	case "tab":
		if strings.TrimSpace(a.editor.Text()) != "" {
			a.queueFromEditor()
			return true
		}
	case "shift+left":
		if a.editLastSteer() {
			return true
		}
		if len(a.queued) > 0 {
			a.editLastQueued()
			return true
		}
	}
	if data == "\x1bv" { // alt+v: where the terminal keeps Ctrl+V for pasting text
		a.pasteClipboardImage()
		return true
	}
	return false
}

// interrupt is Esc while something runs: it stops a "!" command, else the
// turn (pending steers then go out at once). False when nothing runs.
func (a *App) interrupt() bool {
	if a.cancelShell() {
		return true
	}
	if !a.busy {
		return false
	}
	if len(a.pendingSteers) > 0 {
		a.sendSteersAfterInterrupt = true
	}
	a.cancel()
	return true
}

func (a *App) submit(text string, att []tui.Attachment) {
	if a.refuseReadOnly(text) {
		return
	}
	a.ui.ScrollToBottom()
	if cmd, exclude, ok := parseShell(text); ok && len(att) == 0 {
		a.submitShell(text, cmd, exclude)
		return
	}
	if len(att) > 0 && !strings.HasPrefix(text, "/") {
		a.submitWithImages(text, att)
		return
	}
	switch {
	case text == "":
		// Enter on an empty prompt resumes a paused queue, or else a goal
		// waiting for the user.
		switch {
		case a.busy:
		case len(a.queued) > 0:
			a.queuePaused = false
			a.maybeSendNextQueued()
		case a.goal.Held():
			a.goal.Release()
			a.remoteGoal()
			a.continueGoal()
		}
	case strings.HasPrefix(text, "/"):
		a.runCommand(text)
	case a.noModel():
		a.restoreToEditor([]string{text})
	case a.busy && a.runKind == "turn":
		a.steer(text)
	case a.busy:
		a.enqueue(text, nil)
	default:
		a.startTurn(text, nil)
	}
}

// recordSettings writes model/effort entries when they changed since the
// last one, so a resumed session picks them back up.
func (a *App) recordSettings() {
	m, e := a.agent.Current()
	if id := m.ProviderName + "/" + m.Model.ID; id != a.recModel {
		a.sess.Append(session.Entry{Type: session.TypeModel, Provider: m.ProviderName, Model: m.Model.ID})
		a.recModel = id
	}
	if e != a.recEffort {
		a.sess.Append(session.Entry{Type: session.TypeEffort, Effort: e})
		a.recEffort = e
	}
}

// startTurn runs a turn for text and its image attachments.
func (a *App) startTurn(text string, att []tui.Attachment) {
	if a.refuseReadOnly(text) {
		return
	}
	imgs := attachedImages(att)
	for _, im := range imgs {
		if err := images.Save(im); err != nil {
			a.errorNotice(fmt.Errorf("saving image: %w", err))
			a.restoreToEditor([]string{text}, att...)
			return
		}
	}
	a.tr().Event(transcript.Input{Text: text, Images: imgs})
	// A goal that is not running by itself says so, to this message only.
	a.agent.SetInputNote(a.goal.StateNote())
	a.goal.UserInput() // a turn the user started: the goal waits for them after it
	a.runKind = "turn"
	a.recordSettings()
	a.start("Thinking", func(ctx context.Context, emit func(any)) error {
		return a.agent.RunWithImages(ctx, text, imgs, emit)
	})
}

// start runs fn in the background, routing its events into the UI.
func (a *App) start(activity string, fn func(context.Context, func(any)) error) {
	ctx, cancel := context.WithCancel(context.Background())
	a.busy, a.cancel = true, cancel
	a.runStart, a.activity = a.clock(), activity
	a.lastEvent, a.toolsRunning = a.runStart, 0
	a.turnOut, a.streamChars, a.turnIn, a.draftChars = 0, 0, 0, nil
	a.turnVerb = a.pickVerb()
	if a.runKind == "turn" {
		a.goal.BeginTurn()
	}
	a.remoteTurnStarted()
	a.remoteGoal() // a goal on hold is pursued while the turn runs

	go func() { // keep the spinner and timers moving
		t := time.NewTicker(a.ui.AnimationInterval())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.ui.RequestRender()
			}
		}
	}()
	go func() {
		err := fn(ctx, func(ev any) { a.ui.Do(func() { a.onEvent(ev) }) })
		// A reload that no step boundary reached (the turn ended first, or
		// this was a compaction) runs now; its report for the model is
		// delivered like an event.
		var reported []events.Event
		for _, f := range a.agent.TakeBoundary() {
			if text := f(); text != "" {
				reported = append(reported, events.Event{Source: sourceReloaded, Title: "Reload result sent to the agent", Text: strings.TrimPrefix(text, events.Prefix)})
			}
		}
		ctxTokens := a.agent.ContextTokens() // safe: the run is over
		a.ui.Do(func() {
			a.pendingEvents = append(a.pendingEvents, reported...)
			a.tr().End() // a compaction that did not finish disappears
			a.busy = false
			a.ctxTokens = ctxTokens
			cancel()
			a.cancel = nil
			switch {
			case errors.Is(err, context.Canceled) && a.runKind == "branchSummary":
				a.notice("Branch summary canceled.")
			case errors.Is(err, context.Canceled):
				a.notice("Interrupted.")
			case errors.Is(err, agent.ErrPromptBlocked), errors.Is(err, agent.ErrStoppedByHook):
				// The hook's reason was already shown.
			case err != nil:
				a.errorNotice(err)
			}
			if werr := a.sess.Err(); werr != nil {
				a.errorNotice(fmt.Errorf("saving session: %w", werr))
			}
			a.statusTrigger()
			a.remoteTurnCompleted(err)
			a.afterRun(err)
			a.remoteGoal()
			a.remotePending()
		})
	}()
}

// --- effort ---

func (a *App) efforts() []string { return a.model().Model.Levels() }

func (a *App) cycleEffort() {
	levels := a.efforts()
	if len(levels) == 0 {
		return
	}
	i := 0
	for j, l := range levels {
		if l == a.effort() {
			i = j + 1
		}
	}
	a.setEffort(levels[i%len(levels)], false)
}

func (a *App) setEffort(level string, announce bool) {
	a.agent.SetEffort(level)
	a.effortFrom = core.FromCommand
	a.statusTrigger()
	a.remoteUpdated()
	if err := config.UpdateSettings(map[string]any{"defaultEffort": level}); err != nil {
		a.errorNotice(err)
	}
	if announce {
		a.notice("Effort set to %s.", level)
	}
}

// --- footer rendering ---

func (a *App) renderInput(width int) []string {
	if a.modal != nil {
		return append([]string{""}, a.modal.Render(width)...)
	}
	return a.renderEditor(width)
}

// renderEditor draws the editor, in bash mode (green rule and a hint)
// while its text starts a shell command. The prompt stays "›": the "!" or
// "!!" typed is the mode, as in pi.
func (a *App) renderEditor(width int) []string {
	mode := shellMode(a.editor.Text())
	a.editor.Prompt = tui.FG(6, "› ")
	if mode == "" {
		a.editor.Rule = tui.Dim
		return a.editor.Render(width)
	}
	a.editor.Rule = func(s string) string { return tui.FG(2, s) }
	hint := "bash mode · runs in " + shortPath(a.cwd) + " · output goes to the model"
	if mode == "!!" {
		hint = "bash mode · not sent to the model"
	}
	return append(a.editor.Render(width), tui.Truncate(" "+tui.FG(2, hint), width, "…"))
}

// jumpPill is the centered "Jump to bottom" pill above the input while the
// fullscreen transcript is scrolled up (Claude Code has the same). It is a
// footer component, so the renderer routes clicks on it here.
type jumpPill struct{ a *App }

func (p jumpPill) Render(width int) []string {
	if p.a.modal != nil || p.a.ui.ScrollOffset() == 0 {
		return nil
	}
	text, style := " Jump to bottom (click) ↓ ", tui.Dim
	if p.a.ui.NewBelow() {
		// Reverse video, so it reads as new rather than as a hint.
		text = " ↓ New output · Jump to bottom "
		style = func(s string) string { return "\x1b[7m" + s + "\x1b[27m" }
	}
	pad := max(0, (width-tui.VisibleWidth(text))/2)
	return []string{strings.Repeat(" ", pad) + style(tui.Truncate(text, width, "…"))}
}

// Click scrolls to the newest output. The renderer only reports the row,
// so the whole line is the button.
func (p jumpPill) Click(int) bool {
	p.a.ui.ScrollToBottom()
	return true
}

func shortPath(p string) string {
	// Only whole path elements: /Users/bob2 is not under /Users/bob.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(p, home); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
			return "~" + rest
		}
	}
	return p
}

func effortStyle(level string) string {
	switch level {
	case "off", "minimal":
		return tui.Dim(level)
	case "low":
		return tui.FG(4, level)
	case "medium":
		return tui.FG(6, level)
	case "high":
		return tui.FG(3, level)
	default: // xhigh, max
		return tui.FG(5, tui.Bold(level))
	}
}

// refreshCatalog updates the models.dev catalog in the background and
// reloads the model list, so newly available providers appear in /model.
func (a *App) refreshCatalog() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := config.RefreshCatalog(ctx); err != nil {
		return // offline is fine; the cached catalog (if any) stays in use
	}
	models, err := config.LoadModels()
	if err != nil {
		return
	}
	a.ui.Do(func() {
		before := len(a.models.List())
		a.models = models
		if n := len(models.List()); n > before {
			a.notice("Model catalog updated: %d models available (/model).", n)
		}
	})
}

// pinnedPrompt returns the latest prompt that has scrolled above the view,
// shown as a thin bar on the first row so the question stays visible
// (codex does the same in fullscreen).
func (a *App) pinnedPrompt(firstVisible, width int) string {
	var last *userBlock
	a.ui.Body.Each(func(c tui.Component, start, end int) {
		if g, ok := c.(gap); ok {
			if u, ok := g.Component.(*userBlock); ok && end-1 <= firstVisible {
				last = u
			}
		}
	})
	if last == nil {
		return ""
	}
	return last.pinLine(width)
}

// legacyConsole reports a console that can't do the fullscreen renderer:
// only Windows before 10 1809 (build 17763), whose conhost lacks the
// alternate screen and VT input. Newer conhost and Windows Terminal both
// work, and environment variables can't tell them apart reliably (Windows
// Terminal as the default console sets no WT_SESSION, nor does sshd). Set
// "renderer" in settings.json to override.
func legacyConsole() bool { return windowsBuild() > 0 && windowsBuild() < 17763 }

// rendererMode is the mode the "renderer" setting asks for; empty (or
// anything unknown) lets atto pick, see legacyConsole.
func rendererMode(setting string) tui.Mode {
	switch setting {
	case "inline":
		return tui.Inline
	case "fullscreen":
		return tui.Fullscreen
	}
	if legacyConsole() {
		return tui.Inline
	}
	return tui.Fullscreen
}

// cmdTui shows or changes the renderer. The choice is saved in
// settings.json and applied to the running screen at once.
func (a *App) cmdTui(arg string) {
	name := func(m tui.Mode) string {
		if m == tui.Inline {
			return "inline"
		}
		return "fullscreen"
	}
	if arg == "" {
		a.notice("Renderer: %s. Choices: auto (pick for this terminal), fullscreen, inline. Usage: /tui <choice>", name(a.ui.Mode))
		return
	}
	var value string
	switch arg {
	case "auto":
	case "fullscreen", "inline":
		value = arg
	default:
		a.notice("Unknown renderer %q. Choices: auto, fullscreen, inline.", arg)
		return
	}
	// An empty value is the same as no setting: atto picks again.
	if err := config.UpdateSettings(map[string]any{"renderer": value}); err != nil {
		a.errorNotice(err)
		return
	}
	mode := rendererMode(value)
	a.ui.SetMode(mode)
	a.notice("Renderer set to %s.", arg+map[bool]string{true: " (" + name(mode) + ")"}[arg == "auto"])
}
