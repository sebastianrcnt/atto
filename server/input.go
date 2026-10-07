package server

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
)

// Pending input follows codex-rs, as atto's terminal always did:
//
//   - Enter while a turn runs sends a steer, delivered into the running
//     turn after the next tool call (or when the model stops, which
//     continues the turn). Esc interrupts and sends pending steers at once.
//   - Tab queues a follow-up that starts as a new turn when the current
//     one ends. Taking back (Shift+Left) pulls the last steer not yet
//     delivered, else the last queued message, back into the editor.
//   - Ctrl+Enter interrupts the running turn and sends the draft as the
//     next turn, after the steers the turn has not taken.
//
// input/submit carries the intent: auto (Enter), queue (Tab), replace
// (Ctrl+Enter) or steer. Every accepted input gets an ID; input given back
// (a failed turn's message, steers after Ctrl+C, a takeback) goes to the
// client that sent it as input/recovered.

// Submit results.
const (
	StatusStarted   = "started"
	StatusSteered   = "steered"
	StatusQueued    = "queued"
	StatusDone      = "done"      // a command that ran, or nothing to do
	StatusRecovered = "recovered" // refused; the input went back (input/recovered)
)

// submitResult is input/submit's result.
type submitResult struct {
	InputID string `json:"inputId,omitempty"`
	Status  string `json:"status"`
	TurnID  string `json:"turnId,omitempty"`
}

// submit is input/submit on the lane.
func (t *thread) submit(client, text string, imgs []provider.Image, intent string) (submitResult, error) {
	if t.readOnly != "" && !readOnlyCommand(text) && strings.TrimSpace(text) != "" {
		return submitResult{}, failure(ReasonReadOnly, "%s", t.readOnly)
	}
	wasBusy, kind := t.busy, t.runKind
	steers, queued := len(t.steers), len(t.queued)
	turnSeq := t.turnSeq
	var in *pendingInput
	switch intent {
	case "", "auto":
		in = t.submitAuto(client, text, imgs)
	case "queue":
		in = t.submitQueue(client, text, imgs)
	case "replace":
		in = t.submitReplace(client, text, imgs)
	case "steer":
		if !t.busy || t.runKind != "turn" {
			return submitResult{}, errNoTurn
		}
		if strings.TrimSpace(text) == "" {
			return submitResult{}, invalid("input is required")
		}
		in = t.steer(client, text)
	default:
		return submitResult{}, invalid("unknown intent %q (auto, queue, replace or steer)", intent)
	}
	r := submitResult{Status: StatusDone}
	if in != nil {
		r.InputID = in.ID
	}
	switch {
	case t.turnSeq != turnSeq && t.busy && (!wasBusy || intent == "replace"):
		r.Status, r.TurnID = StatusStarted, t.turnID
	case wasBusy && kind == "turn" && len(t.steers) > steers:
		r.Status = StatusSteered
	case len(t.queued) > queued || (intent == "replace" && t.sendNow != nil):
		r.Status = StatusQueued
	}
	t.pendingChanged()
	return r, nil
}

// submitAuto is Enter: a shell command, a slash command, an empty prompt
// (resume a paused queue or a held goal), a steer while a turn runs,
// queued while anything else runs, else a new turn.
func (t *thread) submitAuto(client, text string, imgs []provider.Image) *pendingInput {
	if cmd, exclude, ok := parseShell(text); ok && len(imgs) == 0 {
		t.startShell(client, text, cmd, exclude)
		return nil
	}
	if len(imgs) > 0 && !strings.HasPrefix(text, "/") {
		return t.submitWithImages(client, text, imgs)
	}
	switch {
	case text == "":
		t.resumeQueue()
	case strings.HasPrefix(text, "/"):
		t.runCommand(client, text)
	case t.noModel():
		t.recover(client, false, []string{text}, nil)
		t.notice("", "%s", noModelHint)
	case t.busy && t.runKind == "turn":
		return t.steer(client, text)
	case t.busy:
		return t.enqueue(client, text, nil)
	default:
		in := t.newInput(client, text, nil)
		t.runTurn(in, true)
		return in
	}
	return nil
}

// noModelHint is said when input arrives before a model is set up.
const noModelHint = "No model is set up yet: /login or /model."

// resumeQueue is Enter on an empty prompt: it resumes a paused queue, or
// else a goal waiting for the user.
func (t *thread) resumeQueue() {
	switch {
	case t.busy:
	case len(t.queued) > 0:
		t.queuePaused = false
		t.maybeSendNextQueued()
	case t.goal.Held():
		t.goal.Release()
		t.goalChanged()
		t.continueGoal()
	}
}

// submitQueue is Tab: queued while busy, at once when idle (a shell
// command always runs at once).
func (t *thread) submitQueue(client, text string, imgs []provider.Image) *pendingInput {
	if strings.TrimSpace(text) == "" && len(imgs) == 0 {
		return nil
	}
	if _, _, isShell := parseShell(text); !t.busy || isShell && len(imgs) == 0 {
		return t.submitAuto(client, text, imgs)
	}
	if len(imgs) > 0 && !t.model().Model.Images() {
		return t.submitWithImages(client, text, imgs) // says so, gives it back
	}
	return t.enqueue(client, text, imgs)
}

// submitReplace is Ctrl+Enter: while a turn runs, the draft interrupts it
// and goes out as a new turn, after the steers the turn has not taken.
// Otherwise it is Enter.
func (t *thread) submitReplace(client, text string, imgs []provider.Image) *pendingInput {
	_, _, isShell := parseShell(text)
	if !t.busy || t.runKind != "turn" || t.sendNow != nil || t.handoff.pending || t.noModel() ||
		strings.HasPrefix(text, "/") || isShell {
		return t.submitAuto(client, text, imgs)
	}
	if len(imgs) > 0 && !t.model().Model.Images() {
		return t.submitAuto(client, text, imgs) // says images are not supported, gives the draft back
	}
	if text == "" && len(t.steers) == 0 {
		return nil // nothing to send
	}
	var in *pendingInput
	if text != "" {
		in = t.newInput(client, text, imgs)
		t.sendNow = in
	}
	t.goal.Replace()
	t.sendSteers = len(t.steers) > 0
	t.cancel()
	return in
}

// submitWithImages sends a prompt with images. A running turn only takes
// text steers, so while busy it is queued as the next turn instead.
func (t *thread) submitWithImages(client, text string, imgs []provider.Image) *pendingInput {
	if !t.model().Model.Images() {
		m := t.model()
		t.notice("warning", "%s", images.Unsupported(m.Model.DisplayName(), "/model", "models.json"))
		t.recover(client, false, []string{text}, imgs)
		return nil
	}
	if t.busy {
		return t.enqueue(client, text, imgs)
	}
	in := t.newInput(client, text, imgs)
	t.runTurn(in, true)
	return in
}

func (t *thread) steer(client, text string) *pendingInput {
	in := t.newInput(client, text, nil)
	t.agent.Steer(text)
	t.steers = append(t.steers, in)
	events.Wake(t.id) // `atto sleep` / `atto job wait` return early
	return in
}

func (t *thread) enqueue(client, text string, imgs []provider.Image) *pendingInput {
	in := t.newInput(client, text, imgs)
	t.queued = append(t.queued, in)
	t.maybeSendNextQueued()
	return in
}

// runTurn starts a turn with input in. typed: the user wrote it
// themselves (it comes back to them if the model never answers), as
// opposed to a skill, an extension's message or steers that came back.
func (t *thread) runTurn(in *pendingInput, typed bool) {
	for _, im := range in.Images {
		if len(im.Data) == 0 {
			continue
		}
		if err := images.Save(im); err != nil {
			t.errorNotice(fmt.Errorf("saving image: %w", err))
			t.recover(in.Client, false, []string{in.Text}, in.Images)
			return
		}
	}
	text, imgs := in.Text, in.Images
	t.feed(transcript.Input{Text: text, Images: imgs}, userMeta{client: in.Client, input: in.ID})
	// A goal that is not running by itself says so, to this message only.
	t.agent.SetInputNote(t.goal.StateNote())
	t.goal.UserInput() // a turn the user started: the goal waits for them after it
	t.recordSettings()
	t.start("turn", "Thinking", func(ctx context.Context, emit func(any)) error {
		return t.agent.RunWithImages(ctx, text, imgs, emit)
	})
	if typed { // after start, which forgets the last one
		t.typed = in
	}
}

// maybeSendNextQueued starts the next queued follow-up when idle. Queued
// slash commands that don't start a run are executed in order.
func (t *thread) maybeSendNextQueued() {
	for !t.busy && !t.gated() && !t.queuePaused && len(t.queued) > 0 {
		next := t.queued[0]
		t.queued = t.queued[1:]
		if strings.HasPrefix(next.Text, "/") {
			t.runCommand(next.Client, next.Text)
			continue
		}
		t.runTurn(next, true)
		t.pendingChanged()
		return
	}
	t.continueGoal()
}

// unsteer takes back pending input id (or, with "", the last steer, else
// the last queued message) and gives it to client.
func (t *thread) unsteer(client, id, text string, queued bool) (*pendingInput, error) {
	if id == "" && text != "" { // revision 1: by text
		list := t.steers
		if queued {
			list = t.queued
		}
		for _, p := range slices.Backward(list) {
			if p.Text == text {
				id = p.ID
				break
			}
		}
		if id == "" {
			if queued {
				return nil, failure(ReasonAlreadyCommitted, "that message is no longer queued: it has started")
			}
			return nil, failure(ReasonAlreadyCommitted, "that message is no longer pending: the turn has taken it")
		}
	}
	if id == "" {
		switch {
		case len(t.steers) > 0:
			id = t.steers[len(t.steers)-1].ID
		case len(t.queued) > 0:
			id = t.queued[len(t.queued)-1].ID
		default:
			return nil, failure(ReasonAlreadyCommitted, "nothing is pending")
		}
	}
	if i := slices.IndexFunc(t.steers, func(p *pendingInput) bool { return p.ID == id }); i >= 0 {
		p := t.steers[i]
		if !t.agent.Unsteer(p.Text) {
			return nil, failure(ReasonAlreadyCommitted, "that message is no longer pending: the turn has taken it")
		}
		t.steers = slices.Delete(t.steers, i, i+1)
		t.pendingChanged()
		return p, nil
	}
	if i := slices.IndexFunc(t.queued, func(p *pendingInput) bool { return p.ID == id }); i >= 0 {
		p := t.queued[i]
		t.queued = slices.Delete(t.queued, i, i+1)
		t.pendingChanged()
		return p, nil
	}
	return nil, failure(ReasonAlreadyCommitted, "that message is no longer pending: it has started")
}

// interrupt is Esc while something runs: it stops a user's shell command,
// else the run (pending steers then go out at once, mode sendPending), or
// pauses a goal retry waiting. False when nothing runs.
func (t *thread) interrupt(mode string) bool {
	if t.cancelShell() {
		return true
	}
	if !t.busy {
		return t.interruptGoalRetry()
	}
	if mode != "cancel" && len(t.steers) > 0 {
		t.sendSteers = true
	}
	t.cancel()
	return true
}

// readOnlyAllowed are the commands that work on a read-only session.
var readOnlyAllowed = []string{"quit", "exit", "resume", "clear"}

func readOnlyCommand(text string) bool {
	rest, ok := strings.CutPrefix(text, "/")
	if !ok {
		return false
	}
	name, _, _ := strings.Cut(rest, " ")
	for _, c := range readOnlyAllowed {
		if name != "" && strings.HasPrefix(c, name) {
			return true
		}
	}
	return false
}
