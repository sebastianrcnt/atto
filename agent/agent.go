// Package agent is the loop that makes an agent: ask the model, run the
// code it wrote on the agent's machine, give it the result, again, until
// sys.exit ends its life.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"atto2/cortex"
	"atto2/machine"
	"atto2/model"
)

// Agent is one agent: its mind (cortex), its computer (machine) and the
// model it thinks with.
type Agent struct {
	Name             string // its path, e.g. /root
	Cortex           *cortex.Cortex
	Machine          *machine.Machine
	Model            Completer
	MaxSteps         int
	Steps            int
	CompletionTokens int

	// Trace, when set, sees each step: the code run and its output.
	Trace   func(Event)
	Journal *Journal
}

type Completer interface {
	Complete(context.Context, []model.Message, []model.Tool) (model.Message, model.Usage, error)
}

// Event is something that happened in a step.
type Event struct {
	Kind string // thinking, code, output, stdout
	Text string
}

// luaTool is the one tool an agent has.
var luaTool = model.Tool{
	Name:        "lua",
	Description: "Run Lua 5.1 code on your machine and get what it printed and returned.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"code": map[string]any{"type": "string", "description": "Lua code to run"},
		},
		"required": []string{"code"},
	},
}

// Run gives the agent input and runs it until sys.exit.
func (a *Agent) Run(ctx context.Context, input string) (report string, err error) {
	if a.Journal != nil {
		defer func() {
			if e := a.Journal.Finish(report, err); e != nil {
				err = e
			}
		}()
	}
	a.Cortex.Add(model.Message{Role: "user", Content: input})
	steps := a.MaxSteps
	if steps == 0 {
		steps = 30
	}
	for step := 1; step <= steps; step++ {
		a.Steps = step
		if err := a.step(ctx); err != nil {
			return "", err
		}
		if a.Machine.Kernel != nil && a.Machine.Kernel.Exited {
			return a.Machine.Kernel.Report, nil
		}
	}
	return "", fmt.Errorf("life ended without exit after %d steps", steps)
}

func (a *Agent) step(ctx context.Context) (err error) {
	reply, usage, err := a.Model.Complete(ctx, a.Cortex.Messages(), []model.Tool{luaTool})
	if err != nil {
		return err
	}
	if a.Journal != nil {
		a.Journal.Begin(a.Steps, reply, usage)
		defer func() {
			if e := a.Journal.EndStep(); e != nil {
				err = e
			}
		}()
	}
	a.Cortex.Tokens = usage.PromptTokens
	a.CompletionTokens += usage.CompletionTokens
	a.Cortex.Add(reply)
	a.traceReply(reply)
	if len(reply.ToolCalls) == 0 {
		a.Cortex.Add(model.Message{Role: "user", Content: "Your text is only your own stdout; results leave through sys.exit."})
		return nil
	}
	for _, call := range reply.ToolCalls {
		out := a.call(ctx, call)
		a.Cortex.Add(model.Message{Role: "tool", ToolCallID: call.ID, Content: out})
		if a.Machine.Kernel != nil && a.Machine.Kernel.Exited {
			break
		}
	}
	return nil
}

// call runs one tool call and returns what the model gets back.
func (a *Agent) call(ctx context.Context, call model.ToolCall) (out string) {
	code := ""
	defer func() {
		a.trace("output", out)
		if a.Journal != nil {
			a.Journal.Tool(call, code, out)
		}
	}()
	if call.Function.Name != "lua" {
		return fmt.Sprintf("error: there is no tool %q; the only tool is lua", call.Function.Name)
	}
	var args struct {
		Code *string `json:"code"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args.Code == nil {
		return `error: use lua with JSON arguments {"code": "Lua code"}`
	}
	code = *args.Code
	a.trace("code", code)
	result, err := a.Machine.Run(ctx, *args.Code)
	out = result.Output
	if err != nil {
		out += "error: " + err.Error() + "\n"
	}
	if out == "" {
		out = "(no output)\n"
	}
	return out
}

func (a *Agent) trace(kind, text string) {
	if a.Trace != nil {
		a.Trace(Event{kind, text})
	}
}

func (a *Agent) traceReply(reply model.Message) {
	if reply.Reasoning != "" {
		a.trace("thinking", reply.Reasoning)
	}
	if strings.TrimSpace(reply.Content) != "" {
		a.trace("stdout", reply.Content)
	}
}
