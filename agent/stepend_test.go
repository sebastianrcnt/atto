package agent

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// stepExtensions records StepEnd and ignores the rest.
type stepExtensions struct {
	steps  []StepEnd
	models []string
}

func (x *stepExtensions) UserPrompt(context.Context, string) HookOutcome { return HookOutcome{} }
func (x *stepExtensions) ToolCall(_ context.Context, a BashArgs) (BashArgs, HookOutcome) {
	return a, HookOutcome{}
}
func (x *stepExtensions) ToolResult(_ context.Context, _ BashArgs, _ BashResult, out string) (string, HookOutcome) {
	return out, HookOutcome{}
}
func (x *stepExtensions) TurnStart(string)           {}
func (x *stepExtensions) TurnEnd(error)              {}
func (x *stepExtensions) BlockEnd(_, _, _, _ string) {}
func (x *stepExtensions) StepEnd(e StepEnd, model string) {
	x.steps, x.models = append(x.steps, e), append(x.models, model)
}

// StepEnd splits a response's time at its first streamed output and
// reaches extensions with the model that answered.
func TestStepEndTiming(t *testing.T) {
	srv, _ := scriptedServer(t, func(w http.ResponseWriter) {
		time.Sleep(150 * time.Millisecond) // prompt processing
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond) // generation
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"b\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n")
	})
	a := newTestAgent(srv.URL)
	x := &stepExtensions{}
	a.Extensions = x
	var emitted []StepEnd
	if err := a.Run(context.Background(), "go", func(ev any) {
		if e, ok := ev.(StepEnd); ok {
			emitted = append(emitted, e)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 1 || len(x.steps) != 1 || x.steps[0] != emitted[0] || x.models[0] != "t/m" {
		t.Fatalf("emitted %+v, extensions %+v %v", emitted, x.steps, x.models)
	}
	e := emitted[0]
	if e.TTFT < 140*time.Millisecond || e.TTFT > time.Second || e.Generation < 190*time.Millisecond || e.Generation > time.Second {
		t.Fatalf("ttft %s, generation %s", e.TTFT, e.Generation)
	}
	if e.Usage.CompletionTokens != 7 {
		t.Fatalf("usage %+v", e.Usage)
	}
}
