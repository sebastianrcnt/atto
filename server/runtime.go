package server

import (
	"cmp"
	"encoding/json"
	"fmt"
	"github.com/sebastianrcnt/atto/ui"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/hooks"
	"github.com/sebastianrcnt/atto/mcp"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// thread is a session's runtime: the one owner of its execution. It holds
// the agent, the session's writer and lease, hooks, extensions, MCP, the
// goal, the inbox and the pending input, and schedules work the way the
// terminal always did: steers commit at step boundaries, Tab queues a
// turn, Ctrl+Enter replaces the running one, events and goal turns wait
// for user input, a prompt or an open picker. Clients only send requests
// and follow notifications; any number may be attached.
//
// Everything it does runs on its lane, one goroutine taking functions in
// order (do, call): requests, the agent's events, inbox ticks, timers and
// extension calls. Its notifications are published from the lane too, so
// a snapshot taken there and the event ID it reads agree. Nothing on the
// lane waits for a client: prompts are answered later, as requests.
type thread struct {
	statusCommandCache         *statusLineCache
	customStatusConfigured     bool
	customStatusLines          []string
	statusUIInput              string
	statusUIBusy               bool
	statusUICheck, statusUIRan time.Time
	s                          *Server
	id                         string
	cwd                        string

	laneMu   sync.Mutex
	laneQ    []func()
	laneWake chan struct{}
	closed   bool // the lane takes no more work
	done     chan struct{}

	// Lane only, from here.
	name        string
	startSource string
	agent       *agent.Agent
	sess        *session.Writer
	release     func()
	hooks       *hooks.Runner
	ext         *extensions.Manager
	mcp         *mcp.Manager
	mcpAsked    map[string]bool
	hookSrc     []config.HookSource
	models      config.ModelsFile
	loaded      core.Loaded
	modelFrom   core.Origin
	effortFrom  core.Origin
	readOnly    string // why the session cannot be written ("": it can)

	tr             transcript.Builder
	items          []Item // completed items, notices included
	headlessBlocks blocks
	hasMore        bool
	before         string
	itemOrder      map[string]int // start order, including items still open
	itemSeq        int
	blocks         blocks
	elements       *ui.Registry
	uiEntries      map[string]string
	uiSeq          int
	ui             extensions.UIState
	extTexts       int
	notices        int
	steerSeq       int    // numbers committed steers (Item.SteerGroup)
	steerNext      string // the group of the user items being committed

	turns    core.TurnRunner[*pendingInput]
	runKind  string // turn, compact or branchSummary while busy
	runDone  chan struct{}
	turnID   string
	turnSeq  int
	runStart time.Time
	turn     TurnInfo
	usage    provider.Usage // the running turn's
	total    Usage          // the session's, every response it saved
	ctx      int            // context estimate
	activity string
	tools    int // commands running
	// typed is the user's message that started the running turn; replied
	// is set once the model answered it.
	typed   *pendingInput
	replied bool

	inputSeq int
	steers   []*pendingInput // the user's steers the turn has not taken

	jobCount, timerCount int

	goal       core.GoalDriver
	retryTimer *time.Timer
	lastGoal   *GoalInfo // last published

	shell        *shellRun
	pendingShell []pendingShell

	// A move in the tree waiting for the running turn to stop, and the
	// branch summary being written (runKind branchSummary).
	pendingTree    string
	pendingSummary *summaryRequest
	summary        *summaryRun

	login     *loginRun
	prompt    *openPrompt
	prompts   []*openPrompt
	promptSeq int
	gates     map[string]int // open pickers, by client
	// handoff is a "Run in background" in progress (handoff.go).
	handoff handoffState

	recModel, recEffort string
	lastActive          time.Time // a client was attached or work ran
	closing             bool

	metas      []userMeta          // for the user items being fed (feed)
	itemMeta   map[string]userMeta // by item ID, until completed
	toolsShown int                 // tools last published (setActivity)
	catalogVer string              // see catalogVersion
	treeClient string              // the client that asked for pendingTree
	attached   map[string]bool     // clients following the thread
	// leaveReason is what the last client to leave did (clear, resume,
	// exit): the reason SessionEnd hooks get if the thread retires.
	leaveReason string
	retireTimer *time.Timer
	inboxOff    atomic.Bool // closing: the inbox is left alone
	// mgd, on the session of an agent run by atto agent in this worker, owns
	// the agent's turns (agentturn.go).
	mgd *managedAgent
}

// pendingInput is input waiting: a steer the turn has not taken, a
// queued follow-up or a send-now message.
type pendingInput struct {
	ID     string
	Text   string
	Images []provider.Image
	Client string // the client it came from
}

// --- the lane ---

func (t *thread) startLane() {
	if t.laneWake == nil {
		t.laneWake = make(chan struct{}, 1)
	}
	if t.done == nil {
		t.done = make(chan struct{})
	}
	go func() {
		for {
			t.laneMu.Lock()
			if len(t.laneQ) == 0 {
				if t.closed {
					t.laneMu.Unlock()
					close(t.done)
					return
				}
				t.laneMu.Unlock()
				<-t.laneWake
				continue
			}
			fn := t.laneQ[0]
			t.laneQ = t.laneQ[1:]
			t.laneMu.Unlock()
			fn()
		}
	}()
}

// do queues fn for the lane; false once the thread has closed.
func (t *thread) do(fn func()) bool {
	t.laneMu.Lock()
	if t.closed {
		t.laneMu.Unlock()
		return false
	}
	t.laneQ = append(t.laneQ, fn)
	t.laneMu.Unlock()
	select {
	case t.laneWake <- struct{}{}:
	default:
	}
	return true
}

var errThreadClosed = failure(ReasonNotFound, "the session was closed")

// call runs fn on the lane and waits for it. Never call it from the lane.
func (t *thread) call(fn func() error) error {
	done := make(chan struct{})
	var err error
	if !t.do(func() {
		defer close(done)
		err = fn()
	}) {
		return errThreadClosed
	}
	<-done
	return err
}

// stopLane lets the lane finish what it has and end.
func (t *thread) stopLane() {
	t.laneMu.Lock()
	t.closed = true
	t.laneMu.Unlock()
	select {
	case t.laneWake <- struct{}{}:
	default:
	}
}

// --- what clients see ---

func (t *thread) publish(method string, params map[string]any) {
	params["threadId"] = t.id
	t.s.publish(method, params)
	if t.elements != nil && !strings.HasPrefix(method, "ui/") {
		switch method {
		case "thread/updated", "goal/updated", "turn/started", "turn/completed", "turn/activity", "thread/usage", "thread/status":
			t.elements.Invalidate(ui.Match{Site: ui.Status})
			if method == "goal/updated" {
				t.elements.Invalidate(ui.Match{Site: ui.Pane, ID: "atto/goal"})
			}
		}
	}
}

func (t *thread) model() config.ModelRef {
	m, _ := t.agent.Current()
	return m
}

func (t *thread) info() ThreadInfo {
	m, effort := t.agent.Current()
	info := ThreadInfo{ID: t.id, Cwd: t.cwd, Name: t.name, Effort: effort, ContextTokens: t.ctx, Busy: t.turns.Busy, TurnID: t.turnID,
		RunKind: t.runKind, ReadOnly: t.readOnly, SessionPath: t.sess.Path, LongContext: t.agent.LongContext()}
	SetModel(&info, m, t.models)
	info.AutoCompactLimit, info.AutoCompactCap = t.agent.CompactionLimit()
	total := t.total
	info.Usage = &total
	if t.turns.Busy {
		turn := t.turn
		info.Turn = &turn
	}
	info.Pending = t.pending()
	info.Goal = t.goalInfo()
	info.Jobs, info.Timers = t.jobCount, t.timerCount
	info.Activity = t.activityInfo()
	return info
}

// snapshot is the thread with its items, those in progress as they
// stand, and the event to follow it from. Lane only.
func (t *thread) snapshot() ThreadInfo {
	info := t.info()
	snap := t.uiRegistry().Snapshot()
	info.UI = &snap
	info.HasMore, info.Before = t.hasMore, t.before
	info.Items = make([]Item, 0, len(t.items))
	for _, it := range t.items {
		info.Items = append(info.Items, t.blocks.attach(it))
	}
	if t.turns.Busy || t.shell != nil {
		for _, it := range t.tr.Open() {
			info.Items = append(info.Items, t.blocks.attach(t.wire(&it)))
		}
	}
	slices.SortStableFunc(info.Items, func(a, b Item) int {
		return cmp.Compare(t.itemOrder[a.ID], t.itemOrder[b.ID])
	})
	if !t.ui.Empty() {
		info.ExtensionUI = WireExtensionUI(&t.ui)
	}
	if p := t.prompt; p != nil {
		w := p.wire
		info.Prompt = &w
	}
	info.EventID = t.s.eventSeq()
	info.ServerInstance = t.s.instance
	return info
}

// startedItem remembers display order separately from completion order:
// parallel tools and user shells can finish after items started later.
func (t *thread) startedItem(id string) {
	if t.itemOrder == nil {
		t.itemOrder = map[string]int{}
	}
	t.itemSeq++
	t.itemOrder[id] = t.itemSeq
}

// resetItemOrder adopts the replay's order after a branch or resume.
func (t *thread) resetItemOrder() {
	t.itemOrder, t.itemSeq = nil, 0
	for _, it := range t.items {
		t.startedItem(it.ID)
	}
}

// wire is the protocol form of a transcript item of this thread.
func (t *thread) wire(it *transcript.Item) Item {
	w := wireItem(t.id, it)
	if w.Type == ItemCommand && w.Shell {
		w.ContextPending = t.shellPending(it.ID)
	}
	return w
}

// updated tells clients the thread's settings or state changed.
func (t *thread) updated() {
	t.publish("thread/updated", map[string]any{"thread": t.info()})
}

// notice adds a notice of atto's own: kept for snapshots while the
// runtime lives, never saved. level is "", "warning" or "error".
func (t *thread) notice(level, format string, args ...any) {
	t.addNotice(Item{Level: level, Text: fmt.Sprintf(format, args...)})
}

// info adds an info notice: a title and text under it.
func (t *thread) infoNotice(title, text string) {
	t.addNotice(Item{Level: "info", Title: title, Text: text})
}

func (t *thread) errorNotice(err error) { t.notice("error", "Error: %s", err.Error()) }

// maxNotices bounds the notices kept for snapshots.
const maxNotices = 500

func (t *thread) addNotice(it Item) {
	t.notices++
	it.ID = fmt.Sprintf("%s-n%d", t.id, t.notices)
	it.Type, it.Status = ItemNotice, string(transcript.Completed)
	if len(t.attached) > 0 {
		t.startedItem(it.ID)
		t.items = append(t.items, it)
		t.trimItems()
	}
	if n := t.countNotices(); n > maxNotices {
		i := slices.IndexFunc(t.items, func(x Item) bool { return x.Type == ItemNotice && x.Title == "" && x.Level != "info" })
		if i >= 0 {
			delete(t.itemOrder, t.items[i].ID)
			t.items = slices.Delete(t.items, i, i+1)
		}
	}
	t.publish("item/completed", map[string]any{"turnId": t.turnID, "item": it})
}

func (t *thread) countNotices() int {
	n := 0
	for _, it := range t.items {
		if it.Type == ItemNotice {
			n++
		}
	}
	return n
}

// recover gives input back to the client it came from, to edit: in
// front of its draft, or only into an empty editor (ifEmpty).
func (t *thread) recover(client string, ifEmpty bool, texts []string, imgs []provider.Image) {
	if len(texts) == 0 && len(imgs) == 0 {
		return
	}
	text := joinTexts(texts)
	t.publish("input/recovered", map[string]any{"clientId": client, "text": text, "images": wireRecovered(imgs), "ifEmpty": ifEmpty})
}

func joinTexts(texts []string) string { return strings.Join(texts, "\n\n") }

func wireRecovered(imgs []provider.Image) []ItemImage {
	return wireImages(imgs)
}

// pending is the input the turn has not taken (nil when none).
func (t *thread) pending() *PendingInput {
	if len(t.steers) == 0 && len(t.turns.Queued) == 0 && !t.turns.QueuePaused {
		return nil
	}
	p := &PendingInput{Steers: []string{}, Paused: t.turns.QueuePaused}
	for _, s := range t.steers {
		p.Steers = append(p.Steers, s.Text)
		p.Items = append(p.Items, PendingItem{ID: s.ID, Kind: "steer", Text: s.Text, ClientID: s.Client})
	}
	for _, q := range t.turns.Queued {
		p.Queued = append(p.Queued, q.Text)
		p.Items = append(p.Items, PendingItem{ID: q.ID, Kind: "queued", Text: q.Text, ClientID: q.Client, Images: wireImages(q.Images)})
	}
	return p
}

// pendingChanged tells clients what input is pending now.
func (t *thread) pendingChanged() {
	p := t.pending()
	if p == nil {
		p = &PendingInput{Steers: []string{}}
	}
	t.publish("turn/pending", map[string]any{"pending": p})
}

// gated reports whether automatic work waits: a prompt is open, or a
// client has a picker open (the terminal held events, the queue and goal
// turns while one was).
func (t *thread) gated() bool {
	if t.prompt != nil || t.login != nil || t.startSource != "" {
		return true
	}
	for _, n := range t.gates {
		if n > 0 {
			return true
		}
	}
	return false
}

// newInput makes a pending input of client's.
func (t *thread) newInput(client, text string, imgs []provider.Image) *pendingInput {
	t.inputSeq++
	return &pendingInput{ID: fmt.Sprintf("%s-in%d", t.id, t.inputSeq), Text: text, Images: imgs, Client: client}
}

// goalInfo is the goal as clients show it; nil without one.
func (t *thread) goalInfo() *GoalInfo {
	g := t.goal.Goal
	if g == nil {
		return nil
	}
	secs := t.goal.Elapsed()
	held := t.goal.Held()
	c := *g
	info := &GoalInfo{
		Objective: g.Objective, Status: string(g.Status), Label: g.Status.Label(),
		Indicator: g.Indicator(secs, held && !t.turns.Busy), Summary: g.Summary(), Note: g.Note,
		Tokens: goal.Tokens(g.TokensUsed), TokensUsed: g.TokensUsed,
		Elapsed: goal.FormatElapsed(secs), Seconds: secs, Held: held, Goal: &c,
	}
	if t.turns.Busy && t.goal.Active() {
		info.TurnStartedAt = t.runStart.UnixMilli()
	}
	return info
}

// goalChanged publishes the goal when it changed (goal/updated; null when
// cleared).
func (t *thread) goalChanged() {
	g := t.goalInfo()
	if same := (g == nil) == (t.lastGoal == nil) && (g == nil || goalSame(*g, *t.lastGoal)); same {
		return
	}
	t.lastGoal = g
	t.publish("goal/updated", map[string]any{"goal": g})
}

func goalSame(a, b GoalInfo) bool {
	ag, bg := a.Goal, b.Goal
	a.Goal, b.Goal = nil, nil
	if a != b {
		return false
	}
	x, _ := json.Marshal(ag)
	y, _ := json.Marshal(bg)
	return string(x) == string(y)
}

// status publishes the job and timer counts when they changed.
func (t *thread) setCounts(jobs, timers int) {
	if jobs == t.jobCount && timers == t.timerCount {
		return
	}
	t.jobCount, t.timerCount = jobs, timers
	t.publish("thread/status", map[string]any{"jobs": jobs, "timers": timers})
}

// noModel reports that no model is set up yet.
func (t *thread) noModel() bool { return t.model().Model.ID == "" }

// errBusy and the other refusals clients get.
var (
	errNoTurn = failure(ReasonBusy, "no turn is running; use turn/start")
)
