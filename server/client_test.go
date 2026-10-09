package server

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/provider/providertest"
)

// testServer is a server with the scripted model installed.
func testServer(t *testing.T, script ...providertest.Reply) (*Server, *providertest.Model) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("HOME", t.TempDir())
	m := providertest.New(t, script...)
	m.Install(t, dir)
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	return s, m
}

// follow keeps a view of thread id from c's events, after the snapshot
// it reads now; views are sent on out after each change.
func follow(t *testing.T, c *Client, id string) (*ThreadView, <-chan string) {
	t.Helper()
	var info ThreadInfo
	if err := c.Call(context.Background(), "thread/read", map[string]any{"threadId": id}, &info); err != nil {
		t.Fatal(err)
	}
	v := &ThreadView{}
	v.Reset(info)
	methods := make(chan string, 4096)
	go func() {
		for n := range c.Events() {
			if v.Apply(n) {
				methods <- n.Method
			}
		}
		close(methods)
	}()
	return v, methods
}

func waitMethod(t *testing.T, ch <-chan string, method string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatal("events ended")
			}
			if m == method {
				return
			}
		case <-deadline:
			t.Fatalf("no %s", method)
		}
	}
}

// Clients in the same process, over stdio and joining mid-stream all end
// with the same transcript as a fresh read: the snapshot cursor plus the
// events after it apply every change exactly once.
func TestClientsSeeTheSameThread(t *testing.T) {
	gate := make(chan struct{})
	s, m := testServer(t, providertest.Reply{Reasoning: "plan", Text: "the answer comes in many small pieces", Words: 12, Delay: 5 * time.Millisecond, Gate: gate})
	ctx := context.Background()
	a := Connect(ctx, s)
	defer a.Close()
	var init struct {
		ClientID string `json:"clientId"`
		Version  int    `json:"protocolVersion"`
	}
	if err := a.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{3}, "clientInfo": map[string]string{"name": "a"}}, &init); err != nil || init.ClientID == "" || init.Version != 3 {
		t.Fatalf("initialize %+v %v", init, err)
	}
	var th ThreadInfo
	if err := a.Call(ctx, "thread/start", map[string]any{}, &th); err != nil {
		t.Fatal(err)
	}
	va, ea := follow(t, a, th.ID)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go s.ServeStdio(ctx, inR, outW)
	b := NewClient(struct {
		io.Reader
		io.Writer
		io.Closer
	}{outR, inW, inW})
	defer b.Close()
	vb, eb := follow(t, b, th.ID)

	if err := a.Call(ctx, "turn/start", map[string]any{"threadId": th.ID, "input": "hi"}, nil); err != nil {
		t.Fatal(err)
	}
	m.Started(5 * time.Second)
	close(gate)
	time.Sleep(20 * time.Millisecond) // somewhere in the stream
	c := Connect(ctx, s)
	defer c.Close()
	vc, ec := follow(t, c, th.ID)
	for _, ch := range []<-chan string{ea, eb, ec} {
		waitMethod(t, ch, "turn/completed")
		waitMethod(t, ch, "thread/updated")
	}
	var fresh ThreadInfo
	if err := c.Call(ctx, "thread/read", map[string]any{"threadId": th.ID}, &fresh); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(fresh.Items)
	for name, v := range map[string]*ThreadView{"in-process": va, "stdio": vb, "late": vc} {
		got, _ := json.Marshal(v.Items)
		if string(got) != string(want) {
			t.Fatalf("%s client:\n%s\nwant\n%s", name, got, want)
		}
		if v.Info.Busy {
			t.Fatalf("%s client thinks the turn still runs", name)
		}
	}
}

// What the TUI renders survives the wire: a protocol item maps back to
// the transcript item it came from.
func TestTranscriptItemRoundTrip(t *testing.T) {
	g := &goal.Goal{Objective: "ship", Status: goal.Blocked, Note: "stuck"}
	items := []transcript.Item{
		{ID: "1", Kind: transcript.User, Text: "hi [image 1]", Status: transcript.Completed, Images: []provider.Image{{File: "ab.png", MIME: "image/png", Width: 3, Height: 2}}},
		{ID: "2", Kind: transcript.Reasoning, Text: "hm", Status: transcript.Completed, Duration: 1500 * time.Millisecond, EntryID: "e1"},
		{ID: "3", Kind: transcript.Tool, Status: transcript.Completed, Description: "d", Command: "ls", Output: "x", Dropped: 9, CallID: "c1",
			Timeout: 2 * time.Minute, Duration: time.Second, Result: &transcript.ToolResult{ExitCode: 1, Canceled: true, Err: "boom", Text: "full output reference"}},
		{ID: "4", Kind: transcript.Shell, Status: transcript.Completed, Command: "pwd", Output: "/", Excluded: true, Truncated: true, FullOutput: "/f", Result: &transcript.ToolResult{ExitCode: 2}},
		{ID: "5", Kind: transcript.GoalStatus, Status: transcript.Completed, GoalState: g},
		{ID: "6", Kind: transcript.Compaction, Status: transcript.Completed, Auto: true, Reason: "priceTier", Cap: 500, TokensBefore: 10, TokensAfter: 3, Duration: time.Second, Text: "notes"},
		{ID: "7", Kind: transcript.Hook, Status: transcript.Completed, HookEvent: "Stop", Blocked: true, Text: "no"},
		{ID: "8", Kind: transcript.ExtText, Status: transcript.Completed, Title: "t", Ext: "e", Lang: "diff", Preview: 4, Text: "+x"},
		{ID: "9", Kind: transcript.Tool, Status: transcript.Completed, Command: "sleep 9", Result: &transcript.ToolResult{Job: 3, Background: "user"}},
	}
	for _, it := range items {
		got := TranscriptItem(wireItem("s", &it))
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(it)
		if string(a) != string(b) {
			t.Fatalf("round trip of %s:\n%s\nwant\n%s", it.Kind, a, b)
		}
	}
}

func TestThreadViewAppliesEventsOnce(t *testing.T) {
	v := &ThreadView{}
	v.Reset(ThreadInfo{ID: "s", EventID: 10, Items: []Item{{ID: "i", Type: ItemAgent, Text: "a"}}})
	for _, n := range []Notification{
		{Method: "item/delta", EventID: 10, Params: json.RawMessage(`{"threadId":"s","itemId":"i","delta":"old"}`)},
		{Method: "item/delta", EventID: 11, Params: json.RawMessage(`{"threadId":"other","itemId":"i","delta":"other"}`)},
	} {
		if v.Apply(n) {
			t.Fatal("applied an event outside the view's cursor/thread")
		}
	}
	n := Notification{Method: "item/delta", EventID: 12, Params: json.RawMessage(`{"threadId":"s","itemId":"i","delta":"b"}`)}
	if !v.Apply(n) || v.Apply(n) || v.Items[0].Text != "ab" || v.EventID != 12 {
		t.Fatalf("delta not applied exactly once: %+v", v)
	}
}

func TestConnectionEOFDoesNotInterruptTurn(t *testing.T) {
	gate := make(chan struct{})
	s, model := testServer(t, providertest.Reply{Text: "finished unattended", Gate: gate})
	ctx := context.Background()
	c := Connect(ctx, s)
	var th ThreadInfo
	if err := c.Call(ctx, "thread/start", nil, &th); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "turn/start", map[string]any{"threadId": th.ID, "input": "go"}, nil); err != nil {
		t.Fatal(err)
	}
	if model.Started(5*time.Second) != 1 {
		t.Fatal("model was not called")
	}
	c.Close()
	close(gate)
	other := Connect(ctx, s)
	defer other.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var fresh ThreadInfo
		if err := other.Call(ctx, "thread/read", map[string]any{"threadId": th.ID}, &fresh); err != nil {
			t.Fatal(err)
		}
		if !fresh.Busy {
			found := false
			for _, item := range fresh.Items {
				found = found || item.Text == "finished unattended"
			}
			if !found {
				t.Fatalf("detached turn did not finish: %+v", fresh.Items)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detached turn stayed busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestThreadViewRuntimeStateAndReset(t *testing.T) {
	v := &ThreadView{}
	v.Reset(ThreadInfo{ID: "s", EventID: 4})
	for _, n := range []Notification{
		{Method: "turn/started", EventID: 5, Params: json.RawMessage(`{"threadId":"s","turnId":"t","startedAt":123,"runKind":"turn","activity":"Thinking"}`)},
		{Method: "turn/activity", EventID: 6, Params: json.RawMessage(`{"threadId":"s","activity":{"phase":"Working","runKind":"turn","startedAt":123,"toolsRunning":1}}`)},
		{Method: "thread/status", EventID: 7, Params: json.RawMessage(`{"threadId":"s","jobs":2,"timers":3}`)},
	} {
		if !v.Apply(n) {
			t.Fatalf("not reduced: %+v", n)
		}
	}
	if !v.Info.Busy || v.Info.Turn == nil || v.Info.Turn.StartedAt != 123 || v.Info.Activity.ToolsRunning != 1 || v.Info.Jobs != 2 || v.Info.Timers != 3 {
		t.Fatalf("state: %+v", v.Info)
	}
	if !v.Apply(Notification{Method: "events/reset", Params: json.RawMessage(`{"eventId":1,"serverInstanceId":"new"}`)}) || !v.NeedsSnapshot {
		t.Fatal("reset did not request a new snapshot")
	}
	v.Reset(ThreadInfo{ID: "s", EventID: 1})
	if v.NeedsSnapshot || v.EventID != 1 {
		t.Fatal("snapshot did not clear reset")
	}
	if !v.Apply(Notification{Method: "thread/branchChanged", EventID: 2, Params: json.RawMessage(`{"threadId":"s"}`)}) || !v.NeedsSnapshot {
		t.Fatal("branch change did not request a new snapshot")
	}
}

func TestThreadViewClipsLiveCommandOutputLikeBuilder(t *testing.T) {
	var b transcript.Builder
	b.Event(transcript.ShellStart{Command: "make"})
	v := ThreadView{}
	items := b.Items()
	v.Reset(ThreadInfo{ID: "s", Items: []Item{wireItem("s", &items[0])}})
	for i, chunk := range []string{strings.Repeat("a", 100*1024), strings.Repeat("b", 40*1024), "last"} {
		b.Event(transcript.ShellOutput{Chunk: chunk})
		params, _ := json.Marshal(map[string]any{"threadId": "s", "itemId": v.Items[0].ID, "delta": chunk})
		if !v.Apply(Notification{Method: "item/delta", EventID: int64(i + 1), Params: params}) {
			t.Fatal("delta not applied")
		}
		want := b.Items()[0]
		if it := v.Items[0]; it.Output != want.Output || it.Dropped != want.Dropped {
			t.Fatalf("live clipping differs: %d/%d bytes, %d/%d dropped", len(it.Output), len(want.Output), it.Dropped, want.Dropped)
		}
	}
}
