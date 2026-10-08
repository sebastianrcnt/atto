package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
)

func TestPrintWorkerClient(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	m := providertest.New(t, providertest.Reply{Text: "from the worker", Prompt: 123, Cached: 100, Completion: 12})
	m.Install(t, config.Dir())
	s := server.New("test", t.TempDir())
	t.Cleanup(s.Close)
	c := server.Connect(context.Background(), s)
	t.Cleanup(func() { c.Close() })
	var th server.ThreadInfo
	if err := c.Call(context.Background(), "thread/start", nil, &th); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := printOnClient(PrintOptions{Prompt: "hello", Format: "json"}, th.ID, c, &out, &errOut); err != nil {
		t.Fatalf("%v: %s", err, errOut.String())
	}
	var res printResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Result != "from the worker" || res.IsError || res.SessionID != th.ID || res.NumSteps != 1 || res.Usage.InputTokens != 123 || res.Usage.CachedInputTokens != 100 || res.Usage.OutputTokens != 12 {
		t.Fatalf("result %s (%v)", out.String(), err)
	}
	if len(m.Requests()) != 1 {
		t.Fatalf("requests %v", m.Requests())
	}
}

func TestPrintWorkerWaitsPastStaleIdle(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	gate := make(chan struct{})
	m := providertest.New(t, providertest.Reply{Text: "not premature", Gate: gate})
	m.Install(t, config.Dir())
	s := server.New("test", t.TempDir())
	t.Cleanup(s.Close)
	c := server.Connect(context.Background(), s)
	t.Cleanup(func() { c.Close() })
	var th server.ThreadInfo
	if err := c.Call(context.Background(), "thread/start", nil, &th); err != nil {
		t.Fatal(err)
	}
	// A setter leaves an idle update queued before the print invocation.
	if err := c.Call(context.Background(), "thread/setEffort", map[string]any{"threadId": th.ID, "effort": "low"}, nil); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- printOnClient(PrintOptions{Prompt: "hello"}, th.ID, c, &out, &errOut) }()
	if m.Started(5*time.Second) == 0 {
		t.Fatal("no request")
	}
	select {
	case err := <-done:
		t.Fatalf("returned before model answer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("print never finished")
	}
	if out.String() != "not premature\n" {
		t.Fatalf("output %q", out.String())
	}
}
