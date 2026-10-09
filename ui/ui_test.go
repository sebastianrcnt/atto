package ui

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

//go:fix inline
func ptr[T any](x T) *T { return new(x) }
func fixture() Node {
	return Box(BoxProps{BorderStyle: "round", Gap: 1}, Text(TextProps{Text: "é 👩‍💻 漢字\tunsafe\x1b]52;payload\a"}), Button(ButtonProps{Key: "go", Label: "Go", Hotkey: "g"}), Input(InputProps{Key: "in"}), Select(SelectProps{Key: "sel", Options: []Option{{Value: "a", Label: "A"}, {Value: "b", Label: "B", Disabled: true}}}))
}
func TestConstructors(t *testing.T) {
	for _, n := range []Node{fixture(), Markdown(MarkdownProps{Text: "# hello"}), Code(CodeProps{Source: "x"}), Diff(DiffProps{Source: "+x"}), Link(LinkProps{Href: "https://example.com"}), List(ListProps{Mode: "table", Rows: []Row{{Key: "a", Cells: []string{"x"}}}, Columns: []Column{{Label: "X"}}}), Progress(ProgressProps{Value: new(.5)}), Collapse(CollapseProps{Key: "c", Title: "X"}), Image(ImageProps{Resource: "image-1", Alt: "test"})} {
		if err := Validate(Pane, n); err != nil {
			t.Errorf("%s: %v", n.Type, err)
		}
		b, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "onPress") {
			t.Fatal("callback serialized")
		}
	}
}
func TestValidation(t *testing.T) {
	cases := []Node{Node{Type: "Text", Props: map[string]any{"color": "red"}}, Node{Type: "Text", Props: map[string]any{"foo": true}}, Text(TextProps{Text: strings.Repeat("x", MaxText+1)}), Box(BoxProps{Padding: 17}), Link(LinkProps{Href: "https://user:pass@example.com"}), Link(LinkProps{Href: "http://example.com"}), Link(LinkProps{Href: "javascript:alert(1)"}), Select(SelectProps{Key: "s", Options: []Option{{Value: "a", Label: "A", Disabled: true}}}), Box(BoxProps{}, Button(ButtonProps{Key: "x", Label: "a"}), Button(ButtonProps{Key: "x", Label: "b"})), Box(BoxProps{}, Button(ButtonProps{Key: "x", Label: "a", Hotkey: "a"}), Button(ButtonProps{Key: "y", Label: "b", Hotkey: "a"})), Node{Type: "engine", Props: map[string]any{"site": "pane", "id": "other"}}}
	for _, n := range cases {
		if err := Validate(Pane, n); err == nil {
			t.Errorf("accepted %#v", n)
		}
	}
	n := Text(TextProps{})
	for range 33 {
		n = Box(BoxProps{}, n)
	}
	if Validate(Pane, n) == nil {
		t.Fatal("depth accepted")
	}
	children := make([]Node, 2048)
	for i := range children {
		children[i] = Text(TextProps{})
	}
	if Validate(Pane, Box(BoxProps{}, children...)) == nil {
		t.Fatal("node limit")
	}
	if Validate(Status, fixture()) == nil {
		t.Fatal("status controls")
	}
	if Validate(Pane, Node{Type: "Future", Props: map[string]any{"text": "fallback"}, Events: []EventType{Press}}) != nil {
		t.Fatal("unknown passive fallback")
	}
	for _, url := range []string{"https://example.com/x", "http://localhost:1234/x", "http://127.0.0.1/x", "http://[::1]/x"} {
		if !SafeURL(url) {
			t.Errorf("safe URL rejected: %s", url)
		}
	}
}
func TestRegistryRouting(t *testing.T) {
	var mutations []Mutation
	r := NewRegistry(func(m Mutation) { mutations = append(mutations, m) }, nil)
	defer r.Stop()
	calls := 0
	tree := fixture()
	m := Match{Pane, "atto/test"}
	r.Bind("atto", m, "go", Press, func(context.Context, Action) error { calls++; return nil })
	if err := r.OpenDefault("atto", OpenOptions{Site: Pane, ID: m.ID}, nil, &tree); err != nil {
		t.Fatal(err)
	}
	snap := r.Snapshot()
	rev := snap.Instances[0].Rev
	a := Action{Site: Pane, ID: m.ID, Key: "go", Type: Press, Rev: rev}
	if err := r.Route(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := r.Route(context.Background(), a); err == nil || calls != 1 {
		t.Fatal("duplicate ran")
	}
	a.Rev = r.Snapshot().Instances[0].Rev
	a.Key = "sel"
	a.Type = SelectEvent
	a.Value = new("b")
	if r.Route(context.Background(), a) == nil {
		t.Fatal("disabled option accepted")
	}
	if mutations[0].Method != "ui/open" || mutations[1].Method != "ui/render" {
		t.Fatal("ordering")
	}
	last := int64(0)
	for _, m := range mutations {
		if m.Instance.Rev <= last {
			t.Fatal("revision reused")
		}
		last = m.Instance.Rev
	}
	if err := r.Close("bad", Pane, m.ID); err == nil {
		t.Fatal("ownership")
	}
	_ = r.Close("atto", Pane, m.ID)
	_ = r.OpenDefault("atto", OpenOptions{Site: Pane, ID: m.ID}, nil, &tree)
	if r.Snapshot().Instances[0].Rev <= last {
		t.Fatal("reopen revision reused")
	}
}
func TestCoalescing(t *testing.T) {
	var mu sync.Mutex
	count := 0
	r := NewRegistry(func(m Mutation) {
		if m.Method == "ui/render" {
			mu.Lock()
			count++
			mu.Unlock()
		}
	}, nil)
	defer r.Stop()
	tree := Text(TextProps{Text: "hi"})
	_ = r.OpenDefault("atto", OpenOptions{Site: Band, ID: "atto/band"}, nil, &tree)
	for range 1000 {
		r.Invalidate(Match{Band, "atto/band"})
	}
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("got %d renders", count)
	}
}
func TestMiddlewareFallback(t *testing.T) {
	r := NewRegistry(nil, nil)
	defer r.Stop()
	m := Match{ToolCall, "item-1"}
	r.Render("ext", m, func(e Event, next Next) (*Node, error) {
		e.Props["description"] = "reviewed"
		n, err := next(e)
		if err != nil {
			return nil, err
		}
		out := Box(BoxProps{}, *n, Text(TextProps{Text: "wrapped"}))
		return &out, nil
	})
	if err := r.OpenDefault("atto", OpenOptions{Site: ToolCall, ID: m.ID}, map[string]any{"description": "original"}, nil); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	tree := r.sites[m].Tree
	r.mu.Unlock()
	if tree.Type != "Box" || tree.Children[0].Type != "engine" {
		t.Fatalf("%#v", tree)
	}
	if tree.Children[0].Props["overrides"].(map[string]any)["description"] != "reviewed" {
		t.Fatal("override lost")
	}
}
func TestHeadlessSafe(t *testing.T) {
	got := PlainText(fixture())
	if strings.Contains(got, "payload") || strings.Contains(got, "\x1b") {
		t.Fatal(got)
	}
	if got := PlainText(Node{Type: "Future", Props: map[string]any{}, Children: []Node{Text(TextProps{Text: "child"})}}); got != "child" {
		t.Fatal(got)
	}
}
func FuzzValidate(f *testing.F) {
	for _, n := range []Node{fixture(), Text(TextProps{Text: "x"}), Node{Type: "engine", Props: map[string]any{"site": "notice", "id": "x"}}} {
		b, _ := json.Marshal(n)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > MaxBytes+1 {
			return
		}
		var n Node
		if json.Unmarshal(b, &n) != nil {
			return
		}
		if err := Validate(Pane, n); err == nil {
			if len(b) > MaxBytes { // Input JSON may contain insignificant whitespace; bound canonical encoding.
				canonical, _ := json.Marshal(n)
				if len(canonical) > MaxBytes {
					t.Fatal("oversized accepted")
				}
			}
			_ = PlainText(n)
		}
	})
}

func TestDialogMiddlewareRequiresDefaultOnce(t *testing.T) {
	for _, mode := range []string{"wrap", "replace", "twice"} {
		t.Run(mode, func(t *testing.T) {
			r := NewRegistry(nil, nil)
			defer r.Stop()
			m := Match{Dialog, "atto/question"}
			fallback := Input(InputProps{Key: "answer"})
			r.Render("ext", m, func(e Event, next Next) (*Node, error) {
				n, err := next(e)
				if err != nil {
					return nil, err
				}
				out := Text(TextProps{Text: "replacement"})
				switch mode {
				case "wrap":
					out = Box(BoxProps{}, *n, Text(TextProps{Text: "footer"}))
				case "twice":
					out = Box(BoxProps{}, *n, *n)
				}
				return &out, nil
			})
			_ = r.OpenDefault("atto", OpenOptions{Site: Dialog, ID: m.ID}, nil, &fallback)
			tree := r.Snapshot().Instances[0].Tree
			if mode == "wrap" {
				if tree.Type != "Box" || tree.Children[0].Type != "Input" {
					t.Fatal(tree)
				}
			} else if tree.Type != "Input" {
				t.Fatal("invalid wrapper did not revert")
			}
		})
	}
}

func TestProviderKeysAndPassiveBindings(t *testing.T) {
	r := NewRegistry(nil, nil)
	defer r.Stop()
	m := Match{Pane, "atto/p"}
	fallback := Button(ButtonProps{Key: "native", Label: "Native"})
	r.Render("ext", m, func(e Event, next Next) (*Node, error) {
		original, err := next(e)
		if err != nil {
			return nil, err
		}
		n := Box(BoxProps{}, *original, Button(ButtonProps{Key: "native", Label: "Extension"}))
		return &n, nil
	})
	called := 0
	r.Bind("ext", m, "native", Press, func(context.Context, Action) error { called++; return nil })
	_ = r.OpenDefault("atto", OpenOptions{Site: Pane, ID: m.ID}, nil, &fallback)
	i := r.Snapshot().Instances[0]
	if i.Tree.Children[0].Key != "native" || i.Tree.Children[1].Key != "ext/native" {
		t.Fatal(i.Tree)
	}
	if err := r.Route(context.Background(), Action{Site: Pane, ID: m.ID, Key: "native", Type: Press, Rev: i.Rev}); err == nil {
		t.Fatal("extension bound native control")
	}
	if err := r.Route(context.Background(), Action{Site: Pane, ID: m.ID, Key: "ext/native", Type: Press, Rev: i.Rev}); err != nil || called != 1 {
		t.Fatal(err, called)
	}
}

func TestInvalidMiddlewareUsesFreshBuiltinTail(t *testing.T) {
	r := NewRegistry(nil, nil)
	defer r.Stop()
	value := "initial"
	m := Match{Pane, "atto/fallback"}
	r.Render("atto", m, func(Event, Next) (*Node, error) { n := Text(TextProps{Text: value}); return &n, nil })
	r.Render("bad", m, func(Event, Next) (*Node, error) { n := Text(TextProps{}); n.Props["unknown"] = true; return &n, nil })
	_ = r.Open("atto", OpenOptions{Site: Pane, ID: m.ID})
	if PlainText(*r.Snapshot().Instances[0].Tree) != "initial" {
		t.Fatal("missing built-in default")
	}
	value = "fresh"
	time.Sleep(110 * time.Millisecond)
	r.Invalidate(m)
	if PlainText(*r.Snapshot().Instances[0].Tree) != "fresh" {
		t.Fatal("stale fallback")
	}
}

func TestMarkdownAllowsOnlyCompleteSafeTargets(t *testing.T) {
	for _, text := range []string{"[x](https://example.com javascript:evil)", "[x]( https://example.com)", "<http://example.com>", "[x](data:text/plain,x)"} {
		if Validate(Pane, Markdown(MarkdownProps{Text: text})) == nil {
			t.Fatal("unsafe Markdown accepted", text)
		}
	}
}

func TestModifiedEngineReferenceFallsBack(t *testing.T) {
	r := NewRegistry(nil, nil)
	defer r.Stop()
	m := Match{ToolCall, "item"}
	r.Render("ext", m, func(e Event, next Next) (*Node, error) {
		n, err := next(e)
		if err != nil {
			return nil, err
		}
		n.Props["overrides"] = map[string]any{"output": "forged"}
		return n, nil
	})
	n, _, err := r.DrawItem(m.Site, m.ID, map[string]any{"output": "original"})
	if err != nil || n != nil {
		t.Fatal("modified opaque ref accepted", n, err)
	}
}
func TestRenderDeadlineUsesDefault(t *testing.T) {
	r := NewRegistry(nil, nil)
	defer r.Stop()
	m := Match{Band, "atto/timeout"}
	fallback := Text(TextProps{Text: "default"})
	r.Render("slow", m, func(Event, Next) (*Node, error) {
		time.Sleep(110 * time.Millisecond)
		n := Text(TextProps{Text: "late"})
		return &n, nil
	})
	_ = r.OpenDefault("atto", OpenOptions{Site: Band, ID: m.ID}, nil, &fallback)
	if PlainText(*r.Snapshot().Instances[0].Tree) != "default" {
		t.Fatal("late render published")
	}
}

func TestHungRendererIsBoundedAndCancelled(t *testing.T) {
	r := NewRegistry(nil, nil)
	defer r.Stop()
	m := Match{Pane, "atto/hung"}
	finished := make(chan struct{})
	r.Render("hung", m, func(e Event, next Next) (*Node, error) {
		<-e.Context.Done()
		close(finished)
		return nil, e.Context.Err()
	})
	fallback := Text(TextProps{Text: "default"})
	start := time.Now()
	_ = r.OpenDefault("atto", OpenOptions{Site: Pane, ID: m.ID}, nil, &fallback)
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("watchdog did not bound render")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("provider context was not cancelled")
	}
	if PlainText(*r.Snapshot().Instances[0].Tree) != "default" {
		t.Fatal("did not restore default")
	}
}

func TestSiteSnapshotKeepsRegistrationOrder(t *testing.T) {
	r := NewRegistry(nil, nil)
	defer r.Stop()
	for _, id := range []string{"atto/a", "atto/b"} {
		n := Text(TextProps{Text: id})
		_ = r.OpenDefault("atto", OpenOptions{Site: Band, ID: id}, nil, &n)
	}
	time.Sleep(110 * time.Millisecond)
	r.UpdateProps(Match{Band, "atto/a"}, map[string]any{"changed": true})
	instances := r.Snapshot().Instances
	if instances[0].ID != "atto/a" || instances[1].ID != "atto/b" {
		t.Fatal("unstable site ordering", instances)
	}
}
