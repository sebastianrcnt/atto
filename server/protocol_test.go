package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
)

func TestNegotiate(t *testing.T) {
	for _, versions := range [][]int{{3}, {3, 2}, {1, 2, 3}, {3, 99}} {
		if got, err := negotiate(versions); err != nil || got != ProtocolVersion {
			t.Fatalf("negotiate(%v) = %d, %v; want %d", versions, got, err, ProtocolVersion)
		}
	}
	for _, versions := range [][]int{nil, {}, {1}, {2}, {1, 2}, {99}, {0}, {-1}, {99, -1}} {
		_, err := negotiate(versions)
		if err == nil {
			t.Fatalf("accepted unsupported revisions %v", versions)
		}
		if msg := err.Error(); !strings.Contains(msg, "revision 3") {
			t.Fatalf("negotiate(%v) error does not name the supported revision: %v", versions, msg)
		}
	}
}

func TestInitializeContract(t *testing.T) {
	s := New("test", t.TempDir())
	ctx := context.Background()
	t.Cleanup(s.Close)
	r := s.Handle(ctx, []byte(`{"id":1,"method":"initialize","params":{"protocolVersions":[2,3],"clientInfo":{"name":"test","title":"Test","version":"1"},"capabilities":{"experimentalApi":true,"interactive":true,"images":true}}}`))
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	result := r.Result.(map[string]any)
	if result["name"] != "atto" || result["version"] != "test" || result["protocolVersion"] != 3 || result["serverInstanceId"] != s.instance || len(s.instance) != 16 || result["settings"] == nil {
		t.Fatalf("initialize: %+v", result)
	}
	for _, params := range []string{`null`, `{}`, `{"protocolVersions":[1]}`, `{"protocolVersions":[1,2]}`, `{"protocolVersions":[99]}`} {
		r := s.Handle(ctx, []byte(`{"id":2,"method":"initialize","params":`+params+`}`))
		if r.Error == nil || r.Error.Data == nil || r.Error.Data.Reason != ReasonUnsupportedProtocol || !strings.Contains(r.Error.Message, "revision 3") {
			t.Fatalf("params %s: want unsupportedProtocol naming revision 3: %+v", params, r)
		}
	}
	if r = s.Handle(ctx, []byte(`{"method":"initialized"}`)); r != nil {
		t.Fatalf("notification returned a response: %+v", r)
	}
	if _, err := s.call(context.Background(), "initialized", nil); err != nil {
		t.Fatalf("initialized not recognized: %v", err)
	}
}

func TestRPCErrorReasons(t *testing.T) {
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	for _, c := range []struct {
		raw, reason string
		code        int
	}{
		{`{`, ReasonParse, codeParse},
		{`{"id":1,"method":"missing"}`, ReasonMethodNotFound, codeMethodNotFound},
		{`{"id":2,"method":"initialize","params":{"protocolVersions":"wrong"}}`, ReasonInvalidParams, codeInvalidParams},
	} {
		r := s.Handle(context.Background(), []byte(c.raw))
		if r.Error == nil || r.Error.Code != c.code || r.Error.Data == nil || r.Error.Data.Reason != c.reason {
			t.Fatalf("%s: %+v", c.raw, r)
		}
		b, err := json.Marshal(r)
		if err != nil || !json.Valid(b) {
			t.Fatalf("error not encodable: %s, %v", b, err)
		}
	}
	other := New("test", t.TempDir())
	t.Cleanup(other.Close)
	if s.instance == other.instance {
		t.Fatal("server instance IDs must distinguish separate runs")
	}
}

func TestScriptedModelTurn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	m := providertest.New(t, providertest.Reply{Command: "echo hi", Description: "Say hi"}, providertest.Reply{Text: "done"})
	m.Install(t, dir)
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	done := make(chan struct{}, 1)
	s.Notify = func(method string, _ map[string]any) {
		if method == "turn/completed" {
			done <- struct{}{}
		}
	}
	info, err := s.startThread("", threadParams{})
	if err != nil {
		t.Fatal(err)
	}
	c := Connect(context.Background(), s)
	defer c.Close()
	if err := c.Call(context.Background(), "turn/start", map[string]any{"threadId": info.(ThreadInfo).ID, "input": "go"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("turn did not finish")
	}
	if n := len(m.Requests()); n != 2 {
		t.Fatalf("%d requests, want 2", n)
	}
}
