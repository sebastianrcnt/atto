package agent

import (
	"maps"
	"slices"
	"time"

	"github.com/sebastianrcnt/atto/ai"
)

// Parsing the arguments is quadratic over a long call, so after the first
// few kilobytes it happens at most this often; the call's last state
// arrives with its ToolStart anyway.
const (
	draftParseEvery = 100 * time.Millisecond
	draftParseFree  = 4 * 1024
)

// draftTracker turns the provider's tool call deltas into ToolDraft
// events and makes sure each draft ends: with the ToolStart of its call
// (claim) or a ToolDraftEnd. One per model response.
type draftTracker struct {
	emit  func(any)
	calls map[int]*draftCall
}

type draftCall struct {
	last   time.Time
	args   BashArgs
	closed bool
}

func (d *draftTracker) start(index int) {
	if d.calls == nil {
		d.calls = map[int]*draftCall{}
	}
	d.calls[index] = &draftCall{}
	d.emit(ToolDraft{Index: index})
}

// delta applies the arguments received so far.
func (d *draftTracker) delta(index int, raw string) {
	c := d.calls[index]
	if c == nil || c.closed {
		return
	}
	now := time.Now()
	if len(raw) > draftParseFree && now.Sub(c.last) < draftParseEvery {
		return
	}
	c.last = now
	m := ai.ParseStreamingJSON(raw)
	var args BashArgs
	args.Description, _ = m["description"].(string)
	args.Command, _ = m["command"].(string)
	// Keep the draft header stable until the model actually streams a
	// description. runTool supplies the command-first-line fallback once the
	// call is complete.
	if args == c.args {
		return
	}
	c.args = args
	d.emit(ToolDraft{Index: index, Args: args})
}

// claim notes that the call's ToolStart took over its draft.
func (d *draftTracker) claim(index int) {
	if c := d.calls[index]; c != nil {
		c.closed = true
	}
}

// end drops the draft of a call that will not run.
func (d *draftTracker) end(index int, err string) {
	if c := d.calls[index]; c != nil && !c.closed {
		c.closed = true
		d.emit(ToolDraftEnd{Index: index, Err: err})
	}
}

// endAll drops every draft still open.
func (d *draftTracker) endAll() {
	for _, i := range slices.Sorted(maps.Keys(d.calls)) {
		d.end(i, "")
	}
}
