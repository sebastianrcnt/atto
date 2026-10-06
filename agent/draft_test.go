package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/shell"
)

// pieces streams a chat completions tool call: the first piece starts it
// (id and name), the others are slices of its arguments.
func pieces(index int, id, name string, args ...string) []string {
	var out []string
	for i, a := range args {
		fn := map[string]any{"arguments": a}
		call := map[string]any{"index": index, "function": fn}
		if i == 0 {
			call["id"], call["type"] = id, "function"
			fn["name"] = name
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{call}}}}})
		out = append(out, string(b))
	}
	return out
}

func finish(reason string) string {
	return fmt.Sprintf(`{"choices":[{"delta":{},"finish_reason":%q}]}`, reason)
}

// hangServer sends the chunks, then holds the response open until the
// client goes away.
func hangServer(t *testing.T, chunks ...string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// recorder collects the events of a run as short strings.
type recorder struct {
	mu  sync.Mutex
	log []string
}

func (r *recorder) emit(ev any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e := ev.(type) {
	case ToolDraft:
		r.log = append(r.log, fmt.Sprintf("draft %d %q %q", e.Index, e.Args.Description, e.Args.Command))
	case ToolDraftEnd:
		r.log = append(r.log, fmt.Sprintf("draftend %d %q", e.Index, e.Err))
	case ToolStart:
		r.log = append(r.log, fmt.Sprintf("start %d %s %q %q", e.Index, e.ID, e.Args.Description, e.Args.Command))
	case ToolEnd:
		r.log = append(r.log, "end "+e.ID)
	}
}

func (r *recorder) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

func TestToolCallStreamsAsDraft(t *testing.T) {
	args := `{"description":"Write file","command":"cat > a.txt <<'EOF'\nhello\nEOF"}`
	cut := strings.Index(args, "hello")
	call := pieces(0, "c1", "bash", args[:20], args[20:cut], args[cut:cut+3], args[cut+3:])
	srv, _ := fakeServer(t, append(call, finish("tool_calls")), text("done"))
	a := newTestAgent(srv.URL)
	a.Cwd = t.TempDir()
	var rec recorder
	if err := a.Run(context.Background(), "go", rec.emit); err != nil {
		t.Fatal(err)
	}
	got := rec.lines()
	// The call appears at once, fills in as it streams, then the same call
	// starts and ends: no draft is left open.
	want := []string{
		`draft 0 "" ""`,
		`draft 0 "Writ" ""`,
		`draft 0 "Write file" "cat > a.txt <<'EOF'\n"`,
		`draft 0 "Write file" "cat > a.txt <<'EOF'\nhel"`,
		`draft 0 "Write file" "cat > a.txt <<'EOF'\nhello\nEOF"`,
		`start 0 c1 "Write file" "cat > a.txt <<'EOF'\nhello\nEOF"`,
		`end c1`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestToolCallCommandBeforeDescriptionKeepsPreparingHeader(t *testing.T) {
	args := `{"command":"echo hi","description":"Say hi"}`
	cut := strings.Index(args, `,"description"`)
	call := pieces(0, "c1", "bash", args[:cut], args[cut:])
	srv, _ := fakeServer(t, append(call, finish("tool_calls")), text("done"))
	a := newTestAgent(srv.URL)
	var rec recorder
	if err := a.Run(context.Background(), "go", rec.emit); err != nil {
		t.Fatal(err)
	}
	got := rec.lines()
	want := []string{
		`draft 0 "" ""`,
		`draft 0 "" "echo hi"`,
		`draft 0 "Say hi" "echo hi"`,
		`start 0 c1 "Say hi" "echo hi"`,
		`end c1`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestToolCallWithoutDeltasHasNoDraft(t *testing.T) {
	srv, _ := fakeServer(t, toolCall("echo hi"), text("done"))
	a := newTestAgent(srv.URL)
	var rec recorder
	if err := a.Run(context.Background(), "go", rec.emit); err != nil {
		t.Fatal(err)
	}
	for _, l := range rec.lines() {
		if strings.HasPrefix(l, "draftend") {
			t.Fatalf("unexpected %s", l)
		}
	}
}

func TestDraftsEndWhenCallsDoNotRun(t *testing.T) {
	// Three calls: unknown tool, empty command, and a good one.
	calls := append(pieces(0, "c1", "frobnicate", `{"command":"x"}`), pieces(1, "c2", "bash", `{"command":"  "}`)...)
	calls = append(calls, pieces(2, "c3", "bash", `{"description":"Say hi",`, `"command":"echo hi"}`)...)
	srv, _ := fakeServer(t, append(calls, finish("tool_calls")), text("done"))
	a := newTestAgent(srv.URL)
	var rec recorder
	if err := a.Run(context.Background(), "go", rec.emit); err != nil {
		t.Fatal(err)
	}
	var ended, started []string
	for _, l := range rec.lines() {
		switch {
		case strings.HasPrefix(l, "draftend"):
			ended = append(ended, l)
		case strings.HasPrefix(l, "start"):
			started = append(started, l)
		}
	}
	wantEnded := []string{
		`draftend 0 "unknown tool \"frobnicate\"; the only tool is ` + shell.Default().ToolName() + `"`,
		`draftend 1 "command is empty"`,
	}
	if !reflect.DeepEqual(ended, wantEnded) {
		t.Fatalf("ended %q", ended)
	}
	if len(started) != 1 || !strings.HasPrefix(started[0], `start 2 c3 "Say hi"`) {
		t.Fatalf("started %q", started)
	}
}

// blockAll is a PreToolUse hook that denies every call.
type blockAll struct{ noHooks }

func (blockAll) PreToolUse(_ context.Context, a BashArgs) (BashArgs, HookOutcome) {
	return a, HookOutcome{Block: true, Reason: "no"}
}

type noHooks struct{}

func (noHooks) UserPromptSubmit(context.Context, string) HookOutcome { return HookOutcome{} }
func (noHooks) PreToolUse(_ context.Context, a BashArgs) (BashArgs, HookOutcome) {
	return a, HookOutcome{}
}
func (noHooks) PostToolUse(context.Context, BashArgs, BashResult, string) HookOutcome {
	return HookOutcome{}
}
func (noHooks) Stop(context.Context, bool) HookOutcome       { return HookOutcome{} }
func (noHooks) PreCompact(context.Context, bool) HookOutcome { return HookOutcome{} }

func TestDraftEndsWhenHookBlocks(t *testing.T) {
	calls := pieces(0, "c1", "bash", `{"command":"rm`, ` -rf x"}`)
	srv, _ := fakeServer(t, append(calls, finish("tool_calls")), text("ok"))
	a := newTestAgent(srv.URL)
	a.Hooks = blockAll{}
	var rec recorder
	if err := a.Run(context.Background(), "go", rec.emit); err != nil {
		t.Fatal(err)
	}
	last := rec.lines()[len(rec.lines())-1]
	if last != `draftend 0 "blocked by hook"` {
		t.Fatalf("log %q", rec.lines())
	}
}

func TestDraftEndsWhenStreamIsInterrupted(t *testing.T) {
	// The model is part-way through writing a call when the user presses
	// Esc: the half-written call must not stay on screen as pending.
	calls := append(pieces(0, "c1", "bash", `{"command":"echo`, ` one"}`), pieces(1, "c2", "bash", `{"command":"echo tw`)...)
	srv := hangServer(t, calls...)
	a := newTestAgent(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var rec recorder
	done := make(chan error, 1)
	go func() {
		done <- a.Run(ctx, "go", func(ev any) {
			rec.emit(ev)
			if d, ok := ev.(ToolDraft); ok && d.Index == 1 && strings.Contains(d.Args.Command, "tw") {
				cancel()
			}
		})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the run to be canceled")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
	var ended []string
	for _, l := range rec.lines() {
		switch {
		case strings.HasPrefix(l, "draftend"):
			ended = append(ended, l)
		case strings.HasPrefix(l, "start"):
			t.Fatalf("a half-written call ran: %s", l)
		}
	}
	if !reflect.DeepEqual(ended, []string{`draftend 0 ""`, `draftend 1 ""`}) {
		t.Fatalf("ended %q", ended)
	}
}
