package app

import (
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// saved writes a session in cwd with the messages (role, content pairs)
// and returns its writer, closed.
func saved(t *testing.T, cwd string, msgs ...string) *session.Writer {
	t.Helper()
	w := session.New(cwd)
	for i := 0; i+1 < len(msgs); i += 2 {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: msgs[i], Content: msgs[i+1]}})
	}
	w.Close()
	return w
}

// treeApp is a terminal on a saved session of u1, a1, u2, a2 (its model
// answers "ok").
func treeApp(t *testing.T, msgs ...string) *App {
	t.Helper()
	cwd, _ := testEnv(t)
	if len(msgs) == 0 {
		msgs = []string{"user", "u1", "assistant", "a1", "user", "u2", "assistant", "a2"}
	}
	w := saved(t, cwd, msgs...)
	return startApp(t, cwd, Options{Session: w.ID})
}

func entryID(t *testing.T, a *App, content string) string {
	t.Helper()
	var entries []session.Entry
	a.ui.Do(func() { entries = a.loadSession() })
	for _, e := range entries {
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

func users(a *App) string {
	var s string
	a.ui.Do(func() { s = strings.Join(userBlocks(a), ",") })
	return s
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
	key(a, "\x1b")
	a.ui.Do(func() { a.onInput("x") })
	key(a, "\x1b")
	a.ui.Do(func() {
		if a.modal != nil {
			t.Fatal("intervening key did not cancel")
		}
	})
	key(a, "\x1b")
	settle(a)
	a.ui.Do(func() {
		if _, ok := a.modal.(*treePicker); !ok {
			t.Fatalf("tree %T", a.modal)
		}
		a.closeModal()
		a.editor.SetText("draft")
	})
	key(a, "\x1b")
	key(a, "\x1b")
	settle(a)
	a.ui.Do(func() {
		if a.modal != nil {
			t.Fatal("tree over draft")
		}
		a.editor.SetText("")
		a.busy = true
	})
	key(a, "\x1b")
	key(a, "\x1b")
	a.ui.Do(func() { a.busy = false })
	key(a, "\x1b")
	settle(a)
	a.ui.Do(func() {
		if a.modal != nil {
			t.Fatal("interrupt armed double Esc")
		}
		a.escAction = "none"
	})
	key(a, "\x1b")
	key(a, "\x1b")
	settle(a)
	a.ui.Do(func() {
		if a.modal != nil {
			t.Fatal("none action ignored")
		}
		a.escAction = "fork"
	})
	key(a, "\x1b")
	key(a, "\x1b")
	settle(a)
	a.ui.Do(func() {
		if _, ok := a.modal.(*tui.SelectList); !ok {
			t.Fatalf("fork %T", a.modal)
		}
	})
}

// Going back to a user message moves the branch for every client, puts
// the message in the editor, and the session resumes on the new branch.
func TestNavigateAndResume(t *testing.T) {
	a := treeApp(t)
	if got := users(a); got != "u1,u2" {
		t.Fatalf("resumed transcript %q", got)
	}
	navigate(t, a, "u2")
	within(t, a, "the branch", func() bool { return strings.Join(userBlocks(a), ",") == "u1" })
	within(t, a, "the message in the editor", func() bool { return a.editor.Text() == "u2" })

	// Picking a non-user entry makes it the leaf.
	a.ui.Do(func() { a.editor.SetText("") })
	navigate(t, a, "a1")
	settle(a)
	b := reopen(t, a)
	if got := users(b); got != "u1" {
		t.Fatalf("after reopening: %q", got)
	}

	// The old branch is still in the file.
	_, entries, _ := session.Load(b.sessPath)
	var off []string
	for _, it := range session.Items(entries) {
		if it.OffBranch {
			off = append(off, it.Text)
		}
	}
	if strings.Join(off, ",") != "u2,a2" {
		t.Fatalf("off-branch items %v", off)
	}
}

// Picking an entry while a turn runs interrupts it and moves once it has
// stopped; its pending steer comes back to the editor.
func TestNavigateWhileBusyWaitsForTurn(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	cwd, m := testEnv(t, providertest.Reply{Text: "never", Gate: gate})
	w := saved(t, cwd, "user", "u1", "assistant", "a1")
	a := startApp(t, cwd, Options{Session: w.ID})
	typeLine(a, "go on")
	m.Started(5 * time.Second)
	typeLine(a, "steer")
	within(t, a, "the steer pending", func() bool { return len(a.pending.Steers) == 1 })
	navigate(t, a, "u1")
	within(t, a, "the move", func() bool { return len(userBlocks(a)) == 0 && !a.busy })
	within(t, a, "the steer back", func() bool { return strings.Contains(a.editor.Text(), "steer") })
}

func TestTreePickerDrawsBranches(t *testing.T) {
	cwd, _ := testEnv(t)
	w := session.New(cwd)
	add := func(role, content string) {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: role, Content: content}})
	}
	add("user", "u1")
	add("assistant", "a1")
	a1 := w.Leaf()
	add("user", "u2")
	add("assistant", "a2")
	w.Branch(a1)
	add("user", "u3")
	w.Close()
	a := startApp(t, cwd, Options{Session: w.ID})
	var p *treePicker
	a.ui.Do(func() { a.cmdTree("") })
	settle(a)
	a.ui.Do(func() { p = a.modal.(*treePicker) })
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

	a.ui.Do(func() {
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
	})
	u2 := entryID(t, a, "u2")
	a.ui.Do(func() {
		p.selected = p.nearestVisible(u2)
		p.HandleInput("\x1bb") // option+left folds the branch
		if !p.folded[u2] || len(p.visible) != 4 {
			t.Fatalf("fold: %d visible", len(p.visible))
		}
		// Labels are entries in the session, written by its runtime.
		p.HandleInput("L")
		for _, k := range []string{"o", "k", "\r"} {
			p.HandleInput(k)
		}
	})
	settle(a)
	var label session.Entry
	_, entries, _ := session.Load(w.Path)
	for _, e := range entries {
		if e.Type == session.TypeLabel {
			label = e
		}
	}
	if label.Label != "ok" || label.TargetID != u2 {
		t.Fatalf("label %+v", label)
	}
}

func TestNavigateBringsImagesBack(t *testing.T) {
	cwd, _ := testEnv(t)
	im, err := images.ReadFile(writePNG(t, 3, 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := images.Save(im); err != nil {
		t.Fatal(err)
	}
	w := session.New(cwd)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "look [image 1: 3x2 PNG]", Images: []provider.Image{im}}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "a1"}})
	w.Close()
	a := startApp(t, cwd, Options{Session: w.ID})
	navigate(t, a, "look [image 1: 3x2 PNG]")
	within(t, a, "the message back", func() bool { return a.editor.Text() == "look [image 1: 3x2 PNG]" })
	var att []tui.Attachment
	a.ui.Do(func() { att = a.editor.Attachments() })
	if len(att) != 1 {
		t.Fatalf("%d attachments", len(att))
	}
	if got, ok := att[0].Value.(provider.Image); !ok || len(got.Data) == 0 || got.File != im.File {
		t.Fatalf("attachment %+v", att[0])
	}
}

// navigate goes back to the entry with content, as picking it in /tree.
func navigate(t *testing.T, a *App, content string) {
	t.Helper()
	id := entryID(t, a, content)
	a.ui.Do(func() { a.navigateTree(id) })
}
