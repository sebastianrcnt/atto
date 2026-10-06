package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/tui"
)

// sizedTerm is a terminal of a fixed size that discards what is written.
type sizedTerm struct{ w, h int }

func (sizedTerm) Start(func(string), func()) error { return nil }
func (sizedTerm) Stop()                            {}
func (sizedTerm) Write(string)                     {}
func (s sizedTerm) Size() (int, int)               { return s.w, s.h }

// transcriptApp is an App showing a long finished transcript: blocks
// many and mixed, with long thinking text and a few huge one-line
// commands, the busy footer of a turn running on top.
func transcriptApp(tb testing.TB, mode tui.Mode, fullRepaint bool) *App {
	return transcriptAppOn(tb, sizedTerm{120, 40}, mode, fullRepaint)
}

// transcriptAppOn is transcriptApp on the terminal term.
func transcriptAppOn(tb testing.TB, term tui.Terminal, mode tui.Mode, fullRepaint bool) *App {
	tb.Setenv("ATTO_DIR", tb.TempDir())
	model := config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}
	a := &App{ui: tui.New(term), agent: agent.New(model, "", tb.TempDir()), tools: map[string]*toolBlock{}, quit: make(chan struct{})}
	a.build()
	a.ui.Mode, a.ui.FullRepaint = mode, fullRepaint
	words := strings.Fields("the quick brown fox jumps over the lazy dog while reading src/main.go and thinking about the next step")
	para := func(n int) string {
		var b strings.Builder
		for i := range n {
			if i > 0 {
				b.WriteByte(' ')
				if i%40 == 0 {
					b.WriteString("\n\n")
				}
			}
			b.WriteString(words[i%len(words)])
		}
		return b.String()
	}
	for i := range 40 {
		a.add(&userBlock{text: fmt.Sprintf("Please look at part %d of the code and fix what is wrong.", i)})
		th := &thinkingBlock{d: &a.details, done: true, dur: 3 * time.Second}
		th.text.WriteString(para(600))
		a.add(th)
		cmd := "go test ./..."
		if i%10 == 0 {
			cmd = "grep -rn " + strings.Repeat("pattern|", 2000) + "end ."
		}
		tool := &toolBlock{d: &a.details, args: agent.BashArgs{Description: "Run the tests", Command: cmd}, done: true,
			res: agent.BashResult{Duration: 2 * time.Second}}
		for j := range 30 {
			tool.append(fmt.Sprintf("ok  \tgithub.com/x/y/pkg%d\t0.%03ds\n", j, j))
		}
		a.add(tool)
		txt := &textBlock{}
		txt.text.WriteString("Here is what I found:\n\n- the **first** thing\n- the `second` thing\n\n" + para(120))
		a.add(txt)
		a.add(&noticeBlock{text: "a notice " + para(20), style: tui.Dim})
	}
	a.busy, a.runStart, a.activity = true, time.Now(), "Thinking"
	a.lastEvent = a.runStart
	return a
}

var renderModes = []struct {
	name        string
	mode        tui.Mode
	fullRepaint bool
}{
	{"fullscreen-full-repaint", tui.Fullscreen, true},
	{"fullscreen-diff", tui.Fullscreen, false},
	{"inline-diff", tui.Inline, false},
}

// BenchmarkRenderLongTranscript renders frames of a long transcript in
// which nothing but the footer's spinner changes, as while a turn waits
// on the model.
func BenchmarkRenderLongTranscript(b *testing.B) {
	for _, m := range renderModes {
		b.Run(m.name, func(b *testing.B) {
			a := transcriptApp(b, m.mode, m.fullRepaint)
			a.ui.RenderNow()
			b.ReportAllocs()
			for b.Loop() {
				a.runStart = a.runStart.Add(-a.ui.AnimationInterval()) // the next spinner frame
				a.ui.RenderNow()
			}
		})
	}
}

// BenchmarkRenderStreaming is BenchmarkRenderLongTranscript with the
// model streaming thinking into a last block, a token a frame.
func BenchmarkRenderStreaming(b *testing.B) {
	for _, m := range renderModes {
		b.Run(m.name, func(b *testing.B) {
			a := transcriptApp(b, m.mode, m.fullRepaint)
			th := &thinkingBlock{d: &a.details, start: time.Now()}
			a.add(th)
			a.ui.RenderNow()
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				if th.text.Len() > 16<<10 { // a new block now and then: a typical length
					th.text.Reset()
				}
				th.text.WriteString("token ")
				if i%50 == 49 {
					th.text.WriteString("\n")
				}
				a.ui.RenderNow()
				i++
			}
		})
	}
}
