package app

import (
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// treeApp builds an App with a session, without a terminal or a model;
// nothing is sent.
func treeApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("ATTO_DIR", t.TempDir())
	cwd := t.TempDir()
	a := &App{
		ui:    tui.New(nullTerm{}),
		agent: agent.New(config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}, "", cwd),
		tools: map[string]*toolBlock{},
		cwd:   cwd,
		quit:  make(chan struct{}),
	}
	a.build()
	a.newSession("")
	// Close the session file and its lease before the temp dirs go:
	// Windows can't delete open files. (Cleanups run last-registered first.)
	t.Cleanup(a.closeSession)
	return a
}

func (a *App) record(role, content string) {
	a.sess.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: role, Content: content}})
}

func entryID(t *testing.T, a *App, content string) string {
	t.Helper()
	for _, e := range a.loadSession() {
		if e.Message != nil && e.Message.Content == content {
			return e.ID
		}
	}
	t.Fatalf("no entry %q", content)
	return ""
}

func userBlocks(a *App) []string {
	var out []string
	for _, c := range a.ui.Body.Children {
		if g, ok := c.(gap); ok {
			if u, ok := g.Component.(*userBlock); ok {
				out = append(out, u.text)
			}
		}
	}
	return out
}

func TestDoubleEscTiming(t *testing.T) {
	now := time.Unix(0, 0)
	d := doubleEsc{now: func() time.Time { return now }}
	step := func(dt time.Duration, want bool) {
		t.Helper()
		now = now.Add(dt)
		if got := d.press(); got != want {
			t.Fatalf("at %v: got %v", now.Sub(time.Unix(0, 0)), got)
		}
	}
	step(0, false)
	step(300*time.Millisecond, true)  // second press within the window
	step(100*time.Millisecond, false) // the pair was consumed: starts over
	step(600*time.Millisecond, false) // too slow: this one starts over
	step(499*time.Millisecond, true)
	step(0, false)
	d.reset()
	step(10*time.Millisecond, false) // a key in between cancels the first press
}

func TestDoubleEscOpensTree(t *testing.T) {
	a := treeApp(t)
	a.record("user", "u1")
	a.record("assistant", "a1")

	// Esc, another key, Esc: no tree.
	a.onInput("\x1b")
	a.onInput("x")
	a.onInput("\x1b")
	if a.modal != nil {
		t.Fatal("a key between the presses should cancel")
	}
	a.onInput("\x1b")
	if _, ok := a.modal.(*treePicker); !ok {
		t.Fatalf("esc esc should open the tree, got %T", a.modal)
	}
	a.closeModal()

	// A draft in the editor: Esc does nothing special.
	a.editor.SetText("draft")
	a.onInput("\x1b")
	a.onInput("\x1b")
	if a.modal != nil {
		t.Fatal("tree opened over a draft")
	}
	a.editor.SetText("")

	// While a turn runs, Esc interrupts, and never arms the double Esc.
	cancels := 0
	a.busy, a.cancel = true, func() { cancels++ }
	a.onInput("\x1b")
	a.onInput("\x1b")
	a.busy = false
	a.onInput("\x1b")
	if a.modal != nil || cancels != 2 {
		t.Fatalf("modal %T, cancels %d", a.modal, cancels)
	}

	a.escAction = "none"
	a.onInput("\x1b")
	a.onInput("\x1b")
	if a.modal != nil {
		t.Fatal(`"none" should disable it`)
	}
	a.escAction = "fork"
	a.onInput("\x1b")
	a.onInput("\x1b")
	if _, ok := a.modal.(*tui.SelectList); !ok {
		t.Fatalf("fork picker expected, got %T", a.modal)
	}
}

func TestNavigateAndResume(t *testing.T) {
	a := treeApp(t)
	a.record("user", "u1")
	a.record("assistant", "a1")
	a.record("user", "u2")
	a.record("assistant", "a2")
	a.goal.Goal = &goal.Goal{Objective: "x", Status: goal.Active}
	a.queued = []queuedInput{{text: "queued follow-up"}}

	a.navigateTree(entryID(t, a, "u2"))
	if got := a.editor.Text(); !strings.Contains(got, "queued follow-up") {
		t.Fatalf("queued text should go back to the editor: %q", got)
	}
	if got := userBlocks(a); strings.Join(got, ",") != "u1" {
		t.Fatalf("transcript %v", got)
	}
	if a.goal.Goal.Status != goal.Paused {
		t.Fatal("goal should pause")
	}
	if b := a.agent.Breakdown(); b.Messages != 2 {
		t.Fatalf("agent has %d messages", b.Messages)
	}
	a.editor.SetText("")
	a.navigateTree(entryID(t, a, "u2")) // again: the text goes to the empty editor
	if got := a.editor.Text(); got != "u2" {
		t.Fatalf("editor %q", got)
	}

	// Continue on the new branch, then resume in a fresh app.
	a.record("user", "u2 edited")
	a.record("assistant", "a2 new")
	path := a.sess.Path
	a.sess.Close()

	b := treeApp(t)
	b.resume(path)
	if got := strings.Join(userBlocks(b), ","); got != "u1,u2 edited" {
		t.Fatalf("resumed transcript %q", got)
	}
	if bd := b.agent.Breakdown(); bd.Messages != 4 {
		t.Fatalf("resumed agent has %d messages", bd.Messages)
	}

	// The old branch is still in the file and in atto history.
	_, entries, _ := session.Load(path)
	var off []string
	for _, it := range session.Items(entries) {
		if it.OffBranch {
			off = append(off, it.Text)
		}
	}
	if strings.Join(off, ",") != "u2,a2" {
		t.Fatalf("off-branch items %v", off)
	}

	// Picking a non-user entry makes it the leaf; picking the leaf is a no-op.
	b.navigateTree(entryID(t, b, "a1"))
	if got := strings.Join(userBlocks(b), ","); got != "u1" || b.editor.Text() != "" {
		t.Fatalf("after picking a1: %q, editor %q", got, b.editor.Text())
	}
}

func TestNavigateWhileBusyWaitsForTurn(t *testing.T) {
	a := treeApp(t)
	a.record("user", "u1")
	a.record("assistant", "a1")
	canceled := false
	a.busy, a.cancel, a.runKind = true, func() { canceled = true }, "turn"
	a.pendingSteers = []string{"steer"}
	a.agent.Steer("steer")
	a.navigateTree(entryID(t, a, "u1"))
	if !canceled || a.pendingTree == "" {
		t.Fatal("should interrupt and wait")
	}
	a.busy, a.cancel = false, nil
	a.afterRun(nil)
	if a.pendingTree != "" || len(userBlocks(a)) != 0 {
		t.Fatalf("navigation not applied: %v", userBlocks(a))
	}
	if got := a.editor.Text(); got != "steer" { // as in pi, the message text only fills an empty editor
		t.Fatalf("editor %q", got)
	}
}

func TestTreePickerDrawsBranches(t *testing.T) {
	a := treeApp(t)
	a.record("user", "u1")
	a.record("assistant", "a1")
	a.record("user", "u2")
	a.record("assistant", "a2")
	a.navigateTree(entryID(t, a, "u2"))
	a.record("user", "u3")
	a.editor.SetText("")
	a.cmdTree("")
	p := a.modal.(*treePicker)
	var plain []string
	for _, l := range p.Render(80) {
		plain = append(plain, strings.TrimRight(tui.StripEscapes(l), " "))
	}
	got := strings.Join(plain, "\n")
	for _, want := range []string{
		"Session Tree",
		"Type to search:",
		"  • user: u1",
		"  • assistant: a1",
		"├─ • user: u3", // the active branch first
		"└⊟ user: u2",
		"assistant: a2",
		"(3/5)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// Search, then a filter, then fold.
	for _, k := range []string{"u", "2"} {
		p.HandleInput(k)
	}
	if len(p.visible) != 1 || p.visible[0].n.Entry.Message.Content != "u2" {
		t.Fatalf("search: %d visible", len(p.visible))
	}
	p.HandleInput("\x1b") // clears the search first
	if a.modal == nil || p.query != "" {
		t.Fatal("esc should clear the search before closing")
	}
	p.HandleInput("\x15") // ctrl+u: user messages only
	if len(p.visible) != 3 {
		t.Fatalf("user filter: %d", len(p.visible))
	}
	p.HandleInput("\x15") // toggles back
	if p.filter != filterDefault {
		t.Fatal("ctrl+u should toggle")
	}
	p.selected = p.nearestVisible(entryID(t, a, "u2"))
	p.HandleInput("\x1bb") // option+left folds the branch
	if !p.folded[entryID(t, a, "u2")] || len(p.visible) != 4 {
		t.Fatalf("fold: %d visible", len(p.visible))
	}

	// Labels are entries in the session.
	p.HandleInput("L")
	for _, k := range []string{"o", "k", "\r"} {
		p.HandleInput(k)
	}
	var label session.Entry
	for _, e := range a.loadSession() {
		if e.Type == session.TypeLabel {
			label = e
		}
	}
	if label.Label != "ok" || label.TargetID != entryID(t, a, "u2") {
		t.Fatalf("label %+v", label)
	}
}

func TestNavigateBringsImagesBack(t *testing.T) {
	a := treeApp(t)
	im, err := images.ReadFile(writePNG(t, 3, 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := images.Save(im); err != nil {
		t.Fatal(err)
	}
	a.sess.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "look [image 1: 3x2 PNG]", Images: []provider.Image{im}}})
	a.record("assistant", "a1")
	a.navigateTree(entryID(t, a, "look [image 1: 3x2 PNG]"))
	att := a.editor.Attachments()
	if a.editor.Text() != "look [image 1: 3x2 PNG]" || len(att) != 1 {
		t.Fatalf("editor %q, %d attachments", a.editor.Text(), len(att))
	}
	if got, ok := att[0].Value.(provider.Image); !ok || len(got.Data) == 0 || got.File != im.File {
		t.Fatalf("attachment %+v", att[0])
	}
}
