package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// /remote serves this TUI's own session to a phone or browser: the same
// protocol and web client as atto serve (package server), with the
// session as the server's only thread (server.Live). The browser sees the
// transcript, follows it live, and sends messages as if typed here; all
// of it shows in the terminal too. Each start makes a new token, so
// /remote off revokes the link.
//
// HTTP handlers reach the App through remoteSession, whose methods run under
// the UI lock (a.ui.Do) like everything else that touches the App; the
// App publishes notifications from there too, in order.

// defaultRemotePort is /remote's port unless settings.json's
// "remote": {"port": N} or /remote on <port> says otherwise.
const defaultRemotePort = 7879

// remote is a running /remote server.
type remote struct {
	srv     *http.Server
	api     *server.Server
	addr    string // what it listens on
	token   string
	links   []string
	clients int
	// thread is the session the clients were last told about.
	thread string
	// turnSeq numbers the runs; turnID is the running one's.
	turnSeq int
	turnID  string
	// last is the thread state last published (thread/updated).
	last server.ThreadInfo
	// notices numbers the notices sent as items; notes are those of the
	// session shown, with the number of transcript items before each, so
	// thread/read puts them back in place.
	notices int
	notes   []remoteNote
	// goal is the goal last published (goal/updated).
	goal *server.GoalInfo
	// pending is the pending input last published (turn/pending).
	pending server.PendingInput
}

type remoteNote struct {
	at   int
	item server.Item
}

// maxRemoteNotes bounds the notices kept for thread/read.
const maxRemoteNotes = 200

var errRemoteOff = errors.New("remote control is off")

func (a *App) cmdRemote(arg string) {
	fields := strings.Fields(arg)
	verb := ""
	if len(fields) > 0 {
		verb = fields[0]
	}
	switch verb {
	case "":
		if a.remote != nil {
			a.showRemote(a.remote)
			return
		}
		a.startRemote(0)
	case "on":
		port := 0
		if len(fields) > 1 {
			p, err := strconv.Atoi(fields[1])
			if err != nil || p < 1 || p > 65535 {
				a.notice("Not a port: %s. Usage: /remote on [port]", fields[1])
				return
			}
			port = p
		}
		if r := a.remote; r != nil {
			if _, cur, _ := net.SplitHostPort(r.addr); port == 0 || strconv.Itoa(port) == cur {
				a.showRemote(r)
				return
			}
			a.stopRemote()
		}
		a.startRemote(port)
	case "off":
		if a.remote == nil {
			a.notice("Remote control is off.")
			return
		}
		a.stopRemote()
		a.notice("Remote control stopped. Its link no longer works.")
	default:
		a.notice("Usage: /remote [on [port]|off]")
	}
}

// startRemote listens on port (0: the configured one) on every interface.
func (a *App) startRemote(port int) {
	if port == 0 {
		port = defaultRemotePort
		if s, err := config.LoadSettings(); err == nil && s.Remote != nil && s.Remote.Port > 0 {
			port = s.Remote.Port
		}
		if a.remotePort != nil {
			port = *a.remotePort
		}
	}
	token, err := server.NewToken(16)
	if err != nil {
		a.errorNotice(err)
		return
	}
	host := a.remoteHost
	if host == "" {
		host = "0.0.0.0"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		a.errorNotice(fmt.Errorf("remote control: %w", err))
		return
	}
	r := &remote{token: token, addr: ln.Addr().String(), thread: a.sess.ID}
	r.api = server.NewLive(Version, remoteSession{a, r})
	r.api.OnClients = func(n int) {
		a.ui.Do(func() {
			if a.remote == r {
				r.clients = n
			}
		})
	}
	r.srv = &http.Server{Handler: r.api.HTTPHandler(token), ReadHeaderTimeout: 10 * time.Second}
	r.links = server.WebLinks(r.addr, token)
	if len(r.links) == 0 { // no network beyond this machine
		r.links = []string{"http://" + r.addr + "/#token=" + token}
	}
	r.last = a.remoteInfo(r)
	r.goal = a.remoteGoalInfo()
	a.remote = r
	go func() { _ = r.srv.Serve(ln) }()
	a.showRemote(r)
}

// stopRemote closes the server and every connection to it.
func (a *App) stopRemote() {
	r := a.remote
	if r == nil {
		return
	}
	a.remote = nil
	_ = r.srv.Close()
	r.api.Close()
}

// showRemote prints the links, a QR code of the first, and the warnings.
func (a *App) showRemote(r *remote) {
	b := &remoteBlock{title: "Remote control is on. Open this link on your phone or in a browser:", links: r.links}
	if qr, err := server.QR(r.links[0]); err == nil {
		b.qr = qr
	}
	if len(r.links) > 1 {
		b.hint = append(b.hint, "Other addresses of this machine are listed too; the QR code is the first.")
	}
	if !server.IsLoopback(r.addr) {
		b.hint = append(b.hint, strings.TrimPrefix(server.TLSWarning, "warning: "))
	}
	b.hint = append(b.hint, "Anyone with the link controls this session. /remote off stops it and revokes the link.")
	a.add(b)
}

// remoteBlock shows the links and QR code of /remote.
type remoteBlock struct {
	title string
	links []string
	qr    []string
	hint  []string
}

func (b *remoteBlock) Render(width int) []string {
	out := []string{"  " + tui.Bold("◉ ") + b.title}
	for _, l := range b.links {
		for _, w := range tui.Wrap(l, max(1, width-4)) {
			out = append(out, "    "+tui.FG(6, w))
		}
	}
	if len(b.qr) > 0 {
		if qw := tui.VisibleWidth(b.qr[0]) + 4; qw <= width {
			out = append(out, "")
			for _, l := range b.qr {
				out = append(out, "    "+l) // not styled: the code needs plain foreground blocks
			}
		} else {
			out = append(out, "  "+tui.Dim(fmt.Sprintf("(widen the terminal to %d columns for the QR code)", qw)))
		}
	}
	for _, h := range b.hint {
		for _, w := range tui.Wrap(h, max(1, width-2)) {
			out = append(out, "  "+tui.Dim(w))
		}
	}
	return out
}

// remoteStatus is the status line's indicator.
func (a *App) remoteStatus() string {
	if a.remote == nil {
		return ""
	}
	return tui.Dim(fmt.Sprintf("remote · %d connected", a.remote.clients))
}

// --- what the App tells the clients ---

func (a *App) remoteInfo(r *remote) server.ThreadInfo {
	m, effort := a.agent.Current()
	info := server.ThreadInfo{
		ID: a.sess.ID, Cwd: a.cwd, Name: a.sessName, Effort: effort,
		ContextTokens: a.ctxTokens, Busy: a.busy, TurnID: r.turnID, Live: true,
	}
	server.SetModel(&info, m, a.models)
	u := a.remoteUsage()
	info.Usage = &u
	if a.busy {
		info.Turn = &server.TurnInfo{StartedAt: a.runStart.UnixMilli(), Verb: a.turnVerb, InputTokens: a.turnIn, OutputTokens: a.turnOut}
	}
	if p := a.pendingInput(); len(p.Steers)+len(p.Queued) > 0 {
		info.Pending = &p
	}
	return info
}

// remoteUsage is the session's usage as the status line counts it.
func (a *App) remoteUsage() server.Usage {
	u := &a.usage
	return server.Usage{
		InputTokens: u.input, CachedInputTokens: u.cached, CacheWriteTokens: u.cacheWrite, OutputTokens: u.output, Cost: u.cost,
		LastInputTokens: u.last.PromptTokens, LastCachedInputTokens: u.last.CachedTokens,
	}
}

// remoteStep tells clients a model response ended: the session's totals
// and the response's own usage (thread/usage).
func (a *App) remoteStep(step provider.Usage) {
	if a.remote == nil {
		return
	}
	a.remotePublish("thread/usage", map[string]any{"usage": a.remoteUsage(), "step": server.StepUsage(step), "contextTokens": a.ctxTokens})
}

// pendingInput is what renderPending shows: steers not taken yet and
// queued follow-ups.
func (a *App) pendingInput() server.PendingInput {
	p := server.PendingInput{Steers: append([]string{}, a.pendingSteers...)}
	for _, q := range a.queued {
		p.Queued = append(p.Queued, q.text)
	}
	return p
}

// remotePending publishes the pending input when it changed (turn/pending).
// It runs where the footer draws it, so every change reaches clients.
func (a *App) remotePending() {
	r := a.remote
	if r == nil {
		return
	}
	p := a.pendingInput()
	if slices.Equal(p.Steers, r.pending.Steers) && slices.Equal(p.Queued, r.pending.Queued) {
		return
	}
	r.pending = p
	a.remotePublish("turn/pending", map[string]any{"pending": p})
}

func (a *App) remotePublish(method string, params map[string]any) {
	r := a.remote
	if r == nil {
		return
	}
	params["threadId"] = a.sess.ID
	r.api.Publish(method, params)
}

// remoteItem publishes a transcript item change; replays are not sent, a
// switch notification follows them.
func (a *App) remoteItem(method string, it *transcript.Item) {
	if a.remote == nil || a.replaying {
		return
	}
	a.remotePublish(method, map[string]any{"turnId": a.remote.turnID, "item": a.wireItem(it)})
}

// wireItem is the protocol form of a transcript item, with what extensions
// show on its block.
func (a *App) wireItem(it *transcript.Item) server.Item {
	w := server.WireItem(a.sess.ID, it)
	w.Display = server.WireDisplay(a.blockState(w.BlockID))
	return w
}

// remoteDisplay tells clients what extensions show on a block changed.
func (a *App) remoteDisplay(d *blockDisplay) {
	if a.remote == nil || d.item == "" {
		return
	}
	a.remotePublish("item/display", map[string]any{"itemId": d.item, "blockId": d.id, "display": server.WireDisplay(&d.state)})
}

// remoteExtUI tells clients the extensions' status items or widgets
// changed.
func (a *App) remoteExtUI() {
	if a.remote == nil {
		return
	}
	a.remotePublish("extension/ui", map[string]any{"ui": server.WireExtensionUI(&a.extUI)})
}

func (a *App) remoteDelta(it *transcript.Item, d string) {
	if a.remote == nil || a.replaying {
		return
	}
	a.remotePublish("item/delta", map[string]any{"turnId": a.remote.turnID, "itemId": it.ID, "delta": d})
}

// remoteNotice sends a notice of the terminal (not a transcript item) as
// a finished notice item; thread/read has it too, until the session is
// switched.
func (a *App) remoteNotice(text string) {
	r := a.remote
	if r == nil {
		return
	}
	r.notices++
	it := server.Item{ID: fmt.Sprintf("%s-n%d", a.sess.ID, r.notices), Type: server.ItemNotice, Text: text, Status: "completed"}
	if len(r.notes) < maxRemoteNotes {
		r.notes = append(r.notes, remoteNote{len(a.tr().Items()), it})
	}
	a.remotePublish("item/completed", map[string]any{"turnId": r.turnID, "item": it})
}

// remoteSwitched tells clients the transcript was replaced: a new or
// resumed session, or another branch of this one.
func (a *App) remoteSwitched() {
	r := a.remote
	if r == nil {
		return
	}
	prev := r.thread
	r.thread = a.sess.ID
	r.last = a.remoteInfo(r)
	r.goal = a.remoteGoalInfo()
	r.notes = nil
	r.api.Publish("thread/switched", map[string]any{"threadId": a.sess.ID, "previousThreadId": prev})
}

// remoteUpdated publishes the thread's state when the model, effort or
// name changed.
func (a *App) remoteUpdated() {
	r := a.remote
	if r == nil {
		return
	}
	info := a.remoteInfo(r)
	if info.Model == r.last.Model && info.Effort == r.last.Effort && info.Name == r.last.Name && info.ID == r.last.ID {
		return
	}
	r.last = info
	a.remotePublish("thread/updated", map[string]any{"thread": info})
}

func (a *App) remoteTurnStarted() {
	r := a.remote
	if r == nil {
		return
	}
	r.turnSeq++
	r.turnID = fmt.Sprintf("%s-t%d", a.sess.ID, r.turnSeq)
	a.remotePublish("turn/started", map[string]any{"turnId": r.turnID, "startedAt": a.runStart.UnixMilli(), "verb": a.turnVerb})
	a.remoteGoal()
}

// remoteGoalInfo is the goal as the status line shows it; nil without one.
func (a *App) remoteGoalInfo() *server.GoalInfo {
	g := a.goal.Goal
	if g == nil {
		return nil
	}
	secs := a.goal.Elapsed()
	return &server.GoalInfo{
		Objective: g.Objective, Status: string(g.Status), Label: g.Status.Label(),
		Indicator: g.Indicator(secs, a.goal.Held() && !a.busy), Summary: g.Summary(), Note: g.Note,
		Tokens: goal.Tokens(g.TokensUsed), TokensUsed: g.TokensUsed,
		Elapsed: goal.FormatElapsed(secs), Seconds: secs, Held: a.goal.Held(),
	}
}

// remoteGoal publishes the goal when it changed (goal/updated; null when
// cleared). It is called where the goal changes, and by the inbox's tick
// for the time of a running turn.
func (a *App) remoteGoal() {
	r := a.remote
	if r == nil {
		return
	}
	g := a.remoteGoalInfo()
	if (g == nil) == (r.goal == nil) && (g == nil || *g == *r.goal) {
		return
	}
	r.goal = g
	a.remotePublish("goal/updated", map[string]any{"goal": g})
}

func (a *App) remoteTurnCompleted(err error) {
	r := a.remote
	if r == nil {
		return
	}
	status, msg := "completed", ""
	switch {
	case errors.Is(err, context.Canceled):
		status = "interrupted"
	case err != nil:
		status, msg = "failed", err.Error()
	}
	params := map[string]any{"turnId": r.turnID, "status": status, "contextTokens": a.ctxTokens}
	if msg != "" {
		params["error"] = msg
	}
	r.turnID = ""
	a.remotePublish("turn/completed", params)
	a.remoteUpdated()
}

// --- what the clients ask of the App ---

// remoteSession is the App as the server's live thread. Every method runs
// under the UI lock, and fails once its /remote server was stopped.
type remoteSession struct {
	a *App
	r *remote
}

func (l remoteSession) do(fn func() error) error {
	err := errRemoteOff
	l.a.ui.Do(func() {
		if l.a.remote == l.r {
			err = fn()
		}
	})
	return err
}

func (l remoteSession) Thread(items bool, at func()) (server.ThreadInfo, error) {
	var info server.ThreadInfo
	err := l.do(func() error {
		info = l.a.remoteInfo(l.r)
		info.Goal = l.a.remoteGoalInfo()
		if !l.a.extUI.Empty() {
			info.ExtensionUI = server.WireExtensionUI(&l.a.extUI)
		}
		if p := l.a.prompt; p != nil {
			w := p.wire
			info.Prompt = &w
		}
		if items {
			notes := l.r.notes
			for i, it := range l.a.tr().Items() {
				for len(notes) > 0 && notes[0].at <= i {
					info.Items, notes = append(info.Items, notes[0].item), notes[1:]
				}
				info.Items = append(info.Items, l.a.wireItem(&it))
			}
			for _, n := range notes {
				info.Items = append(info.Items, n.item)
			}
		}
		if at != nil {
			at()
		}
		return nil
	})
	return info, err
}

func (l remoteSession) Model() config.ModelRef {
	var m config.ModelRef
	_ = l.do(func() error {
		m = l.a.model()
		return nil
	})
	return m
}

func (l remoteSession) Send(input string, imgs []provider.Image) (status, turnID string, err error) {
	a := l.a
	err = l.do(func() error {
		if why := a.sess.ReadOnly(); why != "" {
			return errors.New(why)
		}
		if a.model().Model.ID == "" && !strings.HasPrefix(input, "/") {
			return errors.New("no model is set up yet: use /login or /model in the terminal")
		}
		var att []tui.Attachment
		for _, im := range imgs {
			att = append(att, tui.Attachment{Label: images.Label(im), Value: im})
		}
		wasBusy, kind, queued, steers := a.busy, a.runKind, len(a.queued), len(a.pendingSteers)
		a.fromRemote = true
		a.submit(input, att)
		a.fromRemote = false
		switch {
		case !wasBusy && a.busy:
			status, turnID = "started", l.r.turnID
		case wasBusy && kind == "turn" && len(a.pendingSteers) > steers:
			status = "steered"
			if a.remoteSteers == nil {
				a.remoteSteers = map[string]int{}
			}
			a.remoteSteers[input]++
		case len(a.queued) > queued:
			status = "queued"
		default:
			status = "done" // a command that ran at once
		}
		a.remotePending()
		return nil
	})
	return status, turnID, err
}

func (l remoteSession) Interrupt() bool {
	ok := false
	_ = l.do(func() error {
		ok = l.a.interrupt()
		return nil
	})
	return ok
}

func (l remoteSession) Background() bool {
	ok := false
	_ = l.do(func() error {
		ok = l.a.busy && l.a.agent.Background()
		return nil
	})
	return ok
}

func (l remoteSession) SetModel(id string) (server.ThreadInfo, error) {
	var info server.ThreadInfo
	err := l.do(func() error {
		ref, ok := l.a.models.Find("", id)
		if !ok {
			return fmt.Errorf("unknown model %q", id)
		}
		l.a.setModel(ref)
		info = l.a.remoteInfo(l.r)
		return nil
	})
	return info, err
}

func (l remoteSession) SetEffort(level string) (server.ThreadInfo, error) {
	var info server.ThreadInfo
	err := l.do(func() error {
		levels := l.a.efforts()
		found := false
		for _, x := range levels {
			found = found || x == level
		}
		if !found {
			return fmt.Errorf("unknown effort %q (levels: %s)", level, strings.Join(levels, ", "))
		}
		l.a.setEffort(level, true)
		info = l.a.remoteInfo(l.r)
		return nil
	})
	return info, err
}

func (l remoteSession) Unsteer(input string, queued bool) error {
	return l.do(func() error {
		a := l.a
		if queued {
			for i, v := range slices.Backward(a.queued) {
				if v.text == input {
					a.queued = slices.Delete(a.queued, i, i+1)
					a.remotePending()
					return nil
				}
			}
			return errors.New("that message is no longer queued: it has started")
		}
		i := slices.Index(a.pendingSteers, input)
		if i < 0 || !a.agent.Unsteer(input) {
			return errors.New("that message is no longer pending: the turn has taken it")
		}
		a.pendingSteers = slices.Delete(a.pendingSteers, i, i+1)
		a.takeRemoteSteer(input)
		a.remotePending()
		return nil
	})
}

// Rollback goes back to before the n-th last user message as picking it
// in /tree does: the message goes to the editor when it is empty, and
// clients follow the switch.
func (l remoteSession) Rollback(n int) (string, error) {
	var text string
	err := l.do(func() error {
		a := l.a
		if a.busy {
			return errors.New("a turn is running; turn/interrupt first")
		}
		if why := a.sess.ReadOnly(); why != "" {
			return errors.New(why)
		}
		entries := a.loadSession()
		users := server.UserMessages(session.Active(entries))
		if n > len(users) {
			return fmt.Errorf("only %d user messages to roll back", len(users))
		}
		target := users[len(users)-n]
		leaf, t, ok := session.BranchPoint(entries, target.ID)
		if !ok {
			return errors.New("that message is no longer in the session")
		}
		// Not navigateTree: a message the model never answered is the leaf
		// itself, which /tree calls "already at this point".
		text = t
		a.finishMove(entries, target.ID, leaf, text, nil)
		return nil
	})
	return text, err
}

// takeRemoteSteer reports whether text was steered from the remote, and
// forgets it.
func (a *App) takeRemoteSteer(text string) bool {
	if a.remoteSteers[text] == 0 {
		return false
	}
	a.remoteSteers[text]--
	if a.remoteSteers[text] == 0 {
		delete(a.remoteSteers, text)
	}
	return true
}
