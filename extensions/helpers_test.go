package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/ui"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHost records what extensions ask of the front end.
type fakeHost struct {
	mu       sync.Mutex
	lane     UIQueue
	registry *ui.Registry
	store    MemoryStore
	ui       bool
	notices  []string
	status   map[string]string // ext/key
	widgets  map[string][]string
	asked    []Question
	answers  []any // given in order; then the default
	sent     []string
	cleared  []string
	// block status and display text, by ext/blockID.
	blockStatus  map[string]string
	blockDisplay map[string]string
	texts        []shownText
	names        []string // SetSessionName
}

func (h *fakeHost) SetSessionName(ext, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.names = append(h.names, name)
	return nil
}

// shownText is a passive native diff trace, only for the comparison corpus.
type shownOptions struct {
	Lang    string
	Preview int
}
type shownText struct {
	ext, title, text string
	opts             shownOptions
}

func (h *fakeHost) shown() []shownText {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.texts)
}

func (h *fakeHost) recordText(ext, title, text string, o shownOptions) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.texts = append(h.texts, shownText{ext, title, text, o})
}

func newHost(ui bool) *fakeHost {
	return &fakeHost{ui: ui, status: map[string]string{}, widgets: map[string][]string{}}
}

func (h *fakeHost) HasUI() bool { return h.ui }

func (h *fakeHost) Notify(ext, text, level string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notices = append(h.notices, fmt.Sprintf("%s %s: %s", level, ext, text))
}

func (h *fakeHost) Ask(ext string, q Question, answer func(any)) {
	h.mu.Lock()
	h.asked = append(h.asked, q)
	var v any
	if len(h.answers) > 0 {
		v, h.answers = h.answers[0], h.answers[1:]
	} else if q.Kind == "confirm" {
		v = false
	}
	h.mu.Unlock()
	go answer(v) // from another goroutine, as a front end does
}

func (h *fakeHost) DisposeUI(ext string) {
	h.UIWork(func(r *ui.Registry) error { r.Unload(ext); return nil }, func(error) {})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleared = append(h.cleared, ext)
	for k := range h.status {
		if strings.HasPrefix(k, ext+"/") {
			delete(h.status, k)
		}
	}
	for k := range h.widgets {
		if strings.HasPrefix(k, ext+"/") {
			delete(h.widgets, k)
		}
	}
}

func (h *fakeHost) SendMessage(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, text)
}

// hostState is a copy of what a fakeHost recorded.
type hostState struct {
	notices, sent, cleared []string
	asked                  []Question
	status                 map[string]string
	widgets                map[string][]string
}

func (h *fakeHost) snapshot() hostState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return hostState{notices: slices.Clone(h.notices), sent: slices.Clone(h.sent), asked: slices.Clone(h.asked),
		cleared: slices.Clone(h.cleared), status: clone(h.status), widgets: cloneW(h.widgets)}
}

func clone(m map[string]string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, m)
	return out
}

func cloneW(m map[string][]string) map[string][]string {
	out := map[string][]string{}
	maps.Copy(out, m)
	return out
}

// env gives a test its own ATTO_DIR and a project (a git root); it
// returns the user extensions dir and the project's cwd.
func env(t *testing.T) (userDir, cwd string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("ATTO_DIR", filepath.Join(root, "atto"))
	cwd = filepath.Join(root, "proj")
	write(t, filepath.Join(cwd, ".git", "HEAD"), "x")
	if err := config.Ensure(); err != nil {
		t.Fatal(err)
	}
	// One second keeps the timeout tests short.
	write(t, config.SettingsPath(), `{"extensions":{"timeout":1}}`)
	return config.ExtensionsDir(), cwd
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, cwd string, h Host) *Manager {
	t.Helper()
	m := Load(Options{Cwd: cwd, Host: h})
	t.Cleanup(m.Close)
	return m
}

func info(t *testing.T, m *Manager, name string) Info {
	t.Helper()
	for _, in := range m.Report() {
		if in.Name == name {
			return in
		}
	}
	t.Fatalf("no extension %q in %+v", name, m.Report())
	return Info{}
}

// eventually waits for cond, for what extensions do asynchronously.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Keep the old command comparison corpus: decode the new tree to the old
// passive trace in tests only. Production native commands never call recordText.
func (h *fakeHost) UIBlock(title string, tree ui.Node) {
	var parts []string
	for _, child := range tree.Children {
		parts = append(parts, ui.PlainText(child))
	}
	preview := int(tree.Props["previewLines"].(float64))
	h.recordText("diff", title, strings.TrimRight(strings.Join(parts, "\n"), "\n"), shownOptions{Lang: "diff", Preview: preview})
}

func (h *fakeHost) UIWork(work func(*ui.Registry) error, done func(error)) {
	h.lane.Post(func() { done(work(h.uiRegistry())) })
}
func (h *fakeHost) uiRegistry() *ui.Registry {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.registry == nil {
		h.registry = ui.NewRegistry(func(m ui.Mutation) {
			h.mu.Lock()
			defer h.mu.Unlock()
			i := m.Instance
			if m.Method == "ui/close" {
				delete(h.status, i.ID)
				delete(h.widgets, i.ID)
				return
			}
			if m.Method == "ui/render" {
				text := ""
				if i.Tree != nil {
					text = ui.PlainText(*i.Tree)
				}
				if i.Site == ui.Status {
					h.status[i.ID] = text
				}
				if i.Site == ui.Band {
					h.widgets[i.ID] = strings.Split(text, "\n")
				}
			}
		}, func(fn func()) { h.lane.Post(fn) })
	}
	return h.registry
}
func (h *fakeHost) Store(_ context.Context, owner, op, key string, v json.RawMessage) (json.RawMessage, error) {
	return h.store.Do(owner, op, key, v)
}
