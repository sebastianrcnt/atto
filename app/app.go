// Package app is the interactive terminal front end: a client of atto's
// session runtime (package server) that renders its items and state and
// sends what is typed as requests (see client.go).
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
	"github.com/sebastianrcnt/atto/update"
)

// Version is the release this binary was built from (see update.Current).
var Version = update.Current()

type Options struct {
	Inline  bool   // render inline instead of fullscreen
	Resume  bool   // open the resume picker at startup
	Model   string // provider/id to use instead of the default
	Effort  string // effort to use instead of the default
	Session string // resume the session with this ID
	Prompt  string // first message, submitted once the UI is up (atto "fix the build")
}

// modal is a picker shown in place of the editor.
type modal interface {
	tui.Component
	tui.InputHandler
}

type App struct {
	ui     *tui.TUI
	models config.ModelsFile

	// conn is the connection to the session runtime; threadID the thread
	// this terminal shows and info its state as the runtime last said.
	conn             *conn
	threadID         string
	sessPath         string
	info             server.ThreadInfo
	snapEvent        int64 // the cursor of the last snapshot
	view             server.ThreadView
	pageLoading      bool
	pageEpoch        int
	snapshotPending  int
	notifyEvent      int64
	snapshotEvents   []server.Notification
	applyingSnapshot bool
	treeEntries      []session.Entry
	treeLeaf         string
	pending          server.PendingInput
	readOnly         string // why the session is read-only ("": it is not)
	// catalog is the session's slash commands (commands/list).
	catalog []server.CommandInfo
	// loaded is what the session loaded, as its latest Loaded item said.
	loaded                    core.Loaded
	trustAsked                map[string]bool
	trustActive, trustWaiting bool
	trustDone                 func()

	editor *tui.Editor
	modal  modal
	// login replaces the browser, clipboard and device ID in tests.
	login loginHooks
	// clipboard reads an image for Ctrl+V / Alt+V.
	clipboard func(context.Context) (provider.Image, error)
	// copyEnv writes copied text (the zero value is the real system).
	copyEnv copyEnv
	toast   toast

	// The run, as the runtime says: whether one runs and which kind
	// ("turn", "compact" or "branchSummary").
	busy     bool
	runKind  string
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
	// draftChars is how much of each tool call (by item) the call in
	// progress has written.
	draftChars map[string]int
	// turnIn counts the run's input tokens the server had not cached.
	turnIn       int
	lastEvent    time.Time
	toolsRunning int
	now          func() time.Time
	verbRand     *rand.Rand
	stopTicker   func()
	ctxTokens    int
	usage        usageStats
	// goalRetryAt is when a goal retry waiting starts (zero: none).
	goalRetryAt time.Time

	// The blocks of the items being streamed: kinds by item ID until
	// completed, tools by item ID; steerGroup and steerBlock join the user
	// messages of one steer.
	kinds      map[string]transcript.Kind
	thinking   *thinkingBlock
	text       *textBlock
	tools      map[string]*toolBlock
	compact    *compactBlock
	summaryBlk *summaryBlock
	shellBlk   *shellBlock
	steerGroup string
	steerBlock *userBlock
	replaying  bool

	// details expands every collapsible block (ctrl+t); origView shows the
	// original text of blocks an extension replaced (ctrl+o).
	details  details
	origView details
	// itemBlocks are the blocks of streamed reasoning and assistant items
	// by item ID, until they are saved; blocks are those saved, by block ID
	// (see blockdisplay.go).
	itemBlocks map[string]displayBlock
	blocks     map[string]displayBlock
	sessName   string
	// summaryAsked: this terminal asked for a branch summary; canceled, the
	// tree opens again. skipSummary is settings.json's
	// branchSummary.skipPrompt.
	summaryAsked bool
	skipSummary  bool
	// noToolGroups is settings.json's "toolGroups": false (see toolRun).
	noToolGroups bool
	// esc detects Esc twice on an empty prompt; escAction is what it opens.
	esc       doubleEsc
	escAction string

	// Status line counts, as the runtime says.
	jobCount, timerCount int

	// bgx is the exit menu (background_exit.go); bgLine is printed when
	// atto exits after the session went to a background run.
	bgx    bgExit
	bgLine string

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

	// remote is the /remote gateway while it runs (remote.go); remoteHost
	// and remotePort override where it listens (tests).
	remote       *remote
	remoteThread string // the session its clients were last told about
	remoteHost   string
	remotePort   *int
	// prompt is the runtime's open prompt this terminal shows, and
	// promptWaiting one that waits for a picker of this terminal to close.
	prompt        *shownPrompt
	promptWaiting *server.Prompt
	localPrompt   *localPrompt
	localSeq      int

	cwd      string
	quit     chan struct{}
	quitOnce sync.Once
	quitting bool
	closed   bool
}

// newApp makes the App with what it shows, before it connects.
func newApp(term tui.Terminal, models config.ModelsFile, cwd string) *App {
	a := &App{
		ui:         tui.New(term),
		models:     models,
		tools:      map[string]*toolBlock{},
		kinds:      map[string]transcript.Kind{},
		draftChars: map[string]int{},
		cwd:        cwd,
		quit:       make(chan struct{}),
		clipboard:  images.SystemClipboardImage,
	}
	a.build()
	return a
}

// applySettings takes what of settings.json the terminal follows.
func (a *App) applySettings(s config.Settings) {
	a.escAction = s.DoubleEscapeAction
	a.spinnerVerbs, a.spinnerScan = s.SpinnerVerbs, s.SpinnerScanner
	a.bgx.off = s.BackgroundExit != nil && !*s.BackgroundExit
	a.skipSummary = s.BranchSummary != nil && s.BranchSummary.SkipPrompt
	a.noToolGroups = s.ToolGroups != nil && !*s.ToolGroups
}

func Run(opts Options) error {
	settings, models, err := core.Load()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	terminal := tui.NewProcessTerminal()
	a := newApp(terminal, models, cwd)
	stopSignals := watchTerminalExit(func() { terminal.InterruptOutput(); a.doQuit() })
	defer stopSignals()
	if opts.Inline || rendererMode(settings.Renderer) == tui.Inline {
		a.ui.Mode = tui.Inline
	}
	a.applySettings(settings)
	a.ui.NoMouse = mouseDisabled(settings.Mouse, os.Getenv)

	// With the daemon, sessions run in its workers and go on
	// when this terminal goes. Otherwise the runtime runs in this process:
	// a session left behind (/clear, /resume) closes once idle, as it
	// always did, and everything ends with atto.
	var srv *server.Server
	workers := daemon.Usable()
	if !workers {
		srv = server.New(Version, cwd)
		srv.LockKind = session.KindTUI
		srv.Retire = true
		if err := a.connect(server.Connect(context.Background(), srv), srv); err != nil {
			srv.CloseWith("exit")
			return err
		}
	}
	if !opts.Resume {
		if err := a.open(opts); err != nil {
			if srv != nil {
				srv.Close()
			}
			return err
		}
	}
	a.statusCmd = settings.StatusLine != nil && settings.StatusLine.Command != ""
	a.startStatusLine(settings.StatusLine)
	if config.CatalogStale() {
		go a.refreshCatalog()
	}

	tui.OnPanic = writeCrash
	if err := a.ui.Start(); err != nil {
		a.shutdown()
		return err
	}
	start := func() {
		a.trustDone = func() {
			a.rpcErr("thread/sessionStart", nil)
			if opts.Prompt != "" {
				a.submit(opts.Prompt, nil)
			}
		}
		a.askProjectApprovals()
	}
	a.ui.Do(func() {
		if opts.Resume {
			a.openAgents(tabAll)
			c := a.modal.(*agentCenter)
			c.scope = a.cwd
			c.resume = true
			c.flat = true
			c.onClose = func() {
				a.closeModal()
				if !c.picking {
					a.doQuit()
				}
			}
			c.onOpen = func(id, cwd string) {
				a.openThread(id, map[string]any{"deferStart": true}, func(info server.ThreadInfo, old *conn) { a.switchTo(info, "resume", old, start) })
			}
			c.onNew = func(cwd string) {
				a.openThread("", map[string]any{"cwd": cwd, "deferStart": true}, func(info server.ThreadInfo, old *conn) { a.switchTo(info, "new", old, start) })
			}
		} else {
			start()
		}
	})
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
		a.quitting = true
		a.stopRemote()
	})
	a.ui.Stop()
	return a.shutdown()
}

// open opens the first session: the one asked for, or a new one.
func (a *App) open(opts Options) error {
	id := opts.Session
	var info server.ThreadInfo
	var err error
	if id != "" {
		info, err = a.openSync(id, map[string]any{"deferStart": true})
		if err != nil {
			if errors.Is(err, errWorkerProtocol) || errors.Is(err, daemon.ErrProtocol) {
				return err
			}
			return err
		}
	}
	if info.ID == "" {
		if info, err = a.openSync("", map[string]any{"model": opts.Model, "effort": opts.Effort, "deferStart": true}); err != nil {
			return err
		}
	}
	a.ui.Do(func() { a.show(info) })
	return nil
}

// show makes info the thread this terminal shows.
func (a *App) show(info server.ThreadInfo) {
	a.threadID = info.ID
	if a.workers() && info.Cwd != "" {
		a.cwd = info.Cwd
	}
	a.closed = false
	a.applySnapshot(info)
	a.treeEntries, a.treeLeaf = nil, ""
	a.loadCatalog()
	a.remoteSwitched()
}

// shutdown ends the session as atto exits. Without the daemon, the
// runtime goes with this process: its sessions end (SessionEnd, jobs
// stop), unless one went to a background run.
func (a *App) shutdown() error {
	var threadID, bgLine string
	a.ui.Do(func() {
		if a.stopTicker != nil {
			a.stopTicker()
			a.stopTicker = nil
		}
		threadID, bgLine = a.threadID, a.bgLine // notifications still arrive
	})
	if bgLine != "" {
		fmt.Println(bgLine)
	}
	cn := a.conn
	if cn == nil {
		return nil
	}
	if cn.own == nil {
		// A worker's session goes on; if it retires, SessionEnd says exit.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = cn.c.Call(ctx, "thread/detach", map[string]any{"threadId": threadID, "reason": "exit"}, nil)
		return cn.c.Close()
	}
	if threadID != "" && bgLine == "" {
		var r struct {
			StoppedJobs int      `json:"stoppedJobs"`
			Notices     []string `json:"notices"`
		}
		_ = cn.c.Call(context.Background(), "thread/close", map[string]any{"threadId": threadID, "reason": "exit"}, &r)
		if r.StoppedJobs > 0 {
			fmt.Printf("atto: stopped %d background job(s)\n", r.StoppedJobs)
		}
		for _, n := range r.Notices {
			fmt.Fprintln(os.Stderr, n)
		}
	}
	cn.c.Close()
	cn.own.CloseWith("exit")
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
	a.ui.OnScrollTop = a.loadEarlier
	a.ui.PaddingX = 1
	a.ui.GapY = 1
	a.ui.Pin = a.pinnedPrompt
	a.addHeader()
}

// switchTo shows thread info in place of the one shown, which this
// terminal leaves (reason: clear, resume): it goes on until idle, then
// ends. old is the connection the session left was shown on. with runs
// once the session left is let go.
func (a *App) switchTo(info server.ThreadInfo, reason string, old *conn, with func()) {
	prev := a.threadID
	a.show(info)
	if prev == "" || prev == info.ID {
		if with != nil {
			with()
		}
		return
	}
	left := func(raw json.RawMessage, err error) {
		var r struct {
			StoppedJobs int      `json:"stoppedJobs"`
			Notices     []string `json:"notices"`
		}
		if err == nil && json.Unmarshal(raw, &r) == nil {
			for _, n := range r.Notices {
				a.notice("%s", n)
			}
			if r.StoppedJobs > 0 {
				a.notice("Stopped %d background job(s) of the previous conversation.", r.StoppedJobs)
			}
		}
		if with != nil {
			with()
		}
	}
	params := map[string]any{"threadId": prev, "reason": reason}
	if old == nil || old == a.conn {
		a.rpc("thread/detach", params, left)
		return
	}
	go func() { // a worker's connection: leave it, then close it
		var raw json.RawMessage
		err := old.c.Call(context.Background(), "thread/detach", params, &raw)
		old.c.Close()
		a.ui.Do(func() { left(raw, err) })
	}()
}

// newSession starts a new conversation and shows it.
func (a *App) newSession(reason string, with func()) {
	a.openThread("", nil, func(info server.ThreadInfo, old *conn) { a.switchTo(info, reason, old, with) })
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

// notice shows a notice of the terminal's own (not the runtime's: it is
// not kept for other clients or a later attach).
func (a *App) notice(format string, args ...any) {
	a.add(&noticeBlock{text: fmt.Sprintf(format, args...), style: tui.Dim})
}

func (a *App) errorNotice(err error) {
	a.add(&noticeBlock{text: "Error: " + err.Error(), style: func(s string) string { return tui.FG(1, s) }})
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
	case "left":
		// As in codex: ← on an empty prompt opens the agent center.
		if a.editor.Text() == "" {
			a.openAgents(tabAll)
			return true
		}
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
		case a.shellRunning():
			a.rpcErr("shell/interrupt", nil)
		case a.busy:
			// Ctrl+C: steers not taken come back to the editor.
			a.rpcErr("turn/interrupt", map[string]any{"mode": "cancel"})
		case a.editor.Text() != "":
			a.editor.SetText("")
		default:
			a.requestQuit()
		}
		return true
	case "ctrl+d":
		if a.editor.Text() == "" && (!a.busy || a.workers() || a.exitMenuAvailable()) {
			a.requestQuit()
			return true
		}
	case "ctrl+b":
		// As in Claude Code: the running command moves to the background
		// and the turn goes on. Otherwise it is the editor's cursor-left.
		if a.busy && a.toolsRunning > 0 {
			a.rpcErr("turn/background", nil)
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
		if a.takeBackLast() {
			return true
		}
	}
	if data == "\x1bv" { // alt+v: where the terminal keeps Ctrl+V for pasting text
		a.pasteClipboardImage()
		return true
	}
	return false
}

// shellRunning reports whether a "!" command of the session runs.
func (a *App) shellRunning() bool { return a.shellBlk != nil && !a.shellBlk.done }

// interrupt is Esc while something runs: it stops a "!" command, else the
// run (pending steers then go out at once), else pauses a goal retry
// waiting. False when nothing runs.
func (a *App) interrupt() bool {
	switch {
	case a.shellRunning():
		a.rpcErr("shell/interrupt", nil)
	case a.busy:
		a.rpcErr("turn/interrupt", map[string]any{"mode": "sendPending"})
	case !a.goalRetryAt.IsZero():
		a.goalRetryAt = time.Time{}
		a.rpcErr("turn/interrupt", nil)
	default:
		return false
	}
	return true
}

// submit sends what was typed (Enter): the terminal's own commands run
// here, everything else goes to the runtime as typed.
func (a *App) submit(text string, att []tui.Attachment) {
	if a.refuseReadOnly(text) {
		return
	}
	a.ui.ScrollToBottom()
	if strings.HasPrefix(text, "/") && a.runLocal(text) {
		return
	}
	if strings.TrimSpace(text) != "" && !strings.HasPrefix(text, "/") && shellMode(text) == "" && a.noModel() {
		a.restoreToEditor([]string{text}, att...)
		return
	}
	a.send(text, att, "auto")
}

// send is input/submit; a refusal gives the input back to the editor.
func (a *App) send(text string, att []tui.Attachment, intent string) {
	imgs, err := imageInputs(att)
	if err != nil {
		a.errorNotice(fmt.Errorf("saving image: %w", err))
		a.restoreToEditor([]string{text}, att...)
		return
	}
	params := map[string]any{"input": text, "intent": intent}
	if len(imgs) > 0 {
		params["images"] = imgs
	}
	a.rpc("input/submit", params, func(_ json.RawMessage, err error) {
		if err != nil {
			a.errorNotice(err)
			a.restoreToEditor([]string{text}, att...)
		}
	})
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

// setEffort sets the session's effort and makes it the default, as the
// terminal always did.
func (a *App) setEffort(level string, announce bool) {
	a.info.Effort = level
	a.statusTrigger()
	a.rpcErr("thread/setEffort", map[string]any{"effort": level, "saveDefault": true})
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
	hint := "bash mode · runs in " + core.ShortPath(a.cwd) + " · output goes to the model"
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
