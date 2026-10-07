package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atto2/cortex"
	"atto2/kernel"
	"atto2/machine"
	"atto2/model"
)

func mockAgent(t *testing.T, replies []model.Message) (*Agent, *[]Event) {
	t.Helper()
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []model.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if n == 1 && len(replies[0].ToolCalls) == 0 {
			last := req.Messages[len(req.Messages)-1]
			if last.Content != "Your text is only your own stdout; results leave through sys.exit." {
				t.Errorf("missing reminder: %+v", last)
			}
		}
		reply := replies[min(n, len(replies)-1)]
		n++
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": reply}}, "usage": map[string]any{"prompt_tokens": 123}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	k, err := kernel.Project(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := machine.New(k)
	t.Cleanup(m.Close)
	events := []Event{}
	return &Agent{Machine: m, Cortex: cortex.New(cortex.Instructions(k, ".")), Model: &model.Client{BaseURL: server.URL}, MaxSteps: 3, Trace: func(e Event) { events = append(events, e) }}, &events
}
func luaReply(code string) model.Message {
	c := model.ToolCall{ID: "call", Type: "function"}
	c.Function.Name = "lua"
	raw, _ := json.Marshal(map[string]string{"code": code})
	c.Function.Arguments = string(raw)
	return model.Message{Role: "assistant", ToolCalls: []model.ToolCall{c}}
}
func TestStdoutThenExit(t *testing.T) {
	a, events := mockAgent(t, []model.Message{{Role: "assistant", Content: "my stdout"}, luaReply(`return 1+1`), luaReply(`sys.exit{report="the result"}`)})
	report, err := a.Run(context.Background(), "input")
	if err != nil || report != "the result" || a.Steps != 3 || a.Cortex.Tokens != 123 {
		t.Fatalf("%s %v %+v", report, err, a)
	}
	if len(*events) == 0 || (*events)[0].Kind != "stdout" || (*events)[0].Text != "my stdout" {
		t.Fatal(events)
	}
	if a.Machine.Kernel.PureRuns != 1 || a.Machine.Kernel.ImpureRuns != 1 {
		t.Fatal(a.Machine.Kernel)
	}
}
func TestLifeLimit(t *testing.T) {
	a, _ := mockAgent(t, []model.Message{{Role: "assistant", Content: "not an exit"}})
	report, err := a.Run(context.Background(), "input")
	if report != "" || err == nil || err.Error() != "life ended without exit after 3 steps" {
		t.Fatalf("%q %v", report, err)
	}
}
func TestExitSkipsRemainingCalls(t *testing.T) {
	reply := luaReply(`sys.exit("done")`)
	reply.ToolCalls = append(reply.ToolCalls, luaReply(`sys.now()`).ToolCalls...)
	a, _ := mockAgent(t, []model.Message{reply})
	report, err := a.Run(context.Background(), "input")
	if err != nil || report != "done" || len(a.Machine.Kernel.Log) != 1 {
		t.Fatalf("%q %v", report, err)
	}
}
func TestMalformedTools(t *testing.T) {
	a, _ := mockAgent(t, []model.Message{{}})
	c := luaReply(`return 1`).ToolCalls[0]
	c.Function.Arguments = `{"code":false}`
	if out := a.call(context.Background(), c); !strings.Contains(out, "use lua with JSON arguments") {
		t.Fatal(out)
	}
	for _, raw := range []string{`{}`, `{"code":null}`} {
		c.Function.Arguments = raw
		if out := a.call(context.Background(), c); !strings.Contains(out, "use lua with JSON arguments") {
			t.Fatal(out)
		}
	}
	c.Function.Name = "bash"
	if out := a.call(context.Background(), c); !strings.Contains(out, "only tool is lua") {
		t.Fatal(out)
	}
}
