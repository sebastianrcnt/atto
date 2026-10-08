package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
)

func TestNegotiate(t *testing.T) {
	for _, c := range []struct {
		versions []int
		want     int
	}{
		{nil, ProtocolVersion}, {[]int{}, ProtocolVersion}, {[]int{1}, 1},
		{[]int{1, 2, 9}, 2}, {[]int{2, 1, 2}, 2},
	} {
		if got, err := negotiate(c.versions); err != nil || got != c.want {
			t.Fatalf("negotiate(%v) = %d, %v; want %d", c.versions, got, err, c.want)
		}
	}
	for _, versions := range [][]int{{99}, {0}, {-1}, {99, -1}} {
		if _, err := negotiate(versions); err == nil {
			t.Fatalf("accepted unsupported revisions %v", versions)
		}
	}
}

func TestInitializeContract(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "live"}[live], func(t *testing.T) {
			s := New("test", t.TempDir())
			if live {
				s.Close()
				s = NewLive("test", &fakeLive{id: "thread"})
			}
			t.Cleanup(s.Close)
			for _, params := range []string{
				`null`,
				`{"protocolVersions":[1,2],"clientInfo":{"name":"test","title":"Test","version":"1"},"capabilities":{"experimentalApi":true,"interactive":true,"images":true}}`,
			} {
				r := s.Handle(context.Background(), []byte(`{"id":1,"method":"initialize","params":`+params+`}`))
				if r.Error != nil {
					t.Fatal(r.Error)
				}
				result := r.Result.(map[string]any)
				if result["name"] != "atto" || result["version"] != "test" || result["protocolVersion"] != 2 || result["serverInstanceId"] != s.instance || len(s.instance) != 16 || result["settings"] == nil {
					t.Fatalf("initialize: %+v", result)
				}
				if live && (result["live"] != true || result["threadId"] != "thread") {
					t.Fatalf("lost live fields: %+v", result)
				}
			}
			r := s.Handle(context.Background(), []byte(`{"id":2,"method":"initialize","params":{"protocolVersions":[1]}}`))
			if r.Error != nil || r.Result.(map[string]any)["protocolVersion"] != 1 {
				t.Fatalf("revision 1: %+v", r)
			}
			r = s.Handle(context.Background(), []byte(`{"id":3,"method":"initialize","params":{"protocolVersions":[99]}}`))
			if r.Error == nil || r.Error.Data == nil || r.Error.Data.Reason != ReasonUnsupportedProtocol {
				t.Fatalf("unsupported revision: %+v", r)
			}
			if r = s.Handle(context.Background(), []byte(`{"method":"initialized"}`)); r != nil {
				t.Fatalf("notification returned a response: %+v", r)
			}
			if _, err := s.call(context.Background(), "initialized", nil); err != nil {
				t.Fatalf("initialized not recognized: %v", err)
			}
		})
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
