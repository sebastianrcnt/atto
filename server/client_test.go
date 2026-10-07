package server

import (
	"context"
	"encoding/json"
	"io"
	"sync"
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

// lockedView is a view its follower goroutine updates.
type lockedView struct {
	mu sync.Mutex
	v  ThreadView
}

func (l *lockedView) items() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, _ := json.Marshal(l.v.Items)
	return string(b)
}

// follow keeps a view of thread id from c's events, after the snapshot
// it reads now; the methods it applied are sent on the channel.
func follow(t *testing.T, c *Client, id string) (*lockedView, <-chan string) {
	t.Helper()
	var info ThreadInfo
	if err := c.Call(context.Background(), "thread/read", map[string]any{"threadId": id}, &info); err != nil {
		t.Fatal(err)
	}
	v := &lockedView{}
	v.v.Reset(info)
	methods := make(chan string, 4096)
	go func() {
		for n := range c.Events() {
			v.mu.Lock()
			ok := v.v.Apply(n)
			v.mu.Unlock()
			if ok {
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
	if err := a.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{2}, "clientInfo": map[string]string{"name": "a"}}, &init); err != nil || init.ClientID == "" || init.Version != 2 {
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
	}
	var fresh ThreadInfo
	if err := c.Call(ctx, "thread/read", map[string]any{"threadId": th.ID}, &fresh); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(fresh.Items)
	for name, v := range map[string]*lockedView{"in-process": va, "stdio": vb, "late": vc} {
		// The notifications after turn/completed (the closing notice) may
		// still be on their way.
		deadline := time.Now().Add(5 * time.Second)
		for v.items() != string(want) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := v.items(); got != string(want) {
			t.Fatalf("%s client:\n%s\nwant\n%s", name, got, want)
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
			Timeout: 2 * time.Minute, Duration: time.Second, Result: &transcript.ToolResult{ExitCode: 1, Canceled: true, Err: "boom"}},
		{ID: "4", Kind: transcript.Shell, Status: transcript.Completed, Command: "pwd", Output: "/", Excluded: true, Truncated: true, FullOutput: "/f", Result: &transcript.ToolResult{ExitCode: 2}},
		{ID: "5", Kind: transcript.GoalStatus, Status: transcript.Completed, GoalState: g},
		{ID: "6", Kind: transcript.Compaction, Status: transcript.Completed, Auto: true, TokensBefore: 10, TokensAfter: 3, Duration: time.Second, Text: "notes"},
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
