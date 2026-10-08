package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// thinkingTime is the one thing a resumed block may word differently: the
// live duration is measured, the saved one rounded to the millisecond.
var thinkingTime = regexp.MustCompile(`Thought for \S+`)

func noDuration(s string) string { return thinkingTime.ReplaceAllString(s, "Thought for X") }

// mainServer answers each request with the next reply: reasoning and text.
// It keeps the request bodies.
type mainServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

type reply struct{ reasoning, text string }

func newMainServer(t *testing.T, replies ...reply) *mainServer {
	s := &mainServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(raw))
		i := min(len(s.bodies), len(replies)) - 1
		s.mu.Unlock()
		rep := replies[i]
		if rep.reasoning != "" {
			j, _ := json.Marshal(rep.reasoning)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":%s}}]}\n\n", j)
		}
		j, _ := json.Marshal(rep.text)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", j)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *mainServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

// useMain points the app's model t/m at the main server.
func useMain(t *testing.T, a *App, s *mainServer, extra string) {
	t.Helper()
	writeTestFile(t, config.ModelsPath(), `{"providers":{"t":{"baseUrl":"`+s.URL+`","models":[{"id":"m","contextWindow":100000}]}`+extra+`}}`)
	models, err := config.LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := models.Find("t", "m")
	a.models = models
	a.agent.SetModel(ref)
}

func runTurn(t *testing.T, a *App, text string) {
	t.Helper()
	a.ui.Do(func() { a.startTurn(text, nil) })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		busy := true
		a.ui.Do(func() { busy = a.turns.Busy })
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn did not finish")
		}
	}
}

// answerBlocks are the reasoning and answer blocks of the transcript.
func answerBlocks(a *App) []displayBlock {
	var out []displayBlock
	for _, c := range a.ui.Body.Children {
		if g, isGap := c.(gap); !isGap {
			continue
		} else if b, ok := g.Component.(displayBlock); ok {
			out = append(out, b)
		}
	}
	return out
}

// answerLines renders just those blocks.
func answerLines(a *App) string {
	var out []string
	for _, b := range answerBlocks(a) {
		out = append(out, b.(tui.Component).Render(80)...)
	}
	return plainLines(out)
}

func blockEntries(t *testing.T, path string) []session.Entry {
	t.Helper()
	_, entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []session.Entry
	for _, e := range entries {
		if e.Type == session.TypeBlockDisplay {
			out = append(out, e)
		}
	}
	return out
}

const upperExtension = `
export default function (atto: any) {
  atto.on("reasoning_end", (e: any, ctx: any) => ctx.ui.setBlockDisplay(e.blockId, e.text.toUpperCase()));
  atto.on("message_end", (e: any, ctx: any) => {
    ctx.ui.setBlockStatus(e.blockId, "working");
    setTimeout(() => {
      ctx.ui.setBlockDisplay(e.blockId, e.text.toUpperCase());
      ctx.ui.setBlockStatus(e.blockId, null);
    }, e.text.startsWith("first") ? 400 : 10);
  });
  atto.registerCommand("restore", { handler: (id: string, ctx: any) => ctx.ui.setBlockDisplay(id, null) });
}
`

func TestBlockDisplayLifecycle(t *testing.T) {
	a := extApp(t, upperExtension)
	srv := newMainServer(t, reply{"deep thought", "first answer"}, reply{"", "second answer"}, reply{"", "third"})
	useMain(t, a, srv, "")

	runTurn(t, a, "q1")
	// The first answer's override is late (400ms): the second turn's
	// overrides arrive first and must each land on their own block.
	within(t, a, "the working status", func() bool { return strings.Contains(answerLines(a), "working") })
	runTurn(t, a, "q2")
	within(t, a, "both answers", func() bool {
		s := answerLines(a)
		return strings.Contains(s, "SECOND ANSWER") && strings.Contains(s, "FIRST ANSWER") && !strings.Contains(s, "working")
	})
	var shown string
	a.ui.Do(func() { shown = answerLines(a) })
	if strings.Count(shown, "FIRST ANSWER") != 1 || strings.Count(shown, "SECOND ANSWER") != 1 {
		t.Fatalf("each block shows its own text:\n%s", shown)
	}
	for _, want := range []string{"∴ Thought for", "· shown: demo (click or ctrl+o to show original)"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("missing %q:\n%s", want, shown)
		}
	}
	if strings.Contains(shown, "first answer") || strings.Contains(shown, "deep thought") {
		t.Fatalf("the original is not shown:\n%s", shown)
	}

	// Persisted: the status and the overrides are in the session file, and
	// a resumed session shows the same blocks without the extension.
	entries := blockEntries(t, a.sess.Path)
	var texts []string
	for _, e := range entries {
		texts = append(texts, e.Block+":"+e.Display)
	}
	if len(entries) != 7 { // reasoning; per answer: status, override, status cleared
		t.Fatalf("block_display entries: %v", texts)
	}
	b := &App{ui: tui.New(nullTerm{}), agent: agent.New(config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}, "", a.cwd),
		cwd: a.cwd, quit: make(chan struct{})}
	b.build()
	b.newSession("")
	b.resume(a.sess.Path)
	t.Cleanup(b.closeSession)
	if got := noDuration(answerLines(b)); got != noDuration(shown) {
		t.Fatalf("resumed:\n%s\nlive:\n%s", got, shown)
	}

	// Toggling: a click on the toggle line flips one block; ctrl+o all of
	// those not chosen by click.
	a.ui.Do(func() {
		blocks := answerBlocks(a)
		first := blocks[1].(*textBlock) // after the reasoning block
		first.Render(80)
		if first.disp.metaLine < 0 || !first.Click(first.disp.metaLine) {
			t.Fatal("the toggle line is clickable")
		}
		if s := plainLines(first.Render(80)); !strings.Contains(s, "first answer") || !strings.Contains(s, "original shown") {
			t.Errorf("clicked:\n%s", s)
		}
		if first.Click(0) {
			t.Error("clicking the text does nothing")
		}
		second := blocks[2].(*textBlock)
		a.onInput("\x0f") // ctrl+o
		if s := plainLines(second.Render(80)); !strings.Contains(s, "second answer") {
			t.Errorf("ctrl+o:\n%s", s)
		}
		if s := plainLines(first.Render(80)); !strings.Contains(s, "first answer") {
			t.Errorf("the clicked block keeps its choice:\n%s", s)
		}
		a.onInput("\x0f")
		if s := plainLines(second.Render(80)); !strings.Contains(s, "SECOND ANSWER") {
			t.Errorf("ctrl+o again:\n%s", s)
		}
		first.Click(first.disp.metaLine) // ctrl+o dropped the block's own choice: this one flips it again
		if s := plainLines(first.Render(80)); !strings.Contains(s, "first answer") {
			t.Errorf("clicked again:\n%s", s)
		}
		first.Click(first.disp.metaLine)
		if s := plainLines(first.Render(80)); !strings.Contains(s, "FIRST ANSWER") {
			t.Errorf("clicked back:\n%s", s)
		}
	})

	// A reasoning block's header keeps its own click: expand and collapse.
	a.ui.Do(func() {
		th := answerBlocks(a)[0].(*thinkingBlock)
		th.Render(80)
		if th.disp.metaLine != 1 || th.Click(1) == false || !th.Click(0) {
			t.Fatal("line 1 toggles the text, line 0 expands")
		}
		if s := plainLines(th.Render(80)); !strings.Contains(s, "deep thought") || !strings.Contains(s, "Show less") || !strings.Contains(s, "original shown") {
			t.Errorf("expanded:\n%s", s)
		}
	})

	// null restores the original, and the toggle goes.
	var id string
	a.ui.Do(func() { id = answerBlocks(a)[1].display().id })
	a.ui.Do(func() { a.runCommand("/restore " + id) })
	within(t, a, "the original", func() bool {
		s := plainLines(answerBlocks(a)[1].(*textBlock).Render(80))
		return strings.Contains(s, "first answer") && !strings.Contains(s, "shown:")
	})

	// The model never saw any of it.
	runTurn(t, a, "q3")
	reqs := srv.requests()
	last := reqs[len(reqs)-1]
	for _, want := range []string{`"first answer"`, `"second answer"`, `"deep thought"`} {
		if !strings.Contains(last, want) {
			t.Errorf("the model's context lacks %s:\n%s", want, last)
		}
	}
	for _, bad := range []string{"FIRST ANSWER", "SECOND ANSWER", "DEEP THOUGHT", `"working"`, "shown: demo"} {
		if strings.Contains(last, bad) {
			t.Errorf("the request mentions %q:\n%s", bad, last)
		}
	}
	// And the agent's messages are as the model wrote them.
	for _, m := range a.agent.Messages() {
		if strings.ToUpper(m.Content) == m.Content && m.Role == "assistant" && m.Content != "" {
			t.Errorf("message changed: %q", m.Content)
		}
	}
}

func TestBlockDisplayLateResultAfterSessionSwitch(t *testing.T) {
	a := extApp(t, `
export default function (atto: any) {
  atto.on("message_end", (e: any, ctx: any) => {
    if (!e.text.startsWith("old")) return;
    setTimeout(() => {
      ctx.ui.setBlockStatus(e.blockId, "late");
      ctx.ui.setBlockDisplay(e.blockId, "LATE");
    }, 300);
  });
}
`)
	srv := newMainServer(t, reply{"", "old answer"}, reply{"", "new answer"})
	useMain(t, a, srv, "")
	runTurn(t, a, "q1")
	old := a.sess.Path
	a.ui.Do(func() { a.cmdClear("") }) // the session switches before the extension answers
	runTurn(t, a, "q2")
	time.Sleep(700 * time.Millisecond)
	var s string
	a.ui.Do(func() { s = bodyText(a) })
	if strings.Contains(s, "LATE") || strings.Contains(s, "late") {
		t.Fatalf("a result for a block of the old session showed:\n%s", s)
	}
	if a.sess.Path == old {
		t.Fatal("same session")
	}
	if n := len(blockEntries(t, old)) + len(blockEntries(t, a.sess.Path)); n != 0 {
		t.Fatalf("%d block_display entries were written", n)
	}
}

// A side model that is down, slow or failing never touches the turn: the
// answer shows at once and the extension marks the block.
func TestSideModelFailuresLeaveTheTurnAlone(t *testing.T) {
	side := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "slow") {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
			return
		}
		http.Error(w, "boom", 500)
	}))
	defer side.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	a := extApp(t, `
export default function (atto: any) {
  atto.on("message_end", async (e: any, ctx: any) => {
    const model = e.text === "dead" ? "d/m" : "s/m";
    ctx.ui.setBlockStatus(e.blockId, "translating…");
    try {
      await atto.complete({ model, prompt: e.text, timeoutMs: 300 });
      ctx.ui.setBlockStatus(e.blockId, "translated");
    } catch (err: any) {
      ctx.ui.setBlockStatus(e.blockId, "translation failed");
    }
  });
}
`)
	srv := newMainServer(t, reply{"", "dead"}, reply{"", "slow"}, reply{"", "500"})
	useMain(t, a, srv, `,"s":{"baseUrl":"`+side.URL+`","models":[{"id":"m"}]},"d":{"baseUrl":"`+deadURL+`","models":[{"id":"m"}]}`)
	for i, q := range []string{"q1", "q2", "q3"} {
		start := time.Now()
		runTurn(t, a, q)
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("turn %d took %s: the side call held it up", i+1, d)
		}
	}
	within(t, a, "every block marked", func() bool {
		return strings.Count(answerLines(a), "translation failed") == 3
	})
	if n := len(srv.requests()); n != 3 {
		t.Fatalf("%d main requests", n)
	}
	if got := bodyText(a); !strings.Contains(got, "• dead") || !strings.Contains(got, "• slow") || !strings.Contains(got, "• 500") {
		t.Fatalf("the answers:\n%s", got)
	}
}
