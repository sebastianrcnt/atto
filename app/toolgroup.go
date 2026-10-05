package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/tui"
)

// toolRun holds the commands the model ran one after another, with no
// text or other item between them (reasoning between two calls is part of
// the run: thinking models think before every call, and such a run would
// never group). Two or more calls group: the calls before the last
// collapse into one summary line of their descriptions, and the last call
// stays shown as it would be on its own, as do calls that failed and calls
// still running. A click on the summary line, or ctrl+t, shows every call
// on a shaded region. Groups are made from the transcript items as they
// come, live or on resume, so a resumed session shows the same.
type toolRun struct {
	expander
	members []tui.Component // *toolBlock and *thinkingBlock, in order
	off     *bool           // settings.json "toolGroups": false; shows each block on its own

	head  tui.RenderCache[runHeadKey]
	fills []bgFill // by member: its lines on the region's background
	blank bgFill   // a blank line of the region

	// The last frame: what it was made of and where each part went, for
	// clicks, and its lines, given again while no part changed.
	sig   []runPart
	parts []runPart
	out   []string
	width int
}

// runPart is a part of a frame: a member's lines (member >= 0), the
// summary line (member == runHead) or a blank line, as the slice it took
// them from.
type runPart struct {
	member int
	start  int // first line in the frame
	lines  *string
	n      int
	bg     bool
}

const runHead = -1

// runHeadKey is what the summary line depends on, cached only once every
// summarized call is done (their descriptions no longer change).
type runHeadKey struct {
	n, failed int
	dur       time.Duration
	expanded  bool
}

// bgFill caches a member's lines put on the region's background.
type bgFill struct {
	src   *string
	n     int
	width int
	lines []string
}

func (r *toolRun) add(c tui.Component) { r.members = append(r.members, c) }

// tools are the run's calls.
func (r *toolRun) tools() []*toolBlock {
	var out []*toolBlock
	for _, m := range r.members {
		if b, ok := m.(*toolBlock); ok {
			out = append(out, b)
		}
	}
	return out
}

// grouped reports whether the run shows as a group.
func (r *toolRun) grouped() bool {
	if r.off != nil && *r.off {
		return false
	}
	n := 0
	for _, m := range r.members {
		if _, ok := m.(*toolBlock); ok {
			if n++; n >= 2 {
				return true
			}
		}
	}
	return false
}

// failed reports whether a call ended in failure: an error, a non-zero
// exit, a timeout or a cancel.
func (b *toolBlock) failed() bool {
	return b.done && (b.res.Err != nil || b.res.ExitCode != 0 || b.res.TimedOut || b.res.Canceled)
}

// summary is what the summary line says of a call: the description the
// model wrote, as the block's header shows it, or the command's first line.
func (b *toolBlock) summary() string {
	if d := strings.Join(strings.Fields(b.args.Description), " "); d != "" {
		return d
	}
	if c := agent.FirstLine(b.args.Command); c != "" {
		return c
	}
	return "Preparing command"
}

func (r *toolRun) Render(width int) []string {
	parts := r.parts[:0]
	line := 0
	put := func(member int, lines []string, bg bool) {
		parts = append(parts, runPart{member: member, start: line, lines: unsafe.SliceData(lines), n: len(lines), bg: bg})
		line += len(lines)
	}
	blank := func(bg bool) {
		if bg {
			put(-2, r.bgBlank(width), true)
		} else {
			put(-2, emptyLine, false)
		}
	}
	switch {
	case !r.grouped():
		for i, m := range r.members {
			if i > 0 {
				blank(false)
			}
			put(i, m.Render(width), false)
		}
	case r.expanded():
		put(runHead, r.headLine(width), false)
		blank(true)
		for i, m := range r.members {
			if i > 0 {
				blank(true)
			}
			put(i, r.fill(i, m.Render(width), width), true)
		}
		blank(true)
	default:
		put(runHead, r.headLine(width), false)
		last := r.lastTool()
		for i, m := range r.members {
			b, ok := m.(*toolBlock)
			if ok && (i == last || !b.done || b.failed()) || !ok && i > last {
				blank(false)
				put(i, m.Render(width), false)
			}
		}
	}
	r.parts = parts
	if r.out != nil && width == r.width && samePlan(parts, r.sig) {
		return r.out
	}
	out := make([]string, 0, line)
	for _, p := range parts {
		out = append(out, unsafe.Slice(p.lines, p.n)...)
	}
	r.sig = append(r.sig[:0], parts...)
	r.out, r.width = out, width
	return out
}

var emptyLine = []string{""}

// samePlan reports whether a frame is made of the very slices the last
// was: each member's cache gives the same slice while it is unchanged.
func samePlan(a, b []runPart) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *toolRun) lastTool() int {
	for i := len(r.members) - 1; i >= 0; i-- {
		if _, ok := r.members[i].(*toolBlock); ok {
			return i
		}
	}
	return -1
}

// Click toggles the group on its summary line, and passes clicks on a
// call or reasoning shown to it.
func (r *toolRun) Click(line int) bool {
	for _, p := range r.parts {
		if line < p.start || line >= p.start+p.n {
			continue
		}
		switch {
		case p.member == runHead:
			r.toggle()
			return true
		case p.member >= 0:
			if c, ok := r.members[p.member].(tui.Clickable); ok {
				return c.Click(line - p.start)
			}
		}
		return false
	}
	return false
}

// headLine is the summary line for every call but the last: how many,
// their total run time, how many failed, then their descriptions as far as
// they fit.
func (r *toolRun) headLine(width int) []string {
	tools := r.tools()
	sum := tools[:len(tools)-1]
	key := runHeadKey{n: len(sum), expanded: r.expanded()}
	settled := true
	for _, b := range sum {
		settled = settled && b.done
		key.dur += b.res.Duration
		if b.failed() {
			key.failed++
		}
	}
	line := func() []string { return []string{runSummary(sum, key, width)} }
	if !settled {
		return line()
	}
	return r.head.Render(width, key, line)
}

func runSummary(calls []*toolBlock, key runHeadKey, width int) string {
	mark := "▸ "
	if key.expanded {
		mark = "▾ "
	}
	noun := "commands"
	if len(calls) == 1 {
		noun = "command"
	}
	head := tui.Dim(fmt.Sprintf("%s%d %s · %s", mark, len(calls), noun, tui.FormatDuration(key.dur)))
	if key.failed > 0 {
		head += tui.Dim(" · ") + tui.FG(1, fmt.Sprintf("%d failed", key.failed))
	}
	descs := make([]string, len(calls))
	for i, b := range calls {
		descs[i] = b.summary()
	}
	return tui.Truncate(head+tui.Dim("  "+strings.Join(descs, ", ")), width, "…")
}

// --- the shaded region ---

// groupBG is the background of an expanded group: a dark gray, or a
// light one where COLORFGBG says the terminal's background is light (most
// terminals don't say, and dark is the common case). It differs from the
// prompt's band (userBG) so the two don't read as one.
var groupBG = sync.OnceValue(func() int {
	if lightBackground(os.Getenv("COLORFGBG")) {
		return 254
	}
	return 236
})

// lightBackground reads COLORFGBG ("fg;bg" or "fg;default;bg"): a
// background of 7 (light gray) or 9 to 15 (the bright colors but 8,
// dark gray) is light.
func lightBackground(v string) bool {
	f := strings.Split(v, ";")
	n, err := strconv.Atoi(f[len(f)-1])
	return err == nil && (n == 7 || n >= 9 && n <= 15)
}

func (r *toolRun) bgBlank(width int) []string { return r.blank.fill(emptyLine, width) }

// fill puts a member's lines on the region's background, cached while
// they don't change.
func (r *toolRun) fill(member int, lines []string, width int) []string {
	for len(r.fills) <= member {
		r.fills = append(r.fills, bgFill{})
	}
	return r.fills[member].fill(lines, width)
}

func (f *bgFill) fill(lines []string, width int) []string {
	if src := unsafe.SliceData(lines); f.lines == nil || f.src != src || f.n != len(lines) || f.width != width {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = onBG(groupBG(), l, width)
		}
		*f = bgFill{src: src, n: len(lines), width: width, lines: out}
	}
	return f.lines
}

// onBG puts a line on background color c, padded to width. A line's own
// resets (Truncate ends a cut line with one, BG with its own) would end the
// color early, so it is set again after each; the line ends with the
// background reset, so nothing bleeds past it.
func onBG(c int, line string, width int) string {
	if tui.VisibleWidth(line) > width {
		line = tui.Truncate(line, width, "…")
	}
	bg := "\x1b[48;5;" + strconv.Itoa(c) + "m"
	pad := strings.Repeat(" ", max(0, width-tui.VisibleWidth(line)))
	if strings.IndexByte(line, 0x1b) >= 0 {
		line = strings.NewReplacer("\x1b[0m", "\x1b[0m"+bg, "\x1b[m", "\x1b[m"+bg, "\x1b[49m", bg).Replace(line)
	}
	return bg + line + pad + "\x1b[49m"
}
