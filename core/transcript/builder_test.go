package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// chunk is one chat completions stream event.
func chunk(delta map[string]any, finish string) string {
	c := map[string]any{"delta": delta}
	if finish != "" {
		c["finish_reason"] = finish
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{c}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 2}})
	return "data: " + string(b) + "\n\n"
}

func call(i int, id, args string) map[string]any {
	return map[string]any{"index": i, "id": id, "type": "function", "function": map[string]any{"name": "bash", "arguments": args}}
}

// scripted answers the requests in order.
func scripted(t *testing.T, answers ...[]string) string {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		if i >= len(answers) {
			t.Errorf("unexpected request %d", i+1)
			return
		}
		for _, c := range answers[i] {
			fmt.Fprint(w, c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func say(text string) []string {
	return []string{chunk(map[string]any{"content": text}, "stop")}
}

// stopOnce blocks the first Stop with a reason to keep working.
type stopOnce struct{ done bool }

func (*stopOnce) UserPromptSubmit(context.Context, string) agent.HookOutcome {
	return agent.HookOutcome{}
}
func (*stopOnce) PreToolUse(_ context.Context, a agent.BashArgs) (agent.BashArgs, agent.HookOutcome) {
	return a, agent.HookOutcome{}
}
func (*stopOnce) PostToolUse(context.Context, agent.BashArgs, agent.BashResult, string) agent.HookOutcome {
	return agent.HookOutcome{}
}
func (*stopOnce) PreCompact(context.Context, bool) agent.HookOutcome { return agent.HookOutcome{} }
func (h *stopOnce) Stop(context.Context, bool) agent.HookOutcome {
	if h.done {
		return agent.HookOutcome{}
	}
	h.done = true
	return agent.HookOutcome{Block: true, Reason: "check the tests"}
}

func testImage(t *testing.T) provider.Image {
	t.Helper()
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 3, 2)))
	im, err := images.Prepare(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := images.Save(im); err != nil {
		t.Fatal(err)
	}
	return im
}

// sameTiming zeroes the thinking time, which live is measured by the
// builder and on replay comes from the agent's own clock.
func sameTiming(items []Item) []Item {
	for i := range items {
		if items[i].Kind == Reasoning {
			items[i].Duration = 0
		}
	}
	return items
}

// TestLiveMatchesReplay runs a conversation through a real agent against
// a scripted model, builds items from its events, then from the session
// file it wrote: they must be the same.
func TestLiveMatchesReplay(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	url := scripted(t,
		// Turn 1: reasoning, whitespace text, two commands (one failing);
		// a steer goes in after them.
		[]string{
			chunk(map[string]any{"reasoning_content": "plan"}, ""),
			chunk(map[string]any{"content": "\n\n"}, ""),
			chunk(map[string]any{"tool_calls": []any{
				call(0, "c1", `{"description":"Say hi","command":"echo hi"}`),
				call(1, "c2", `{"command":"exit 3"}`),
			}}, "tool_calls"),
		},
		say("done"),    // the Stop hook sends it back to work
		say("checked"), // ends turn 1
		say("noted"),   // turn 2: an event
		say("going"),   // turn 3: a goal continuation
		say("the notes"),
		say("ok"), // turn 4, after compaction
	)
	ag := agent.New(config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: url},
		Model: config.Model{ID: "m", ContextWindow: 100000, Input: []string{"text", "image"}}}, "", t.TempDir())
	ag.Hooks = &stopOnce{}
	w := session.New(t.TempDir())
	ag.Record, ag.EntryID = w.Append, w.Leaf

	live := Builder{IDPrefix: "x-i"}
	var started, completed []string
	live.Handler = Handler{
		Started:   func(it *Item) { started = append(started, it.ID) },
		Completed: func(it *Item) { completed = append(completed, it.ID) },
	}
	ctx := context.Background()
	run := func(text string, imgs ...provider.Image) {
		t.Helper()
		live.Event(Input{Text: text, Images: imgs})
		if err := ag.RunWithImages(ctx, text, imgs, live.Event); err != nil {
			t.Fatal(err)
		}
		live.End()
	}
	im := testImage(t)
	ag.Steer("also this")
	run("hello [image 1]", im)
	run(events.Prefix + "job 1 exited")
	run(goal.OpenTag + "\nkeep going\n" + goal.CloseTag)
	if err := ag.Compact(ctx, live.Event); err != nil {
		t.Fatal(err)
	}
	live.End()
	run("after")
	w.Close()

	_, entries, err := session.Load(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	replayed := sameTiming(FromEntries("x-i", session.Active(entries)))
	got := sameTiming(live.Items())
	if !reflect.DeepEqual(got, replayed) {
		for i := range max(len(got), len(replayed)) {
			var a, b Item
			if i < len(got) {
				a = got[i]
			}
			if i < len(replayed) {
				b = replayed[i]
			}
			if !reflect.DeepEqual(a, b) {
				t.Errorf("item %d:\n live   %+v %+v\n replay %+v %+v", i, a, a.Result, b, b.Result)
			}
		}
		t.FailNow()
	}

	var kinds []string
	for _, it := range got {
		kinds = append(kinds, string(it.Kind))
	}
	want := "user reasoning tool tool user assistant hook assistant event assistant goal assistant compaction user assistant"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("kinds %v\nwant %s", kinds, want)
	}
	if !reflect.DeepEqual(started, completed) || len(started) != len(got) {
		t.Fatalf("each item completes once, in order: %v / %v", started, completed)
	}
	tool, failed := got[2], got[3]
	if tool.Output != "hi" || tool.Status != Completed || tool.Description != "Say hi" || tool.Result.Text != "hi" {
		t.Fatalf("tool %+v %+v", tool, tool.Result)
	}
	if failed.Status != Failed || failed.Result.ExitCode != 3 || failed.Description != "exit 3" || failed.Output != "" {
		t.Fatalf("failed tool %+v %+v", failed, failed.Result)
	}
	if u := got[0]; len(u.Images) != 1 || u.Images[0].File != im.File || u.Images[0].Data != nil {
		t.Fatalf("user images %+v", u.Images)
	}
	if h := got[6]; h.HookEvent != "Stop" || !h.Blocked || h.Text != "check the tests" {
		t.Fatalf("hook %+v", h)
	}
	if c := got[12]; c.Text != "the notes" || c.TokensBefore == 0 || c.TokensAfter == 0 {
		t.Fatalf("compaction %+v", c)
	}
}

func TestStreamingStates(t *testing.T) {
	var log []string
	b := Builder{Handler: Handler{
		Started:   func(it *Item) { log = append(log, "start "+string(it.Kind)) },
		Delta:     func(it *Item, d string) { log = append(log, fmt.Sprintf("delta %s %q", it.Kind, d)) },
		Completed: func(it *Item) { log = append(log, "done "+string(it.Kind)+" "+string(it.Status)) },
	}}
	b.Event(agent.TextDelta{Text: " \n"}) // nothing to show yet
	b.Event(agent.TextDelta{Text: "Hi"})
	b.Event(agent.StepEnd{})
	b.Event(agent.TextDelta{Text: "\n\n"}) // a step with only whitespace
	b.Event(agent.StepEnd{})
	b.Event(agent.ToolStart{ID: "c", Args: agent.BashArgs{Description: "d", Command: "sleep 9"}, Timeout: time.Minute})
	b.Event(agent.ToolOutput{ID: "c", Chunk: "x\r\n\r\n"})
	b.Event(agent.CompactStart{})
	b.End() // interrupted
	want := []string{
		"start assistant", `delta assistant " \nHi"`, "done assistant completed",
		"start tool", `delta tool "x\r\n\r\n"`,
		"start compaction",
		"done tool failed", "done compaction failed",
	}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("%q", log)
	}
	items := b.Items()
	if tool := items[1]; tool.Output != "x" || !tool.Result.Canceled {
		t.Fatalf("interrupted tool %+v %+v", tool, tool.Result)
	}
}

func TestReplayInterruptedCall(t *testing.T) {
	entries := []session.Entry{
		{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "go"}},
		{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "a", Function: provider.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}},
			{ID: "b", Function: provider.FunctionCall{Name: "bash", Arguments: `{"command":"pwd"}`}},
		}}},
		{Type: session.TypeMessage, Message: &provider.Message{Role: "tool", ToolCallID: "a", Content: "f\n[exit code 2]"},
			Tool: &session.ToolMeta{ExitCode: 2, DurationMs: 5}},
		{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "again"}},
	}
	items := FromEntries("", entries)
	if len(items) != 4 {
		t.Fatalf("%+v", items)
	}
	a, b := items[1], items[2]
	if a.Output != "f" || a.Result.ExitCode != 2 || a.Duration != 5*time.Millisecond || a.Status != Failed || a.Result.Text != "f\n[exit code 2]" {
		t.Fatalf("first call %+v %+v", a, a.Result)
	}
	if b.Command != "pwd" || !b.Result.Canceled || b.Status != Failed || items[3].Text != "again" {
		t.Fatalf("call without a result %+v", b)
	}
}

// Goal messages replay as goal items, the old "[atto goal] " ones too; a
// user's message that merely mentions the tag stays the user's.
func TestReplayGoalMessages(t *testing.T) {
	var entries []session.Entry
	texts := []string{goal.OpenTag + "\nkeep going\n" + goal.CloseTag, "[atto goal] keep going", "fix <atto_internal_context> handling"}
	for _, text := range texts {
		entries = append(entries, session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: text}})
	}
	items := FromEntries("", entries)
	if len(items) != 3 || items[0].Kind != Goal || items[1].Kind != Goal || items[2].Kind != User {
		t.Fatalf("%+v", items)
	}
}

// The goal's state note rides at the end of the user's message for the
// model; the replay shows the message alone.
func TestReplayHidesGoalStateNote(t *testing.T) {
	g, _ := goal.New("x", 0)
	g.Status = goal.Paused
	text := "Thanks, that is fine.\n\n" + g.StateMessage(false)
	items := FromEntries("", []session.Entry{{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: text}}})
	if len(items) != 1 || items[0].Kind != User || items[0].Text != "Thanks, that is fine." {
		t.Fatalf("%+v", items)
	}
}

// A command that moved to the background replays as such, without the
// status line the model got.
func TestReplayBackgroundedCall(t *testing.T) {
	res := agent.BashResult{Output: "compiling\n", Job: 7, Background: agent.BackgroundTimeout, Duration: time.Minute}
	content := res.ForModel(agent.BashArgs{})
	entries := []session.Entry{
		{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "go"}},
		{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "a", Function: provider.FunctionCall{Name: "bash", Arguments: `{"command":"make"}`}},
		}}},
		{Type: session.TypeMessage, Message: &provider.Message{Role: "tool", ToolCallID: "a", Content: content},
			Tool: &session.ToolMeta{DurationMs: 60000, Job: 7, Background: agent.BackgroundTimeout}},
	}
	items := FromEntries("", entries)
	if len(items) != 2 {
		t.Fatalf("%+v", items)
	}
	it := items[1]
	if it.Output != "compiling" || it.Status != Completed || it.Result.Job != 7 || it.Result.Background != agent.BackgroundTimeout {
		t.Fatalf("%+v %+v", it, it.Result)
	}
}

// Images atto view attached to a result are on the tool's item, live and
// replayed, without their bytes.
func TestToolImages(t *testing.T) {
	im := provider.Image{File: "x.png", MIME: "image/png", Width: 3, Height: 2, Name: "shot.png", Data: []byte{1}}
	var live Builder
	live.Event(agent.ToolStart{ID: "a", Args: agent.BashArgs{Command: "atto view shot.png"}})
	live.Event(agent.ToolEnd{ID: "a", Text: "attached", Images: []provider.Image{im}})
	entries := []session.Entry{
		{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "a", Function: provider.FunctionCall{Name: "bash", Arguments: `{"command":"atto view shot.png"}`}},
		}}},
		{Type: session.TypeMessage, Message: &provider.Message{Role: "tool", ToolCallID: "a", Content: "attached", Images: []provider.Image{im}},
			Tool: &session.ToolMeta{}},
	}
	replayed := FromEntries("", entries)
	for _, items := range [][]Item{live.Items(), replayed} {
		it := items[len(items)-1]
		if len(it.Images) != 1 || it.Images[0].Name != "shot.png" || it.Images[0].Data != nil {
			t.Fatalf("%+v", it)
		}
	}
}
