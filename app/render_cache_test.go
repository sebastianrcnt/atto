package app

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/tui"
)

// cachedBlock is a block whose Render caches what render makes.
type cachedBlock interface {
	tui.Component
	render(width int) []string
}

// TestBlockCacheInvalidation changes blocks in every way the app does and
// checks that each change shows: after it, Render (warm cache) gives what
// render (no cache) gives, and something else than before.
func TestBlockCacheInvalidation(t *testing.T) {
	d := &details{}
	many := strings.Repeat("line\n", 12)
	cases := []struct {
		name   string
		block  func() cachedBlock
		change func(b cachedBlock)
	}{
		{"user: width", func() cachedBlock { return &userBlock{text: "hello world, this is a prompt"} }, nil},
		{"text: streamed", func() cachedBlock { b := &textBlock{}; b.text.WriteString("Hello"); return b },
			func(b cachedBlock) { b.(*textBlock).text.WriteString(" **world**") }},
		{"thinking: streamed", func() cachedBlock { return newThinking(d, "first thought", false) },
			func(b cachedBlock) { b.(*thinkingBlock).text.WriteString("\nsecond thought") }},
		{"thinking: done", func() cachedBlock { return newThinking(d, "thought", false) },
			func(b cachedBlock) { t := b.(*thinkingBlock); t.done, t.dur = true, 3*time.Second }},
		{"thinking: click", func() cachedBlock { return newThinking(d, "thought", true) },
			func(b cachedBlock) { b.(*thinkingBlock).Click(0) }},
		{"thinking: ctrl+t", func() cachedBlock { return newThinking(d, "thought", true) }, ctrlT(d)},
		{"tool: output", func() cachedBlock { return newTool(d, "ls", "a\n", true) },
			func(b cachedBlock) { b.(*toolBlock).append("b\n") }},
		{"tool: output trimmed", func() cachedBlock {
			return newTool(&details{on: true}, "ls", strings.Repeat("x\n", toolKeepBytes), true)
		},
			func(b cachedBlock) { b.(*toolBlock).append("y") }},
		{"tool: call written", func() cachedBlock { b := newTool(d, "ls", "", false); b.pending = true; return b },
			func(b cachedBlock) { b.(*toolBlock).args = agent.BashArgs{Description: "List", Command: "ls -la"} }},
		{"tool: exit code", func() cachedBlock { return newTool(d, "false", "", true) },
			func(b cachedBlock) { b.(*toolBlock).res.ExitCode = 1 }},
		{"tool: error", func() cachedBlock { return newTool(d, "x", "", true) },
			func(b cachedBlock) { b.(*toolBlock).res.Err = errors.New("no such command") }},
		{"tool: background", func() cachedBlock { return newTool(d, "x", "", true) },
			func(b cachedBlock) { b.(*toolBlock).res.Job = 3 }},
		{"tool: image attached", func() cachedBlock { return newTool(d, "atto view a.png", "", true) },
			func(b cachedBlock) { b.(*toolBlock).images = []string{"a.png 3×2"} }},
		{"tool: done, folds", func() cachedBlock { return &toolBlock{expander: expander{d: d}, args: agent.BashArgs{Command: "ls"}} },
			func(b cachedBlock) { b.(*toolBlock).done = true }},
		{"tool: open", func() cachedBlock {
			return &toolBlock{expander: expander{d: d}, args: agent.BashArgs{Command: "ls"}, done: true}
		},
			func(b cachedBlock) { b.(*toolBlock).Click(0) }},
		{"tool: click", func() cachedBlock { return newTool(d, "ls", many, true) },
			func(b cachedBlock) { b.(*toolBlock).Click(0) }},
		{"tool: ctrl+t", func() cachedBlock { return newTool(d, "ls", many, true) }, ctrlT(d)},
		{"tool: long command", func() cachedBlock { return newTool(d, "echo", "", true) },
			func(b cachedBlock) { b.(*toolBlock).args.Command = strings.Repeat("echo hi && ", 50) }},
		{"compaction: notes streamed", func() cachedBlock { return newCompact(d, "notes", true) },
			func(b cachedBlock) { b.(*compactBlock).notes.WriteString(" and more") }},
		{"compaction: done", func() cachedBlock { return newCompact(d, "draft", true) },
			func(b cachedBlock) {
				c := b.(*compactBlock)
				c.running = false
				c.notes.Reset()
				c.notes.WriteString("final")
				c.before, c.after, c.elapsed = 9000, 2000, time.Second
			}},
		{"compaction: click", func() cachedBlock { return newCompact(d, "notes", false) },
			func(b cachedBlock) { b.(*compactBlock).Click(0) }},
		{"compaction: ctrl+t", func() cachedBlock { return newCompact(d, "notes", false) }, ctrlT(d)},
		{"summary: streamed", func() cachedBlock { return newSummary(d, "tried", true) },
			func(b cachedBlock) { b.(*summaryBlock).text.WriteString(" this") }},
		{"summary: done", func() cachedBlock { return newSummary(d, "tried", true) },
			func(b cachedBlock) {
				c := b.(*summaryBlock)
				c.running = false
				c.text.Reset()
				c.text.WriteString("final")
			}},
		{"summary: click", func() cachedBlock { return newSummary(d, "tried", false) },
			func(b cachedBlock) { b.(*summaryBlock).Click(0) }},
		{"shell: output", func() cachedBlock { return newShell(d, "a\n", true) },
			func(b cachedBlock) { b.(*shellBlock).output.WriteString("b\n") }},
		{"shell: done", func() cachedBlock { return newShell(d, "a\n", false) },
			func(b cachedBlock) {
				s := b.(*shellBlock)
				s.done, s.exit, s.dur = true, 2, time.Second
			}},
		{"shell: queued", func() cachedBlock { return newShell(d, "a\n", true) },
			func(b cachedBlock) { b.(*shellBlock).queued = true }},
		{"shell: click", func() cachedBlock { return newShell(d, many, true) },
			func(b cachedBlock) { b.(*shellBlock).Click(0) }},
		{"shell: ctrl+t", func() cachedBlock { return newShell(d, many, true) }, ctrlT(d)},
		{"notice: width", func() cachedBlock {
			return &noticeBlock{text: "a notice that is long enough to wrap at forty", style: tui.Dim}
		}, nil},
		{"info: width", func() cachedBlock {
			return &infoBlock{title: "Goal paused", hint: "a hint that is long enough to wrap at forty columns"}
		}, nil},
	}
	for _, c := range cases {
		*d = details{}
		b := c.block()
		width := 60
		before := b.Render(width)
		b.Render(width) // from the cache
		if c.change != nil {
			c.change(b)
		} else {
			width = 40
		}
		got := b.Render(width)
		clicks := clickState(b)
		want := b.render(width)
		if !slices.Equal(got, want) {
			t.Errorf("%s: cached %q\nwant %q", c.name, got, want)
		}
		if slices.Equal(got, before) {
			t.Errorf("%s: the change does not show: %q", c.name, got)
		}
		if clicks != clickState(b) {
			t.Errorf("%s: clickable lines %+v, want %+v", c.name, clicks, clickState(b))
		}
	}
}

func clickState(b cachedBlock) clickable {
	switch b := b.(type) {
	case *thinkingBlock:
		return b.clickable
	case *toolBlock:
		return b.clickable
	case *compactBlock:
		return b.clickable
	case *summaryBlock:
		return b.clickable
	case *shellBlock:
		return b.clickable
	}
	return clickable{}
}

func ctrlT(d *details) func(cachedBlock) {
	return func(cachedBlock) { d.on, d.gen = !d.on, d.gen+1 }
}

func newThinking(d *details, text string, done bool) *thinkingBlock {
	b := &thinkingBlock{expander: expander{d: d}, done: done, dur: time.Second}
	b.text.WriteString(text)
	return b
}

func newTool(d *details, cmd, output string, done bool) *toolBlock {
	b := &toolBlock{expander: expander{d: d}, args: agent.BashArgs{Description: "Run", Command: cmd}, done: done,
		start: time.Now(), timeout: time.Minute}
	b.append(output)
	if done {
		b.open.setTo(true) // shows the output, not only its line
	}
	return b
}

func newCompact(d *details, notes string, running bool) *compactBlock {
	b := &compactBlock{expander: expander{d: d}, running: running}
	b.notes.WriteString(notes)
	return b
}

func newSummary(d *details, text string, running bool) *summaryBlock {
	b := &summaryBlock{expander: expander{d: d}, running: running}
	b.text.WriteString(text)
	return b
}

func newShell(d *details, output string, done bool) *shellBlock {
	b := &shellBlock{expander: expander{d: d}, cmd: "ls", done: done, start: time.Now()}
	b.output.WriteString(output)
	return b
}

// TestRunningBlocksTick checks that a running command's time is redrawn
// while the rest of its block comes from the cache.
func TestRunningBlocksTick(t *testing.T) {
	d := &details{}
	tool := newTool(d, "sleep 9", "out\n", false)
	shell := newShell(d, "out\n", false)
	for _, b := range []cachedBlock{tool, shell} {
		first := b.Render(60)
		switch b := b.(type) {
		case *toolBlock:
			b.start = b.start.Add(-2 * time.Second)
		case *shellBlock:
			b.start = b.start.Add(-2 * time.Second)
		}
		second := b.Render(60)
		if first[0] == second[0] {
			t.Errorf("%T: the time did not move: %q", b, second[0])
		}
		if !slices.Equal(first[1:], second[1:]) || &first[1] == &second[1] {
			t.Errorf("%T: the rest changed, or the cached lines were modified: %q", b, second)
		}
		if want := b.render(60); !slices.Equal(second, want) {
			t.Errorf("%T: %q, want %q", b, second, want)
		}
	}
}

// TestRenderLongTranscriptAllocs renders a long transcript, then the
// same again: the second frame must cost a small fraction of the first,
// and no more for a transcript twice as long.
func TestRenderLongTranscriptAllocs(t *testing.T) {
	steady := map[int]float64{}
	for _, groups := range []int{40, 80} {
		a := transcriptApp(t, tui.Fullscreen, true)
		if groups == 80 { // twice as long
			for _, c := range slices.Clone(a.ui.Body.Children[1:]) {
				a.ui.Body.Add(c)
			}
		}
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		a.ui.RenderNow()
		runtime.ReadMemStats(&m1)
		first := float64(m1.Mallocs - m0.Mallocs)
		steady[groups] = testing.AllocsPerRun(5, func() {
			a.runStart = a.runStart.Add(-a.ui.AnimationInterval()) // the spinner turns
			a.ui.RenderNow()
		})
		if steady[groups]*20 > first {
			t.Errorf("%d groups: a frame with nothing new allocates %.0f times, the first %.0f", groups, steady[groups], first)
		}
	}
	if steady[80] > steady[40]+20 {
		t.Errorf("frame allocations grow with the transcript: %.0f, then %.0f", steady[40], steady[80])
	}
}

// recordTerm is a sizedTerm that keeps what is written.
type recordTerm struct {
	sizedTerm
	out *strings.Builder
}

func (r recordTerm) Write(s string) { r.out.WriteString(s) }

// TestActivityFrameWritesOneLine: at 30 frames a second, a frame in which
// only the activity line moves writes that line alone, and costs no more
// for a transcript twice as long (the blocks' render caches hit).
func TestActivityFrameWritesOneLine(t *testing.T) {
	for _, mode := range []tui.Mode{tui.Fullscreen, tui.Inline} {
		allocs := map[int]float64{}
		for _, groups := range []int{40, 80} {
			var out strings.Builder
			a := transcriptAppOn(t, recordTerm{sizedTerm{120, 40}, &out}, mode, false)
			if groups == 80 {
				for _, c := range slices.Clone(a.ui.Body.Children[1:]) {
					a.ui.Body.Add(c)
				}
			}
			a.ui.Colors = tui.TrueColor
			a.spinnerScan = true // so every frame changes
			now := a.runStart
			a.now = func() time.Time { return now }
			a.ui.RenderNow()
			allocs[groups] = testing.AllocsPerRun(30, func() {
				out.Reset()
				now = now.Add(a.ui.AnimationInterval())
				a.ui.RenderNow()
				w := tui.StripEscapes(out.String())
				if !strings.Contains(w, "Thinking…") || strings.Contains(w, "part") || strings.Count(w, "\n") > 0 || len(w) > 400 {
					t.Fatalf("mode %v: a frame wrote %q", mode, w)
				}
			})
		}
		if allocs[80] > allocs[40]+20 {
			t.Errorf("mode %v: frame allocations grow with the transcript: %.0f, then %.0f", mode, allocs[40], allocs[80])
		}
	}
}

// A done command shows the images atto view attached, folded or open.
func TestToolBlockImages(t *testing.T) {
	b := &toolBlock{expander: expander{d: &details{}}, args: agent.BashArgs{Description: "Look", Command: "atto view shot.png"},
		done: true, images: []string{"shot.png 1136×1038"}}
	if s := plainLines(b.Render(80)); !strings.Contains(s, "▣ shot.png 1136×1038") {
		t.Fatalf("folded:\n%s", s)
	}
	b.Click(0)
	if s := plainLines(b.Render(80)); !strings.Contains(s, "▣ shot.png 1136×1038") || !strings.Contains(s, "$ atto view") {
		t.Fatalf("open:\n%s", s)
	}
}
