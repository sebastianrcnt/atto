package server

import (
	"context"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"runtime/debug"
)

// threadCall serves the requests that act on one thread; ok is false for
// a method it does not know. State is resolved on the thread's lane;
// resource I/O, status commands, profiles and archive cleanup run off it.
func (s *Server) threadCall(ctx context.Context, client, method string, p threadParams) (out any, ok bool, err error) {
	t, err := s.thread(p.ThreadID)
	if err != nil {
		if _, known := threadMethods[method]; known {
			return nil, true, err
		}
		return nil, false, nil
	}
	h, known := threadMethods[method]
	if !known {
		return nil, false, nil
	}
	err = t.call(func() error {
		if t.closing {
			return errThreadClosed
		}
		var e error
		out, e = h(t, client, p)
		return e
	})
	if err == nil {
		if method == "thread/files" {
			out, err = threadFiles(ctx, out.(string), p.Query, p.Limit)
		} else if r, ok := out.(statusLineRequest); ok {
			out, err = runStatusLine(ctx, r)
		} else if method == "thread/debug" {
			out = debugProfiles()
		} else if r, ok := out.(resourceRequest); ok {
			out, err = readResource(r)
		}
	}
	return out, true, err
}

// threadMethods are the requests on a thread; they run on its lane.
var threadMethods = map[string]func(t *thread, client string, p threadParams) (any, error){
	"thread/entry": func(t *thread, client string, p threadParams) (any, error) {
		e, ok, err := session.ReadEntry(t.sess.Path, p.EntryID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, failure(ReasonNotFound, "entry not found")
		}
		return e, nil
	},
	"thread/items": func(t *thread, client string, p threadParams) (any, error) {
		b := transcript.Builder{IDPrefix: itemPrefix(t.id)}
		page, _, err := replayFile(&b, t.id, t.sess.Path, p.Before, p.Limit, t.pageAnchor(p.Before)...)
		debug.FreeOSMemory()
		return page, err
	},
	"thread/statusLine": func(t *thread, client string, p threadParams) (any, error) {
		return t.statusLine()
	},
	"thread/debug": func(t *thread, client string, p threadParams) (any, error) {
		return nil, nil
	},
	"auth/list": func(t *thread, client string, p threadParams) (any, error) {
		return t.authList(), nil
	},
	"auth/login": func(t *thread, client string, p threadParams) (any, error) {
		return t.authLogin(p)
	},
	"auth/logout": func(t *thread, client string, p threadParams) (any, error) {
		if t.readOnly != "" {
			return nil, failure(ReasonReadOnly, "%s", t.readOnly)
		}
		if t.login != nil {
			return nil, failure(ReasonBusy, "cancel the pending login first")
		}
		removed, err := config.RemoveAuth(p.Provider)
		if err != nil {
			return nil, err
		}
		err = t.reloadModels()
		return map[string]any{"removed": removed}, err
	},
	"auth/cancel": func(t *thread, client string, p threadParams) (any, error) {
		if t.login != nil {
			t.login.cancel()
		}
		return nil, nil
	},
	"thread/files": func(t *thread, client string, p threadParams) (any, error) {
		return t.cwd, nil
	},
	"item/image": func(t *thread, client string, p threadParams) (any, error) {
		return t.itemResource(p, "image")
	},
	"item/output": func(t *thread, client string, p threadParams) (any, error) {
		return t.itemResource(p, "output")
	},
	"agent/turn": func(t *thread, client string, p threadParams) (any, error) {
		if t.mgd == nil {
			return nil, failure(ReasonUnsupported, "this session is not an agent that this runtime runs turns of")
		}
		return t.mgd.accept(p.Turn)
	},
	"worker/state": func(t *thread, client string, p threadParams) (any, error) {
		busy := t.turns.Busy || t.shell != nil
		state := "idle"
		if busy {
			state = "working"
		}
		if t.prompt != nil || (t.goal.Active() && t.goal.Held()) {
			state = "waiting"
		}
		return map[string]any{"id": t.s.instance, "session": t.id, "name": t.name, "state": state, "cwd": t.cwd, "clients": len(t.attached), "busy": busy, "openPrompt": t.prompt != nil, "goalWaiting": t.goal.Goal != nil && (t.goal.Held() || t.goal.Goal.Status == goal.Paused || t.goal.Goal.Status == goal.Blocked || t.goal.Goal.Status == goal.UsageLimited), "version": t.s.Version, "pid": os.Getpid()}, nil
	},
	"mcp/list": func(t *thread, client string, p threadParams) (any, error) {
		if t.mcp == nil {
			return map[string]any{"servers": []any{}}, nil
		}
		servers, err := t.mcp.Servers(context.Background())
		return map[string]any{"servers": servers}, err
	},
	"thread/sessionStart": func(t *thread, client string, p threadParams) (any, error) {
		if source := t.startSource; source != "" {
			t.startSource = ""
			t.sessionStart(source)
			t.askMCPApprovals()
			t.deliverEvents()
			t.maybeSendNextQueued()
		}
		return nil, nil
	},

	"input/submit": func(t *thread, client string, p threadParams) (any, error) {
		imgs, err := inputImages(p.Images)
		if err != nil {
			return nil, err
		}
		return t.submit(client, p.Input, imgs, p.Intent)
	},
	"turn/start": func(t *thread, client string, p threadParams) (any, error) {
		if strings.TrimSpace(p.Input) == "" && len(p.Images) == 0 {
			return nil, invalid("input is required")
		}
		if t.turns.Busy {
			return nil, failure(ReasonBusy, "a turn is already running; use turn/steer or turn/interrupt")
		}
		if t.readOnly != "" {
			return nil, failure(ReasonReadOnly, "%s", t.readOnly)
		}
		if t.noModel() {
			return nil, failure(ReasonNoModel, "%s", noModelHint)
		}
		imgs, err := turnImages(p.Images, t.model())
		if err != nil {
			return nil, err
		}
		in := t.newInput(client, images.WithPlaceholders(p.Input, imgs), imgs)
		t.runTurn(in, true)
		return map[string]any{"turnId": t.turnID, "inputId": in.ID}, nil
	},
	"turn/steer": func(t *thread, client string, p threadParams) (any, error) {
		if !t.turns.Busy || t.runKind != "turn" {
			return nil, errNoTurn
		}
		if strings.TrimSpace(p.Input) == "" {
			return nil, invalid("input is required")
		}
		in := t.steer(client, p.Input)
		t.pendingChanged()
		return map[string]any{"inputId": in.ID}, nil
	},
	"turn/unsteer": func(t *thread, client string, p threadParams) (any, error) {
		in, err := t.unsteer(client, p.InputID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"inputId": in.ID, "text": in.Text, "images": wireImages(in.Images), "clientId": in.Client}, nil
	},
	"turn/interrupt": func(t *thread, client string, p threadParams) (any, error) {
		if p.Mode != "" && p.Mode != "cancel" && p.Mode != "sendPending" {
			return nil, invalid("mode is cancel or sendPending")
		}
		return map[string]any{"interrupted": t.interrupt(p.Mode)}, nil
	},
	"turn/background": func(t *thread, client string, p threadParams) (any, error) {
		if !t.backgroundShell() && (!t.turns.Busy || !t.agent.Background()) {
			return nil, failure(ReasonUnsupported, "no command is running that can move to the background")
		}
		return map[string]any{"accepted": true}, nil
	},
	"queue/resume": func(t *thread, client string, p threadParams) (any, error) {
		t.resumeQueue()
		t.pendingChanged()
		return nil, nil
	},
	"shell/start": func(t *thread, client string, p threadParams) (any, error) {
		cmd := strings.TrimSpace(p.Command)
		if cmd == "" {
			return nil, invalid("command is required")
		}
		if t.readOnly != "" {
			return nil, failure(ReasonReadOnly, "%s", t.readOnly)
		}
		prefix := "!"
		if p.Exclude {
			prefix = "!!"
		}
		t.startShell(client, prefix+cmd, cmd, p.Exclude)
		return nil, nil
	},
	"shell/interrupt": func(t *thread, client string, p threadParams) (any, error) {
		return map[string]any{"interrupted": t.cancelShell()}, nil
	},
	"thread/compact": func(t *thread, client string, p threadParams) (any, error) {
		if t.turns.Busy {
			return nil, failure(ReasonBusy, "a turn is already running; use turn/steer or turn/interrupt")
		}
		t.recordSettings()
		t.start("compact", "Compacting context", t.agent.Compact)
		return map[string]any{"turnId": t.turnID}, nil
	},
	"thread/setModel": func(t *thread, client string, p threadParams) (any, error) {
		ref, ok := t.models.Find("", p.Model)
		if !ok {
			// The models may have changed on disk (a login).
			if _, models, err := core.Load(); err == nil {
				t.models = models
				ref, ok = models.Find("", p.Model)
			}
		}
		if !ok {
			return nil, invalid("unknown model %q", p.Model)
		}
		t.setModel(ref, p.SaveDefault)
		if notice := config.PriceTierNotice(ref); notice != "" {
			t.notice("", "%s", notice)
		}
		if !p.SaveDefault { // the API's: recorded at once, as before
			t.sess.Append(session.Entry{Type: session.TypeModel, Provider: ref.ProviderName, Model: ref.Model.ID})
			t.recModel = ref.ProviderName + "/" + ref.Model.ID
		}
		return t.info(), nil
	},
	"models/reload": func(t *thread, client string, p threadParams) (any, error) {
		return nil, t.reloadModels()
	},

	"thread/setEffort": func(t *thread, client string, p threadParams) (any, error) {
		if err := core.CheckEffort(t.model(), p.Effort); err != nil {
			return nil, invalid("%v", err)
		}
		t.setEffort(p.Effort, p.SaveDefault)
		if !p.SaveDefault {
			t.sess.Append(session.Entry{Type: session.TypeEffort, Effort: p.Effort})
			t.recEffort = p.Effort
		}
		return t.info(), nil
	},
	"thread/setContextMode": func(t *thread, client string, p threadParams) (any, error) {
		switch p.ContextMode {
		case "long", "normal":
		default:
			return nil, invalid("contextMode is normal or long")
		}
		t.setContextMode(p.ContextMode == "long")
		return t.info(), nil
	},
	"thread/setName": func(t *thread, client string, p threadParams) (any, error) {
		if strings.TrimSpace(p.Name) == "" {
			return nil, invalid("name is required")
		}
		t.nameSession(strings.TrimSpace(p.Name))
		return t.info(), t.sess.Err()
	},
	"thread/setLabel": func(t *thread, client string, p threadParams) (any, error) {
		t.sess.Append(session.Entry{Type: session.TypeLabel, TargetID: p.EntryID, Label: p.Label})
		return nil, t.sess.Err()
	},
	"thread/rollback": func(t *thread, client string, p threadParams) (any, error) {
		return t.rollback(client, p.NumTurns)
	},
	"thread/navigate": func(t *thread, client string, p threadParams) (any, error) {
		if p.EntryID == "" {
			return nil, invalid("entryId is required")
		}
		if _, _, ok, err := session.BranchPointFile(t.sess.Path, p.EntryID); err != nil || !ok {
			return nil, failure(ReasonNotFound, "that entry is no longer in the session")
		}
		if p.Summary != nil && p.Summary.Mode != "" && p.Summary.Mode != "none" && p.Summary.Mode != "auto" && p.Summary.Mode != "custom" {
			return nil, invalid("summary mode is none, auto or custom")
		}
		var sum *summaryRequest
		if s := p.Summary; s != nil && s.Mode != "" && s.Mode != "none" {
			sum = &summaryRequest{instructions: strings.TrimSpace(s.Instructions)}
		}
		t.moveTo(p.EntryID, sum, client)
		return nil, nil
	},
	"thread/fork": func(t *thread, client string, p threadParams) (any, error) {
		if t.turns.Busy {
			return nil, failure(ReasonBusy, "Still working — press esc to interrupt first.")
		}
		path, id, text, imgs, err := t.fork(p.EntryID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": path, "threadId": id, "input": text, "images": wireImages(imgs)}, nil
	},
	"thread/tree": func(t *thread, client string, p threadParams) (any, error) {
		if p.Query != "" {
			matches, err := session.SearchTree(t.sess.Path, p.Query)
			return map[string]any{"matches": matches}, err
		}
		rows, err := session.ReadTreeRows(t.sess.Path)
		debug.FreeOSMemory()
		return map[string]any{"entries": rows, "leaf": t.sess.Leaf()}, err
	},
	"thread/context": func(t *thread, client string, p threadParams) (any, error) {
		return t.contextInfo(p.View), nil
	},
	"thread/reload": func(t *thread, client string, p threadParams) (any, error) {
		t.requestReload(false)
		return nil, nil
	},
	"thread/debugRequests": func(t *thread, client string, p threadParams) (any, error) {
		sets := map[string][]ai.SentRequest{"recent": ai.RecentRequests()}
		maps.Copy(sets, ai.PinnedRequests())
		return map[string]any{"sets": sets}, nil
	},
	"thread/debugRequest": func(t *thread, client string, p threadParams) (any, error) {
		body := t.agent.LastRequest()
		if body == nil {
			return map[string]any{}, nil
		}
		return map[string]any{"request": string(body)}, nil
	},
	"thread/handoff": func(t *thread, client string, p threadParams) (any, error) {
		return nil, t.startHandoff()
	},
	"goal/read": func(t *thread, client string, p threadParams) (any, error) {
		t.goal.Poll()
		return map[string]any{"goal": t.goalInfo()}, nil
	},
	"goal/set": func(t *thread, client string, p threadParams) (any, error) {
		t.setGoal(client, p.Input)
		return map[string]any{"goal": t.goalInfo()}, nil
	},
	"goal/edit": func(t *thread, client string, p threadParams) (any, error) {
		if err := t.setObjective(p.Input); err != nil {
			return nil, invalid("%v", err)
		}
		return map[string]any{"goal": t.goalInfo()}, nil
	},
	"goal/pause": func(t *thread, client string, p threadParams) (any, error) {
		t.pauseGoal()
		return map[string]any{"goal": t.goalInfo()}, nil
	},
	"goal/resume": func(t *thread, client string, p threadParams) (any, error) {
		t.resumeGoalCmd()
		return map[string]any{"goal": t.goalInfo()}, nil
	},
	"goal/clear": func(t *thread, client string, p threadParams) (any, error) {
		t.clearGoal()
		return nil, nil
	},
	"commands/list": func(t *thread, client string, p threadParams) (any, error) {
		return map[string]any{"commands": t.commands()}, nil
	},
	"commands/run": func(t *thread, client string, p threadParams) (any, error) {
		text := "/" + strings.TrimPrefix(p.Name, "/")
		if p.Args != "" {
			text += " " + p.Args
		}
		return t.submit(client, text, nil, "auto")
	},
	"prompt/clientOpen": func(t *thread, client string, p threadParams) (any, error) {
		if p.Prompt == nil {
			return nil, invalid("prompt is required")
		}
		return t.openClientPrompt(client, *p.Prompt)
	},
	"prompt/clientClose": func(t *thread, client string, p threadParams) (any, error) {
		t.withdrawClientPrompt(client, p.ID)
		return nil, nil
	},
	"prompt/answer": func(t *thread, client string, p threadParams) (any, error) {
		if p.ID == "" {
			return nil, invalid("id is required")
		}
		if !p.Cancel && p.Index == nil && p.Indexes == nil && p.Text == nil {
			return nil, invalid("index, indexes, text or cancel is required")
		}
		return nil, t.answerPrompt(client, p.ID, PromptAnswer{Index: p.Index, Indexes: p.Indexes, Text: p.Text, Cancel: p.Cancel})
	},
	"client/gate": func(t *thread, client string, p threadParams) (any, error) {
		if p.Open {
			t.gates[client]++
			return nil, nil
		}
		if t.gates[client] > 0 {
			t.gates[client]--
		}
		if t.gates[client] == 0 {
			delete(t.gates, client)
			t.deliverEvents()
			t.maybeSendNextQueued()
			t.askMCPApprovals()
		}
		return nil, nil
	},
	"job/list":   bg("job/list"),
	"job/output": bg("job/output"),
	"job/stop":   bg("job/stop"),
	"agent/list": bg("agent/list"),
	"agent/tree": bg("agent/tree"),
	"agent/read": bg("agent/read"),
	"job/stopAll": func(t *thread, client string, p threadParams) (any, error) {
		n := jobs.KillAll(t.id)
		t.setCounts(0, t.timerCount)
		return map[string]any{"stopped": n}, nil
	},
	"timer/list": func(t *thread, client string, p threadParams) (any, error) {
		out := []Timer{}
		for _, tm := range events.Timers(t.id) {
			x := Timer{ID: tm.ID, Due: tm.Due.UnixMilli(), Message: tm.Message}
			if tm.Recurring() {
				x.Schedule = tm.Schedule()
			}
			out = append(out, x)
		}
		return map[string]any{"timers": out}, nil
	},
	"timer/create": func(t *thread, client string, p threadParams) (any, error) {
		due, err := events.ParseWhen(p.When, time.Now())
		if err != nil {
			return nil, invalid("%v", err)
		}
		if strings.TrimSpace(p.Message) == "" {
			return nil, invalid("message is required")
		}
		tm, err := events.AddTimer(t.id, due, strings.TrimSpace(p.Message))
		if err != nil {
			return nil, err
		}
		t.setCounts(t.jobCount, t.timerCount+1)
		return map[string]any{"timer": Timer{ID: tm.ID, Due: tm.Due.UnixMilli(), Message: tm.Message}}, nil
	},
	"timer/cancel": func(t *thread, client string, p threadParams) (any, error) {
		return nil, events.CancelTimer(t.id, p.ID)
	},
}

func bg(method string) func(t *thread, client string, p threadParams) (any, error) {
	return func(t *thread, client string, p threadParams) (any, error) { return background(method, t.id, p) }
}

// inputImages are input/submit's images: base64 data, or (from a client
// on this machine) the name of a file already in the image store.
func inputImages(in []ImageInput) ([]provider.Image, error) {
	if len(in) > maxTurnImages {
		return nil, invalid("too many images (%d, at most %d)", len(in), maxTurnImages)
	}
	var out []provider.Image
	for i, x := range in {
		if x.Data == "" && x.File != "" {
			im, err := storedImage(x)
			if err != nil {
				return nil, invalid("image %d: %v", i+1, err)
			}
			out = append(out, im)
			continue
		}
		im, err := decodeImage(x)
		if err != nil {
			return nil, invalid("image %d: %v", i+1, err)
		}
		out = append(out, im)
	}
	return out, nil
}

// rollback is thread/rollback, named after codex's method: back to before
// the numTurns-th last user message (default 1), as /tree does; idle only.
// The result carries the message text as "input".
func (t *thread) rollback(client string, n int) (any, error) {
	if n == 0 {
		n = 1
	}
	if n < 0 {
		return nil, invalid("numTurns must be positive")
	}
	if t.turns.Busy {
		return nil, failure(ReasonBusy, "a turn is running; turn/interrupt first")
	}
	var users []session.Entry
	if err := session.VisitActive(t.sess.Path, func(e session.Entry) error {
		if len(UserMessages([]session.Entry{e})) > 0 {
			users = append(users, session.Entry{ID: e.ID})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if n > len(users) {
		return nil, invalid("only %d user messages to roll back", len(users))
	}
	target := users[len(users)-n]
	leaf, text, ok, readErr := session.BranchPointFile(t.sess.Path, target.ID)
	if readErr != nil {
		return nil, readErr
	}
	if !ok {
		return nil, invalid("that message is no longer in the session")
	}
	t.stashPending()
	t.sess.Branch(leaf)
	if err := t.sess.Err(); err != nil {
		return nil, err
	}
	t.showBranchDisk()
	info := t.snapshot()
	return rollbackResult{rollbackInfo(info), text}, nil
}

func (t *thread) reloadModels() error {
	_, models, err := core.Load()
	if err != nil {
		return err
	}
	t.models = models
	if m := t.model(); m.Model.ID != "" {
		if ref, ok := models.Find(m.ProviderName, m.Model.ID); ok {
			t.agent.SetModel(ref)
		}
	}
	t.updated()
	return nil
}

// Named alias prevents a snapshot marshaler from swallowing rollback input.
type rollbackInfo ThreadInfo
type rollbackResult struct {
	rollbackInfo
	Input string `json:"input"`
}
