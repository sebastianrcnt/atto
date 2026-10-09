package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/tui"
)

// gap renders a blank line before a block, except at the very top.
type gap struct{ tui.Component }

func (g gap) Render(width int) []string {
	return append([]string{""}, g.Component.Render(width)...)
}

// Spaced lets a tui.Container draw the blank line, saving the copy.
func (g gap) Spaced() (tui.Component, int) { return g.Component, 1 }

// userBlock shows a submitted prompt on a shaded band, codex-style, with a
// blank shaded row above and below.
type userBlock struct {
	text string
	// remote marks a message sent from another client of the session.
	remote     bool
	cache, pin tui.RenderCache[string]
}

// userBG is the band color: a dark gray that reads on dark themes and
// stays subtle on light ones.
const userBG = 237

func band(content string, width int) string {
	return tui.BG(userBG, content+strings.Repeat(" ", max(0, width-tui.VisibleWidth(content))))
}

func (u *userBlock) Render(width int) []string {
	return u.cache.Render(width, u.text, func() []string { return u.render(width) })
}

func (u *userBlock) render(width int) []string {
	lines := tui.Wrap(tui.StripControls(u.text), max(1, width-2))
	top := ""
	if u.remote {
		top = tui.Dim("  from remote")
	}
	out := []string{band(top, width)}
	for i, l := range lines {
		lead := "  "
		if i == 0 {
			lead = tui.FG(6, "› ")
		}
		out = append(out, band(lead+l, width))
	}
	return append(out, band("", width))
}

// pinLine renders the one-line pinned form of a prompt.
func (u *userBlock) pinLine(width int) string {
	return u.pin.Render(width, u.text, func() []string {
		text := strings.Join(strings.Fields(tui.StripControls(u.text)), " ")
		return []string{band(tui.Truncate(tui.FG(6, "› ")+text, width, "…"), width)}
	})[0]
}

// textBlock shows assistant text rendered as markdown, indented under the
// prompt. Output is cached until the text or width changes.
type textBlock struct {
	text strings.Builder
	// disp is what extensions changed about how the block shows (see
	// blockdisplay.go): a status and a replacement text.
	disp  blockDisplay
	cache tui.RenderCache[textKey]
}

func (t *textBlock) Click(line int) bool { return t.disp.click(line) }

func (t *textBlock) Render(width int) []string {
	ver, orig := t.disp.key()
	return t.cache.Render(width, textKey{t.text.String(), ver, orig}, func() []string { return t.render(width) })
}

// textKey is what a text block's lines depend on: its text, and what
// extensions changed (see blockDisplay.key).
type textKey struct {
	text string
	ver  int
	orig bool
}

func (t *textBlock) render(width int) []string {
	lines := tui.Markdown(strings.TrimSpace(tui.StripControls(t.disp.shown(t.text.String()))), max(1, width-2))
	for i, l := range lines {
		if i == 0 && !startsWithMarker(l) {
			lines[i] = tui.Dim("• ") + l
		} else {
			lines[i] = "  " + l
		}
	}
	// The status and the toggle share a line under the text.
	t.disp.metaLine = -1
	if toggle, status := t.disp.toggleLine(width), t.disp.header(); toggle != "" || status != "" {
		t.disp.metaLine = len(lines)
		if toggle == "" {
			t.disp.metaLine = -1
			toggle = tui.Dim("  ·")
		}
		lines = append(lines, tui.Truncate(toggle+status, width, "…"))
	}
	return lines
}

const (
	thinkingPreviewLines = 6
	toolPreviewLines     = 5 // codex: TOOL_CALL_MAX_LINES, the "… +N lines" row included
	toolKeepBytes        = 64 * 1024
)

// disclosure renders the expand/collapse affordance shared by collapsible
// blocks: "+ N lines" when collapsed, "− Show less" when expanded.
func disclosure(expanded bool, hidden int, what string) string {
	switch {
	case expanded:
		return tui.Dim("    − Show less (click)")
	case hidden > 0:
		return tui.Dim(fmt.Sprintf("    + %d %s (click or ctrl+t to expand)", hidden, what))
	}
	return ""
}

// details is the global "expand everything" state toggled by ctrl+t. Each
// toggle bumps gen, which resets per-block choices.
type details struct {
	on  bool
	gen int
}

// expander holds a block's expanded state: a click sets a per-block choice
// that overrides the global ctrl+t state until ctrl+t is pressed again.
type expander struct {
	d   *details
	set bool
	val bool
	gen int
}

func (e *expander) expanded() bool {
	if e.set && (e.d == nil || e.gen == e.d.gen) {
		return e.val
	}
	return e.d != nil && e.d.on
}

func (e *expander) toggle() { e.setTo(!e.expanded()) }

// setTo makes the block's own choice v.
func (e *expander) setTo(v bool) {
	e.set, e.val = true, v
	if e.d != nil {
		e.gen = e.d.gen
	}
}

// clickable records, at render time, which lines of a block toggle it: its
// header and its disclosure line, and only when there is something to
// expand or collapse. A click anywhere else in the block (to start a
// selection, say) leaves it alone.
type clickable struct {
	more  bool // something to expand or collapse
	lines int  // lines rendered
	foot  bool // the last line is a disclosure line
	mid   int  // a disclosure line inside the block (the "… +N lines" of a tool's output), or 0
}

func (c *clickable) clicks(more bool, out []string, foot bool) []string {
	c.more, c.lines, c.foot, c.mid = more, len(out), foot, 0
	return out
}

func (c clickable) hit(line int) bool {
	return c.more && (line == 0 || c.foot && line == c.lines-1 || c.mid > 0 && line == c.mid)
}

// gapped components forward clicks past the leading blank line.
func (g gap) Click(line int) bool {
	if c, ok := g.Component.(tui.Clickable); ok && line > 0 {
		return c.Click(line - 1)
	}
	return false
}

// thinkingBlock streams reasoning dimmed, then collapses to one line that
// expands on click.
type thinkingBlock struct {
	expander
	clickable
	disp  blockDisplay // see blockdisplay.go
	text  strings.Builder
	start time.Time
	dur   time.Duration
	done  bool
	cache tui.RenderCache[thinkingKey]
}

// thinkingKey is what a thinking block's lines depend on.
type thinkingKey struct {
	text           string
	dur            time.Duration
	done, expanded bool
	dver           int  // blockDisplay.key: what extensions changed
	orig           bool //
}

func (t *thinkingBlock) finish() {
	if !t.done {
		t.done = true
		t.dur = time.Since(t.start)
	}
}

func (t *thinkingBlock) Click(line int) bool {
	if t.disp.click(line) {
		return true
	}
	if !t.hit(line) {
		return false
	}
	t.toggle()
	return true
}

func (t *thinkingBlock) Render(width int) []string {
	ver, orig := t.disp.key()
	key := thinkingKey{text: t.text.String(), dur: t.dur, done: t.done, expanded: t.expanded(), dver: ver, orig: orig}
	return t.cache.Render(width, key, func() []string { return t.render(width) })
}

// render adds what extensions changed to the block: statuses on the
// header and a line under it to flip to the original.
func (t *thinkingBlock) render(width int) []string {
	out := t.renderText(width)
	if h := t.disp.header(); h != "" {
		out[0] = tui.Truncate(out[0]+h, width, "…")
	}
	t.disp.metaLine = -1
	if toggle := t.disp.toggleLine(width); toggle != "" {
		t.disp.metaLine = 1
		out = append(out[:1:1], append([]string{toggle}, out[1:]...)...)
		t.lines++
	}
	return out
}

func (t *thinkingBlock) renderText(width int) []string {
	text := strings.TrimSpace(tui.StripControls(t.disp.shown(t.text.String())))
	has := text != "" // Wrap("") is one empty line
	body := tui.Wrap(text, max(1, width-4))
	style := func(s string) string { return tui.Dim(tui.Italic(s)) }
	expanded := t.expanded()
	if !t.done {
		// While streaming: the last few lines, or everything once clicked.
		head := style("  ∴ Thinking…")
		hidden := 0
		if !expanded && len(body) > thinkingPreviewLines {
			hidden = len(body) - thinkingPreviewLines
			body = body[hidden:]
		}
		if hidden > 0 {
			head += tui.Dim(fmt.Sprintf(" · %d earlier lines · click to expand", hidden))
		} else if expanded {
			head += tui.Dim(" · click to collapse")
		}
		out := []string{tui.Truncate(head, width, "…")}
		for _, l := range body {
			out = append(out, "    "+style(l))
		}
		return t.clicks(hidden > 0 || expanded, out, false)
	}
	head := style(fmt.Sprintf("  ∴ Thought for %s", tui.FormatDuration(t.dur)))
	if !expanded {
		if has {
			head += tui.Dim(" · click to expand")
		}
		return t.clicks(has, []string{tui.Truncate(head, width, "…")}, false)
	}
	out := []string{head}
	for _, l := range body {
		out = append(out, "    "+style(l))
	}
	return t.clicks(has, append(out, disclosure(true, 0, "")), true)
}

// toolBlock shows a bash call: the model's description, the command, the
// last few output lines and the outcome. Once the call is done it folds to
// one line, the description, outcome and command (with the last output
// lines when it failed: why it failed shows). A click on that line opens
// the command and the output's first and last lines, a click on "… +N
// lines" the whole output; ctrl+t opens every block in full.
type toolBlock struct {
	expander          // the whole output
	open     expander // more than one line, once done
	clickable
	args    agent.BashArgs
	timeout time.Duration
	pending bool // the model is still writing the call; start is when it ran
	start   time.Time
	output  strings.Builder
	total   int // total output bytes seen
	done    bool
	res     agent.BashResult
	images  []string // what atto view attached, as images.ViewLabel says
	cache   tui.RenderCache[toolKey]
}

// toolKey is what a tool block's lines depend on, but for the clock.
type toolKey struct {
	args                    agent.BashArgs
	timeout                 time.Duration
	output                  string
	total                   int
	pending, done, expanded bool
	open                    bool
	res                     agent.BashResult // without Err, which may not be comparable
	err                     string
	images                  string
}

// running reports whether the block shows a running time.
func (b *toolBlock) running() bool { return !b.pending && !b.done }

func (b *toolBlock) append(s string) {
	b.total += len(s)
	b.output.WriteString(s)
	if b.output.Len() > 2*toolKeepBytes {
		keep := b.output.String()[b.output.Len()-toolKeepBytes:]
		b.output.Reset()
		b.output.WriteString(keep)
	}
}

func (b *toolBlock) Click(line int) bool {
	if b.done && line == 0 {
		// The header opens a done block, or folds it back to one line
		// (forgetting that it showed the whole output).
		if b.opened() {
			b.setTo(false)
		}
		b.open.toggle()
		return true
	}
	if !b.hit(line) {
		return false
	}
	b.toggle()
	return true
}

// opened reports whether a done block shows more than its line.
func (b *toolBlock) opened() bool {
	if b.open.d == nil {
		b.open.d = b.d
	}
	return b.open.expanded()
}

// commandPreviewLines is how many lines a collapsed block gives its
// command: one line hides what comes after "cd x &&", all of it can fill
// the screen.
const commandPreviewLines = 2

// commandLines wraps a command line under "  $ ", continuation lines
// indented to match, dim. With limit > 0 it keeps that many lines and ends
// the last with "…". Selection copies the wrapped lines back as one.
func commandLines(cmd string, width, limit int) []string {
	return wrapCommand(cmd, width, limit, true)
}

// commandRows is how many lines commandLines(cmd, width, 0) has, counting
// to n at most: a huge command is not wrapped just to be counted.
func commandRows(cmd string, width, n int) int {
	return len(tui.WrapFirst(cmd, max(1, width-4), n))
}

// wrapCommand is commandLines for one line of a script; only the first
// line gets the "$".
func wrapCommand(cmd string, width, limit int, first bool) []string {
	cmd = tui.StripControls(cmd)
	var wrapped []string
	if limit > 0 { // one line more tells whether to cut
		wrapped = tui.WrapFirst(cmd, max(1, width-4), limit+1)
	} else {
		wrapped = tui.Wrap(cmd, max(1, width-4))
	}
	cut := limit > 0 && len(wrapped) > limit
	if cut {
		wrapped = wrapped[:limit]
	}
	out := make([]string, len(wrapped))
	for i, l := range wrapped {
		prefix := "    "
		if i == 0 && first {
			prefix = "  $ "
		}
		l = prefix + l
		if cut && i == len(wrapped)-1 {
			l = tui.Truncate(l, width-1, "") + "…"
		}
		out[i] = tui.Dim(l)
	}
	return out
}

// displayLines turns raw output into printable lines: escapes stripped,
// carriage-return progress bars collapsed to their final state, trailing
// spaces dropped, and blank lines at either end removed (PowerShell pads
// its tables with them, which would leave the preview empty).
func displayLines(raw string) []string {
	lines := strings.Split(tui.StripEscapes(raw), "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if j := strings.LastIndexByte(l, '\r'); j >= 0 {
			l = l[j+1:]
		}
		lines[i] = strings.TrimRight(strings.ReplaceAll(tui.StripControls(l), "\t", "   "), " ")
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// backgroundHintAfter is when a running command's block starts to
// mention ctrl+b: quick commands never show it.
const backgroundHintAfter = 3 * time.Second

func (b *toolBlock) status() (icon, status string) {
	switch {
	case b.pending:
		return tui.FG(3, "●"), "writing…"
	case !b.done:
		ran := time.Since(b.start)
		s := fmt.Sprintf("%s / %s", tui.FormatDuration(ran.Truncate(100*time.Millisecond)), tui.FormatDuration(b.timeout))
		if ran >= backgroundHintAfter && agent.ShellHost {
			s += " · ctrl+b to background"
		}
		return tui.FG(3, "●"), s
	case b.res.Err != nil:
		return tui.FG(1, "✗"), tui.FG(1, tui.StripControls(b.res.Err.Error()))
	case b.res.Job > 0 && b.res.Background == agent.BackgroundRequested:
		return tui.FG(6, "◐"), tui.FG(6, fmt.Sprintf("running in background (job %d)", b.res.Job))
	case b.res.Job > 0:
		return tui.FG(6, "◐"), tui.FG(6, fmt.Sprintf("moved to background (job %d)", b.res.Job)) + tui.Dim(" · after "+tui.FormatDuration(b.res.Duration))
	case b.res.Canceled:
		return tui.FG(1, "✗"), tui.FG(1, "canceled")
	case b.res.TimedOut:
		return tui.FG(1, "✗"), tui.FG(1, "timed out after "+tui.FormatDuration(b.timeout))
	case b.res.ExitCode != 0:
		return tui.FG(1, "✗"), tui.FG(1, fmt.Sprintf("exit %d", b.res.ExitCode)) + tui.Dim(" · "+tui.FormatDuration(b.res.Duration))
	}
	return tui.FG(2, "✓"), tui.FormatDuration(b.res.Duration)
}

// Render caches the block; while the command runs, the header line, which
// shows the time, is made again on every call.
func (b *toolBlock) Render(width int) []string {
	key := toolKey{args: b.args, timeout: b.timeout, output: b.output.String(), total: b.total,
		pending: b.pending, done: b.done, expanded: b.expanded(), open: b.opened(), res: b.res,
		images: strings.Join(b.images, "\n")}
	if key.res.Err != nil {
		key.res.Err, key.err = nil, b.res.Err.Error()
	}
	out := b.cache.Render(width, key, func() []string { return b.render(width) })
	if b.running() {
		out = append([]string{b.head(width)}, out[1:]...)
	}
	return out
}

// head is the block's first line: description and status.
func (b *toolBlock) head(width int) string {
	icon, status := b.status()
	// The command goes on its own line: next to the description it got cut
	// off on narrow terminals.
	desc := tui.StripControls(b.args.Description)
	if desc == "" {
		desc = "Preparing command"
	}
	head := icon + " " + tui.Bold(desc) + tui.Dim(" · ") + tui.Dim(status)
	return tui.Truncate(head, width, tui.Dim("…"))
}

// line is a done block folded: description, outcome and command on one
// line, and for a failed call the last output lines.
func (b *toolBlock) line(width int) []string {
	icon, status := b.status()
	cmd := tui.StripControls(agent.FirstLine(b.args.Command))
	if strings.Contains(strings.TrimSpace(b.args.Command), "\n") {
		cmd += " …"
	}
	head := icon + " "
	if desc := strings.Join(strings.Fields(tui.StripControls(b.args.Description)), " "); desc != "" {
		head += tui.Bold(desc) + tui.Dim(" · ") + tui.Dim(status) + tui.Dim("  $ "+cmd)
	} else {
		head += tui.Bold(cmd) + tui.Dim(" · ") + tui.Dim(status)
	}
	out := []string{tui.Truncate(head, width, tui.Dim("…"))}
	if b.failed() {
		lines := displayLines(b.output.String())
		lines = lines[max(0, len(lines)-failedTailLines):]
		for i, l := range lines {
			prefix := "    "
			if i == 0 {
				prefix = "  └ "
			}
			out = append(out, tui.Truncate(tui.Dim(prefix+l), width, tui.Dim("…")))
		}
	}
	out = append(out, b.imageLines(width)...)
	return b.clicks(true, out, false)
}

// imageLines are a dim line per image atto view attached to the result.
func (b *toolBlock) imageLines(width int) []string {
	var out []string
	for _, l := range b.images {
		out = append(out, tui.Truncate(tui.Dim("  ▣ "+tui.StripControls(l)), width, tui.Dim("…")))
	}
	return out
}

// failedTailLines is how much of a failed call's output its folded block
// shows.
const failedTailLines = 2

func (b *toolBlock) render(width int) []string {
	if b.done && !b.opened() {
		return b.line(width)
	}
	cmd := strings.TrimSpace(b.args.Command)
	if i := strings.IndexByte(cmd, '\n'); i >= 0 {
		cmd = cmd[:i] + " …"
	}
	out := []string{b.head(width)}

	full := strings.TrimSpace(b.args.Command)
	multiLine := strings.Contains(full, "\n")
	lines := displayLines(b.output.String())
	long := commandRows(cmd, width, commandPreviewLines+1) > commandPreviewLines
	collapsible := len(lines) > toolPreviewLines || multiLine || long
	expanded := collapsible && b.expanded()

	if expanded {
		for i, l := range strings.Split(full, "\n") {
			out = append(out, wrapCommand(l, width, 0, i == 0)...)
		}
	} else {
		out = append(out, commandLines(cmd, width, commandPreviewLines)...)
	}
	// Collapsed, long output keeps its first and last lines around a
	// "… +N lines" row, as codex does: the start says what ran, the end
	// how it went.
	hidden, mid := 0, 0
	var tail []string
	if !expanded && len(lines) > toolPreviewLines {
		keep := (toolPreviewLines - 1) / 2
		hidden = len(lines) - 2*keep
		lines, tail = lines[:keep], lines[len(lines)-keep:]
	}
	if expanded && b.total > b.output.Len() {
		out = append(out, tui.Dim(fmt.Sprintf("    (earlier output not kept: %d bytes)", b.total-b.output.Len())))
	}
	if len(lines) == 0 && b.done {
		lines = []string{"(no output)"}
	}
	for i, l := range lines {
		prefix := "    "
		if i == 0 {
			prefix = "  └ "
		}
		out = append(out, tui.Truncate(tui.Dim(prefix+l), width, tui.Dim("…")))
	}
	if hidden > 0 {
		mid = len(out)
		out = append(out, tui.Truncate(tui.Dim(fmt.Sprintf("    … +%d lines (click or ctrl+t to expand)", hidden)), width, tui.Dim("…")))
		for _, l := range tail {
			out = append(out, tui.Truncate(tui.Dim("    "+l), width, tui.Dim("…")))
		}
	}
	out = append(out, b.imageLines(width)...)
	foot := false
	switch {
	case expanded:
		out, foot = append(out, disclosure(true, 0, "")), true
	case hidden == 0 && collapsible:
		out, foot = append(out, tui.Dim("    + Show details")), true
	}
	out = b.clicks(collapsible, out, foot)
	b.mid = mid
	return out
}

// compactBlock reports a compaction; the notes expand on click.
type compactBlock struct {
	expander
	clickable
	auto    bool
	reason  string // agent.ReasonPriceTier or ReasonSetting, with cap
	cap     int
	running bool
	notes   strings.Builder
	before  int
	after   int
	elapsed time.Duration
	cache   tui.RenderCache[compactKey]
}

// compactKey is what a compaction block's lines depend on.
type compactKey struct {
	auto, running, expanded bool
	reason                  string
	cap                     int
	notes                   string
	before, after           int
	elapsed                 time.Duration
}

func (c *compactBlock) Click(line int) bool {
	if c.running || !c.hit(line) {
		return false
	}
	c.toggle()
	return true
}

func (c *compactBlock) Render(width int) []string {
	key := compactKey{auto: c.auto, reason: c.reason, cap: c.cap, running: c.running, expanded: c.expanded(), notes: c.notes.String(),
		before: c.before, after: c.after, elapsed: c.elapsed}
	return c.cache.Render(width, key, func() []string { return c.render(width) })
}

func (c *compactBlock) render(width int) []string {
	kind := "Context compacted"
	if c.auto {
		kind = "Context auto-compacted"
	}
	why := ""
	switch c.reason {
	case agent.ReasonPriceTier:
		why = " · price tier above " + tui.FormatTokens(c.cap)
	case agent.ReasonSetting:
		why = " · compaction.limits " + tui.FormatTokens(c.cap)
	}
	if c.running {
		out := []string{tui.Dim("  ◇ Compacting context" + why + " · writing handoff notes…")}
		lines := tui.Wrap(strings.TrimSpace(tui.StripControls(c.notes.String())), max(1, width-4))
		if len(lines) > thinkingPreviewLines {
			lines = lines[len(lines)-thinkingPreviewLines:]
		}
		for _, l := range lines {
			out = append(out, "    "+tui.Dim(l))
		}
		return c.clicks(false, out, false)
	}
	head := tui.FG(6, "  ◇ ") + kind
	head += tui.Dim(why)
	if c.elapsed > 0 {
		head += tui.Dim(" · " + tui.FormatDuration(c.elapsed))
	}
	switch {
	case c.before > 0 && c.after > 0:
		head += tui.Dim(fmt.Sprintf(" · %s → ~%s tokens", tui.FormatTokens(c.before), tui.FormatTokens(c.after)))
	case c.before > 0: // a session saved before the size after was recorded
		head += tui.Dim(fmt.Sprintf(" · %s tokens before", tui.FormatTokens(c.before)))
	}
	if !c.expanded() {
		return c.clicks(true, []string{tui.Truncate(head+tui.Dim(" · click to view notes"), width, "…")}, false)
	}
	out := []string{tui.Truncate(head, width, "…")}
	for _, l := range tui.Markdown(c.notes.String(), max(1, width-4)) {
		out = append(out, "    "+l)
	}
	return c.clicks(true, append(out, disclosure(true, 0, "")), true)
}

// noticeBlock is a one-off status message from atto itself.
// The style is set once, with the text.
type noticeBlock struct {
	text  string
	style func(string) string
	cache tui.RenderCache[string]
}

func (n *noticeBlock) Render(width int) []string {
	return n.cache.Render(width, n.text, func() []string { return n.render(width) })
}

func (n *noticeBlock) render(width int) []string {
	lines := tui.Wrap(tui.StripControls(n.text), max(1, width-2))
	for i, l := range lines {
		lines[i] = "  " + n.style(l)
	}
	return lines
}

// startsWithMarker reports whether a rendered markdown line begins with its
// own marker (list bullet, number, quote or code bar, table border), where
// an extra leading bullet would double up.
func startsWithMarker(line string) bool {
	plain := strings.TrimSpace(tui.StripEscapes(line))
	for _, m := range []string{"•", "◦", "☐", "☑", "│", "╭", "┌", "─"} {
		if strings.HasPrefix(plain, m) {
			return true
		}
	}
	i := strings.IndexFunc(plain, func(r rune) bool { return r < '0' || r > '9' })
	return i > 0 && (plain[i] == '.' || plain[i] == ')')
}

func (g gap) ClickAt(column, line int) bool {
	if line <= 0 {
		return false
	}
	if c, ok := g.Component.(tui.CellClickable); ok {
		return c.ClickAt(column, line-1)
	}
	return g.Click(line)
}
