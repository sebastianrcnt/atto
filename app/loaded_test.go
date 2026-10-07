package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/tui"
)

// loadedEnv is a home whose ATTO_DIR and project have AGENTS files,
// skills and hooks: the home directory is <tmp>, ATTO_DIR <tmp>/.atto and
// the project <tmp>/proj; its model t/m (the default) is served at url.
func loadedEnv(t *testing.T, url string) (cwd string) {
	t.Helper()
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir, cwd := filepath.Join(home, ".atto"), filepath.Join(home, "proj")
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	writeTestFile(t, filepath.Join(dir, "AGENTS.md"), "Global rules.")
	writeTestFile(t, filepath.Join(dir, "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: Work with PDFs\n---\nx")
	writeTestFile(t, filepath.Join(dir, "skills", "broken", "SKILL.md"), "---\nname: broken\n---\nx")
	writeTestFile(t, filepath.Join(dir, "settings.json"), `{"defaultProvider":"t","defaultModel":"m","skills":{"disabled":["atto-extensions"]},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"./check.sh"}]}]}}`)
	writeTestFile(t, filepath.Join(cwd, ".git", "HEAD"), "x")
	writeTestFile(t, filepath.Join(cwd, "AGENTS.md"), "Project rules.")
	writeTestFile(t, filepath.Join(cwd, "CLAUDE.md"), "Claude rules.")
	setModels(t, url)
	return cwd
}

// loadedApp is a terminal on a session of loadedEnv, its model down.
func loadedApp(t *testing.T) *App {
	t.Helper()
	return startApp(t, loadedEnv(t, "http://127.0.0.1:9/v1"))
}

// setModels configures the test model t/m at url.
func setModels(t *testing.T, url string) {
	writeTestFile(t, config.ModelsPath(), `{"providers":{"t":{"baseUrl":"`+url+`","models":[{"id":"m","contextWindow":100000}]}}}`)
}

// loadedBlocks returns the transcript's loaded blocks.
func loadedBlocks(a *App) []*loadedBlock {
	var out []*loadedBlock
	a.ui.Do(func() {
		for _, c := range a.ui.Body.Children {
			if g, ok := c.(gap); ok {
				if b, ok := g.Component.(*loadedBlock); ok {
					out = append(out, b)
				}
			}
		}
	})
	return out
}

func TestLoadedBlockAtSessionStart(t *testing.T) {
	a := loadedApp(t)
	bs := loadedBlocks(a)
	if len(bs) != 1 {
		t.Fatalf("one loaded block at the top, got %d", len(bs))
	}
	b := bs[0]
	short := plainLines(b.Render(80))
	t.Logf("collapsed at 80:\n%s", short)
	for _, s := range []string{
		"◇ Loaded · click or ctrl+t for details",
		"AGENTS.md   ~/.atto/AGENTS.md (13 B), ~/proj/AGENTS.md (14 B); 1 skipped",
		"Skills      1: pdf; 1 skipped",
		"Hooks       1: PreToolUse(Bash) · from ~/.atto/settings.json",
		"Extensions  2: autorename, diff",
		"Model       m · medium (model from ~/.atto/settings.json, effort default)",
		"Config      ~/.atto/settings.json, ~/.atto/models.json",
		"Prompt      ", "system prompt: base, environment, 2 AGENTS files, 1 skill",
	} {
		if !strings.Contains(filepath.ToSlash(short), s) {
			t.Errorf("collapsed block lacks %q", s)
		}
	}
	for _, l := range b.Render(80) {
		if tui.VisibleWidth(l) > 80 {
			t.Errorf("line wider than 80: %q", l)
		}
	}
	if n := len(b.Render(80)); n != 8 {
		t.Errorf("collapsed: a header and one line per kind, got %d lines", n)
	}

	if !b.Click(0) {
		t.Fatal("the header toggles the block")
	}
	long := plainLines(b.Render(80))
	t.Logf("expanded at 80:\n%s", long)
	for _, s := range []string{
		"AGENTS files (in prompt order, 32 KiB cap)",
		"~/proj/CLAUDE.md", "skipped: shadowed by AGENTS.md",
		"skipped  ~/.atto/skills/broken/SKILL.md: description is required",
		"PreToolUse [Bash]  ./check.sh · ~/.atto/settings.json",
		"System prompt (", "Also sent", "− Show less (click)",
	} {
		if !strings.Contains(filepath.ToSlash(long), s) {
			t.Errorf("expanded block lacks %q", s)
		}
	}
	for _, l := range b.Render(40) {
		if tui.VisibleWidth(l) > 40 {
			t.Errorf("expanded line wider than 40: %q", l)
		}
	}
	b.Click(len(b.Render(80)) - 1) // the disclosure line collapses it
	a.details.on, a.details.gen = true, a.details.gen+1
	if !strings.Contains(plainLines(b.Render(80)), "Also sent") {
		t.Error("ctrl+t expands it too")
	}
}

func TestReloadCommand(t *testing.T) {
	a := loadedApp(t)
	typeLine(a, "/reload")
	within(t, a, "the reload block", func() bool { return len(loadedBlocksLocked(a)) == 2 })
	bs := loadedBlocks(a)
	got := plainLines(bs[1].Render(80))
	if !strings.Contains(got, "◇ Reloaded · nothing changed") || !strings.Contains(got, "system prompt unchanged") {
		t.Fatalf("unchanged reload:\n%s", got)
	}

	writeTestFile(t, filepath.Join(a.cwd, "AGENTS.md"), "Project rules, revised.")
	writeTestFile(t, filepath.Join(config.Dir(), "settings.json"), `{"defaultProvider":"t","defaultModel":"m","skills":{"disabled":["atto-extensions"]},"doubleEscapeAction":"none"}`)
	typeLine(a, "/reload")
	within(t, a, "the second reload block", func() bool { return len(loadedBlocksLocked(a)) == 3 })
	bs = loadedBlocks(a)
	got = plainLines(bs[len(bs)-1].Render(80))
	t.Logf("after a change, at 80:\n%s", got)
	for _, s := range []string{
		"◇ Reloaded · 3 changes",
		"changed  AGENTS file ~/proj/AGENTS.md",
		"removed  hook PreToolUse [Bash]: ./check.sh",
		"changed  config ~/.atto/settings.json",
		"system prompt changed; the next request re-reads the prompt",
		"Hooks       none",
	} {
		if !strings.Contains(filepath.ToSlash(got), s) {
			t.Errorf("reload block lacks %q", s)
		}
	}
	within(t, a, "the terminal's settings", func() bool { return a.escAction == "none" })
}

func loadedBlocksLocked(a *App) []*loadedBlock {
	var out []*loadedBlock
	for _, c := range a.ui.Body.Children {
		if g, ok := c.(gap); ok {
			if b, ok := g.Component.(*loadedBlock); ok {
				out = append(out, b)
			}
		}
	}
	return out
}

// reloadServer is a chat completions server: the first request is
// answered with a tool call once release is closed, later ones with text.
// It keeps each request's system prompt and last message.
func reloadServer(t *testing.T, release chan struct{}) (url string, got func() (systems, lasts []string), first chan struct{}) {
	var mu sync.Mutex
	var systems, lasts []string
	first = make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		n := len(systems)
		systems = append(systems, body.Messages[0].Content)
		lasts = append(lasts, body.Messages[len(body.Messages)-1].Content)
		mu.Unlock()
		if n == 0 && release != nil {
			close(first)
			<-release
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"description\":\"Say hi\",\"command\":\"echo hi\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() ([]string, []string) {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), systems...), append([]string(nil), lasts...)
	}, first
}

// `atto reload` run by the agent during a turn is applied at the next step
// boundary: the following request has the new system prompt and the
// reload's report.
func TestAttoReloadBetweenSteps(t *testing.T) {
	release := make(chan struct{})
	url, got, first := reloadServer(t, release)
	a := startApp(t, loadedEnv(t, url))
	typeLine(a, "edit AGENTS.md and reload")
	<-first // the request is in flight: its prompt must not change under it

	writeTestFile(t, filepath.Join(a.cwd, "AGENTS.md"), "Rules the agent just wrote.")
	if err := events.RequestReload(a.threadID); err != nil { // what atto reload does
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond) // the runtime's inbox tick
	close(release)
	waitIdle(t, a)

	systems, lasts := got()
	if len(systems) != 2 {
		t.Fatalf("requests: %d", len(systems))
	}
	if strings.Contains(systems[0], "Rules the agent just wrote.") || !strings.Contains(systems[1], "Rules the agent just wrote.") {
		t.Fatal("the second request must carry the new prompt")
	}
	if !strings.HasPrefix(lasts[1], events.Prefix+"Reload applied. changed AGENTS file") || !strings.Contains(lasts[1], "The system prompt was rebuilt") {
		t.Fatalf("the model is told: %q", lasts[1])
	}
	if bs := loadedBlocks(a); len(bs) != 2 || !bs[1].reloaded {
		t.Fatal("the user sees the reload block")
	}
}

// With no turn running, `atto reload` applies at once and its report starts
// a turn, like any event.
func TestAttoReloadWhenIdle(t *testing.T) {
	url, got, _ := reloadServer(t, nil)
	a := startApp(t, loadedEnv(t, url))
	if err := events.RequestReload(a.threadID); err != nil {
		t.Fatal(err)
	}
	within(t, a, "the reload's turn", func() bool { _, lasts := got(); return len(lasts) == 1 })
	waitIdle(t, a)
	_, lasts := got()
	if len(lasts) != 1 || lasts[0] != events.Prefix+"Reload applied: nothing changed. The system prompt is unchanged." {
		t.Fatalf("requests %q", lasts)
	}
}
