// Package agent is the loop that makes an agent: ask the model, run the
// code it wrote on the agent's machine, give it the result, again, until
// it answers.
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
	Name     string // its path, e.g. /root
	Cortex   *cortex.Cortex
	Machine  *machine.Machine
	Model    *model.Client
	MaxSteps int

	// Trace, when set, sees each step: the code run and its output.
	Trace func(Event)
}

// Event is something that happened in a step.
type Event struct {
	Kind string // thinking, code, output, answer
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

// Run gives the agent input and runs it until it answers.
func (a *Agent) Run(ctx context.Context, input string) (string, error) {
	a.Cortex.Add(model.Message{Role: "user", Content: input})
	steps := a.MaxSteps
	if steps == 0 {
		steps = 30
	}
	for step := 0; step < steps; step++ {
		reply, usage, err := a.Model.Complete(ctx, a.Cortex.Messages(), []model.Tool{luaTool})
		if err != nil {
			return "", err
		}
		a.Cortex.Tokens = usage.PromptTokens
		a.Cortex.Add(reply)
		if reply.Reasoning != "" {
			a.trace("thinking", reply.Reasoning)
		}
		if len(reply.ToolCalls) == 0 {
			a.trace("answer", reply.Content)
			return reply.Content, nil
		}
		if strings.TrimSpace(reply.Content) != "" {
			a.trace("thinking", reply.Content)
		}
		for _, call := range reply.ToolCalls {
			a.Cortex.Add(model.Message{Role: "tool", ToolCallID: call.ID, Content: a.call(ctx, call)})
		}
	}
	return "", fmt.Errorf("no answer after %d steps", steps)
}

// call runs one tool call and returns what the model gets back.
func (a *Agent) call(ctx context.Context, call model.ToolCall) string {
	if call.Function.Name != "lua" {
		return fmt.Sprintf("error: there is no tool %q; the only tool is lua", call.Function.Name)
	}
	var args struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "error: arguments are not JSON with a code string: " + err.Error()
	}
	a.trace("code", args.Code)
	out, err := a.Machine.Run(ctx, args.Code)
	if err != nil {
		out += "error: " + err.Error() + "\n"
	}
	if out == "" {
		out = "(no output; commands print, values need print or return)\n"
	}
	a.trace("output", out)
	return out
}

func (a *Agent) trace(kind, text string) {
	if a.Trace != nil {
		a.Trace(Event{kind, text})
	}
}
