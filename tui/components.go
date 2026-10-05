package tui

import (
	"strings"
	"time"
)

// Text displays word-wrapped text with optional horizontal padding.
// Output is cached per (text, width) so unchanged blocks cost nothing.
type Text struct {
	text     string
	PaddingX int

	cacheText  string
	cacheWidth int
	cacheLines []string
}

func NewText(text string) *Text { return &Text{text: text} }

func (t *Text) SetText(s string)    { t.text = s }
func (t *Text) AppendText(s string) { t.text += s }
func (t *Text) Text() string        { return t.text }

func (t *Text) Render(width int) []string {
	if t.cacheLines != nil && t.cacheText == t.text && t.cacheWidth == width {
		return t.cacheLines
	}
	var lines []string
	if t.text != "" {
		pad := min(t.PaddingX, max(0, (width-1)/2))
		margin := strings.Repeat(" ", pad)
		for _, l := range Wrap(t.text, max(1, width-2*pad)) {
			lines = append(lines, margin+l)
		}
	}
	t.cacheText, t.cacheWidth, t.cacheLines = t.text, width, lines
	if lines == nil {
		t.cacheLines = []string{}
	}
	return lines
}

// Spacer renders n blank lines.
type Spacer struct{ N int }

func (s Spacer) Render(int) []string { return make([]string, s.N) }

// Spinner shows an animated frame followed by a message while running.
// It renders nothing when stopped.
type Spinner struct {
	Message string
	Style   func(string) string

	frame   int
	running bool
	stop    chan struct{}
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Start begins animating. Must be called under the TUI lock (inside Do).
func (s *Spinner) Start(ui *TUI) {
	if s.running {
		return
	}
	s.running = true
	s.stop = make(chan struct{})
	stop := s.stop
	go func() {
		tick := time.NewTicker(ui.GlyphInterval())
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				ui.Do(func() { s.frame++ })
			}
		}
	}()
}

// Stop halts the animation. Must be called under the TUI lock.
func (s *Spinner) Stop() {
	if !s.running {
		return
	}
	s.running = false
	close(s.stop)
}

func (s *Spinner) Render(width int) []string {
	if !s.running {
		return nil
	}
	f := spinnerFrames[s.frame%len(spinnerFrames)]
	if s.Style != nil {
		f = s.Style(f)
	}
	return []string{Truncate(f+" "+s.Message, width, "…")}
}

// Func adapts a render function into a Component, handy for status lines.
type Func func(width int) []string

func (f Func) Render(width int) []string { return f(width) }
