package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sebastianrcnt/atto/provider/providertest"
)

func TestNegotiate(t *testing.T) {
	for _, c := range []struct {
		in   []int
		want int
	}{{nil, ProtocolVersion}, {[]int{1}, 1}, {[]int{1, 2, 9}, 2}} {
		if got, err := negotiate(c.in); err != nil || got != c.want {
			t.Fatalf("negotiate(%v) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	resp := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersions":[99]}}`))
	if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.Reason != ReasonUnsupportedProtocol {
		t.Fatalf("an unknown revision must be refused with its reason: %+v", resp)
	}
	resp = s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersions":[1,2],"clientInfo":{"name":"t"}}}`))
	b, _ := json.Marshal(resp.Result)
	var r struct {
		Version  int    `json:"protocolVersion"`
		Instance string `json:"serverInstanceId"`
	}
	json.Unmarshal(b, &r)
	if r.Version != 2 || len(r.Instance) != 16 {
		t.Fatalf("initialize: %s", b)
	}
}

// The scripted model drives a turn: a tool call, then the answer, and
// the requests are kept for byte-level checks.
func TestScriptedModelTurn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	m := providertest.New(t, providertest.Reply{Command: "echo hi", Description: "Say hi"}, providertest.Reply{Text: "done"})
	m.Install(t, dir)
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	ch := make(chan string, 100)
	s.Notify = func(method string, _ map[string]any) { ch <- method }
	info, err := s.startThread("", threadParams{})
	if err != nil {
		t.Fatal(err)
	}
	id := info.(ThreadInfo).ID
	if _, err := s.call(context.Background(), "turn/start", json.RawMessage(`{"threadId":"`+id+`","input":"go"}`)); err != nil {
		t.Fatal(err)
	}
	for method := range ch {
		if method == "turn/completed" {
			break
		}
	}
	if n := len(m.Requests()); n != 2 {
		t.Fatalf("%d requests, want 2", n)
	}
}
