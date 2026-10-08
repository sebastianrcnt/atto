package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/tui"
)

func TestSkillCommands(t *testing.T) {
	cwd, _ := testEnv(t)
	skill := filepath.Join(config.Dir(), "skills", "pdf", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: pdf\ndescription: Work with PDFs\n---\nUse pdftotext."), 0o644); err != nil {
		t.Fatal(err)
	}
	a := startApp(t, cwd)
	a.ui.Do(func() { a.editor.SetText("/skill:p") })
	within(t, a, "the skill in the command list", func() bool {
		lines := a.renderSuggestions(80)
		return len(lines) > 0 && strings.Contains(tui.StripEscapes(lines[0]), "/skill:pdf")
	})
	a.ui.Do(func() { a.suggestionKey("tab") })
	if got := a.editor.Text(); got != "/skill:pdf " {
		t.Fatalf("tab completes the skill: %q", got)
	}
}

func TestSuggestionList(t *testing.T) {
	a := testApp(t)
	a.editor.SetText("/")
	lines := a.renderSuggestions(80)
	if len(lines) != maxSuggestions+1 || !strings.Contains(tui.StripEscapes(lines[maxSuggestions]), "(1/") {
		t.Fatalf("a long list shows %d rows and a position: %q", maxSuggestions, lines)
	}

	a.suggestionKey("up") // wraps to the last command
	l := a.suggestions()
	m, sel := l.Visible(), l.Selected
	if sel != len(m)-1 {
		t.Fatalf("up from the top selects the last, got %d", sel)
	}
	lines = a.renderSuggestions(80)
	if !strings.Contains(tui.StripEscapes(lines[maxSuggestions-1]), "/"+m[sel].Value) {
		t.Fatalf("the window follows the selection: %q", lines)
	}

	a.editor.SetText("/co") // compact, copy, context
	a.suggestionKey("down")
	a.suggestionKey("tab")
	if got := a.editor.Text(); got != "/copy " {
		t.Fatalf("tab completes the selection: %q", got)
	}

	a.editor.SetText("/na")
	if !a.suggestionKey("enter") || a.editor.Text() != "/name " {
		t.Fatalf("enter on a command with a required argument only completes it: %q", a.editor.Text())
	}
	a.editor.SetText("/cle")
	if a.suggestionKey("enter") || a.editor.Text() != "/clear" {
		t.Fatalf("enter completes and lets the editor submit: %q", a.editor.Text())
	}

	a.editor.SetText("/")
	a.suggestionKey("escape")
	if len(a.renderSuggestions(80)) != 0 {
		t.Fatal("esc closes the list")
	}
	a.editor.SetText("/m")
	if len(a.renderSuggestions(80)) == 0 {
		t.Fatal("typing reopens it")
	}
}

func TestTuiCommand(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	a := &App{ui: tui.New(nil)}
	a.ui.Body.Add(tui.Func(func(int) []string { return nil }))
	a.cmdTui("inline")
	if a.ui.Mode != tui.Inline {
		t.Fatal("/tui inline switches the renderer")
	}
	s, err := config.LoadSettings()
	if err != nil || s.Renderer != "inline" {
		t.Fatalf("saved renderer %q, %v", s.Renderer, err)
	}
	a.cmdTui("auto")
	if s, _ := config.LoadSettings(); s.Renderer != "" {
		t.Fatalf("auto clears the setting: %q", s.Renderer)
	}
	if a.ui.Mode != rendererMode("") {
		t.Fatal("auto picks the default again")
	}
	a.cmdTui("bogus")
	a.cmdTui("")
}

func TestSuggestionsAboveInput(t *testing.T) {
	a := testApp(t)
	a.editor.SetText("/co")
	var rows []string
	for _, l := range a.ui.Footer.Render(80) {
		rows = append(rows, tui.StripEscapes(l))
	}
	sug, in := -1, -1
	for i, l := range rows {
		if strings.Contains(l, "/compact") && sug < 0 {
			sug = i
		}
		if strings.Contains(l, "/co") && strings.Contains(l, "›") {
			in = i
		}
	}
	if sug < 0 || in < 0 || sug >= in {
		t.Fatalf("the list (row %d) belongs above the input (row %d): %q", sug, in, rows)
	}
}

func TestJumpPill(t *testing.T) {
	a := &App{ui: tui.New(nil)}
	p := jumpPill{a}
	if p.Render(40) != nil {
		t.Fatal("no pill at the bottom")
	}
	a.ui.ScrollBy(5)
	got := p.Render(40)
	if len(got) != 1 || !strings.Contains(tui.StripEscapes(got[0]), "Jump to bottom (click) ↓") {
		t.Fatalf("pill: %q", got)
	}
	if text := tui.StripEscapes(got[0]); !strings.HasPrefix(text, strings.Repeat(" ", 7)) {
		t.Fatalf("the pill is centered: %q", text)
	}
	if !p.Click(0) || a.ui.ScrollOffset() != 0 {
		t.Fatal("click scrolls to the bottom")
	}
}
