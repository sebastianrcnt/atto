package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/sebastianrcnt/atto/ai"
)

// Client streams requests for one model through package ai.
type Client struct {
	Model  ai.Model
	APIKey string
	// KeyFunc, if set, supplies the key per request (OAuth refresh) and
	// takes precedence over APIKey.
	KeyFunc func(context.Context) (string, error)
	// Headers are added to every request; "$session" becomes the session
	// ID and the header is skipped when there is none.
	Headers map[string]string
	HTTP    *http.Client
	// OnRequest, if set, receives every request body before it is sent.
	OnRequest func(body []byte)
}

// noToolChoice holds the models (provider/id) that refused a tool_choice:
// some OpenAI-compatible servers take only "auto".
var noToolChoice sync.Map

// Stream sends req and streams the response. On error (including context
// cancellation) the returned Result holds whatever was received so far.
//
// A tool_choice the server refuses is dropped and the request sent again,
// once, before anything streamed; the model is then sent none from here on.
// Compaction and summaries ask for "none" only to keep the tools in the
// prefix without calls, and their callers ignore a call made anyway.
func (c *Client) Stream(ctx context.Context, req Request, h Handler) (Result, error) {
	key := c.Model.Provider + "/" + c.Model.ID
	if _, ok := noToolChoice.Load(key); ok {
		req.ToolChoice = ""
	}
	streamed := false
	res, err := c.stream(ctx, req, h, &streamed)
	if err != nil && req.ToolChoice != "" && !streamed && ctx.Err() == nil && toolChoiceRefused(err) {
		noToolChoice.Store(key, true)
		req.ToolChoice = ""
		return c.stream(ctx, req, h, &streamed)
	}
	return res, err
}

// toolChoiceRefused reports whether an error is a server refusing the
// request's tool_choice.
func toolChoiceRefused(err error) bool {
	m := err.Error()
	return strings.Contains(m, "tool_choice") && (strings.Contains(m, "400") || strings.Contains(m, "invalid") || strings.Contains(m, "not supported"))
}

func (c *Client) stream(ctx context.Context, req Request, h Handler, streamed *bool) (Result, error) {
	model := c.Model
	key := c.APIKey
	if c.KeyFunc != nil {
		k, err := c.KeyFunc(ctx)
		if err != nil {
			return Result{Message: Message{Role: "assistant"}}, err
		}
		key = k
	}
	headers := map[string]string{"User-Agent": UserAgent}
	for k, v := range c.Headers {
		if v == "$session" {
			if req.SessionID == "" {
				continue
			}
			v = req.SessionID
		}
		headers[k] = v
	}
	opts := &ai.SimpleStreamOptions{
		Context: ctx, APIKey: key, HTTPClient: c.HTTP, Headers: headers,
		SessionID: req.SessionID, MaxTokens: req.MaxTokens, OnRequestBody: c.OnRequest,
		ToolChoice: req.ToolChoice,
		Reasoning:  req.Effort,
	}
	stream := ai.StreamSimple(&model, ToContext(&model, req.Messages, req.Tools), opts)

	toolIndex := map[int]int{}             // content index -> tool call index
	toolArgs := map[int]*strings.Builder{} // tool call index -> arguments so far
	var final *ai.AssistantMessage
	for ev := range stream.All() {
		switch ev.Type {
		case ai.EventThinkingDelta, ai.EventTextDelta, ai.EventToolCallStart:
			*streamed = true
		}
		switch ev.Type {
		case ai.EventThinkingDelta:
			if h.OnReasoning != nil {
				h.OnReasoning(ev.Delta)
			}
		case ai.EventTextDelta:
			if h.OnText != nil {
				h.OnText(ev.Delta)
			}
		case ai.EventToolCallStart:
			toolIndex[ev.ContentIndex] = len(toolIndex)
			if h.OnToolCallStart != nil {
				h.OnToolCallStart(toolIndex[ev.ContentIndex])
			}
		case ai.EventToolCallDelta:
			// The partial message is not read: the producer is still
			// changing it. The deltas carry what is needed.
			if h.OnToolCallDelta != nil && ev.Delta != "" {
				i := toolIndex[ev.ContentIndex]
				if toolArgs[i] == nil {
					toolArgs[i] = &strings.Builder{}
				}
				toolArgs[i].WriteString(ev.Delta)
				h.OnToolCallDelta(i, toolArgs[i].String())
			}
		case ai.EventToolCallEnd:
			if h.OnToolCall != nil && ev.ToolCall != nil {
				h.OnToolCall(toolIndex[ev.ContentIndex], ev.ToolCall.ID, ev.ToolCall.Name)
			}
		case ai.EventDone:
			final = ev.Message
		case ai.EventError:
			final = ev.Error
		}
	}
	if final == nil {
		final = stream.Result()
	}
	res := FromAssistantMessage(&model, final)
	if final == nil || final.StopReason == ai.StopError || final.StopReason == ai.StopAborted {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		msg := "request failed"
		if final != nil && final.ErrorMessage != "" {
			msg = final.ErrorMessage
		}
		if final != nil && final.ErrorCause != nil {
			return res, streamError{message: msg, cause: final.ErrorCause}
		}
		return res, errors.New(msg)
	}
	return res, nil
}

// streamError keeps the displayed message and the provider's retry metadata.
type streamError struct {
	message string
	cause   error
}

func (e streamError) Error() string { return e.message }
func (e streamError) Unwrap() error { return e.cause }

// ToContext converts an atto transcript to an ai context for model. The
// first system message becomes the system prompt.
func ToContext(model *ai.Model, msgs []Message, tools []Tool) ai.Context {
	var c ai.Context
	for _, t := range tools {
		c.Tools = append(c.Tools, ai.Tool{Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters})
	}
	toolNames := map[string]string{}
	haveSystem := false
	for _, m := range msgs {
		switch m.Role {
		case "system":
			if !haveSystem {
				c.SystemPrompt, haveSystem = m.Content, true
				continue
			}
			c.Messages = append(c.Messages, &ai.SystemMessage{Role: "system", Content: m.Content})
		case "user":
			c.Messages = append(c.Messages, toUserMessage(m))
		case "assistant":
			am := toAssistantMessage(model, m)
			for _, tc := range m.ToolCalls {
				toolNames[tc.ID] = tc.Function.Name
			}
			c.Messages = append(c.Messages, am)
		case "tool":
			c.Messages = append(c.Messages, &ai.ToolResultMessage{
				Role: "toolResult", ToolCallID: m.ToolCallID, ToolName: toolNames[m.ToolCallID],
				Content: append([]ai.Content{ai.NewText(m.Content)}, imageParts(m.Images)...),
			})
		}
	}
	return c
}

func toUserMessage(m Message) *ai.UserMessage {
	if len(m.Images) == 0 {
		return &ai.UserMessage{Role: "user", Text: m.Content}
	}
	parts := []ai.Content{}
	if m.Content != "" {
		parts = append(parts, ai.NewText(m.Content))
	}
	return &ai.UserMessage{Role: "user", Parts: append(parts, imageParts(m.Images)...)}
}

// imageParts are the content blocks of a message's images. Package ai
// sends a tool result's images as each API allows: in the result itself
// (Responses), or in a user message after the results (chat completions).
func imageParts(imgs []Image) []ai.Content {
	var parts []ai.Content
	for _, im := range imgs {
		if d := im.base64Data(); d != "" {
			parts = append(parts, ai.NewImage(d, im.MIME))
		} else {
			parts = append(parts, ai.NewText(ImageMissing))
		}
	}
	return parts
}

func toAssistantMessage(model *ai.Model, m Message) *ai.AssistantMessage {
	am := &ai.AssistantMessage{
		Role: "assistant", Content: []ai.Content{}, Provider: m.Provider, Api: m.API, Model: m.Model,
		StopReason: ai.StopStop,
	}
	if am.Provider == "" && am.Api == "" && am.Model == "" {
		// Written before atto recorded the model: treat it as the current
		// model's, which is what atto replayed it as.
		am.Provider, am.Api, am.Model = model.Provider, model.Api, model.ID
		if m.Reasoning != nil {
			am.Model = m.Reasoning.Model
		}
	}
	if m.Reasoning != nil {
		for i, it := range m.Reasoning.Items {
			b := ai.NewThinking("")
			if i == 0 {
				b.Thinking = m.ReasoningContent
			}
			b.ThinkingSignature = string(it)
			am.Content = append(am.Content, b)
		}
	} else if m.ReasoningContent != "" {
		b := ai.NewThinking(m.ReasoningContent)
		// Chat completions signatures name the reasoning field; they mean
		// nothing to the Responses APIs, which replay only their own items.
		if am.Api == ai.ApiOpenAICompletions || am.Api == "" {
			b.ThinkingSignature = m.ThinkingSignature
			if b.ThinkingSignature == "" {
				b.ThinkingSignature = "reasoning_content"
			}
		}
		am.Content = append(am.Content, b)
	}
	if m.Content != "" {
		t := ai.NewText(m.Content)
		t.TextSignature = m.TextSignature
		am.Content = append(am.Content, t)
	}
	for _, tc := range m.ToolCalls {
		call := ai.NewToolCall(tc.ID, tc.Function.Name, ai.ParseStreamingJSON(tc.Function.Arguments))
		call.RawArguments = tc.Function.Arguments
		am.Content = append(am.Content, call)
	}
	return am
}

// FromAssistantMessage converts a reply back to an atto message.
func FromAssistantMessage(model *ai.Model, am *ai.AssistantMessage) Result {
	res := Result{Message: Message{Role: "assistant"}}
	if am == nil {
		return res
	}
	msg := &res.Message
	msg.Provider, msg.API, msg.Model = am.Provider, am.Api, am.Model
	responses := am.Api == ai.ApiOpenAIResponses || am.Api == ai.ApiOpenAICodexResponses
	var text, reasoning []string
	var items []json.RawMessage
	n := 0
	for _, c := range am.Content {
		switch b := c.(type) {
		case *ai.TextContent:
			text = append(text, b.Text)
			if msg.TextSignature == "" {
				msg.TextSignature = b.TextSignature
			}
		case *ai.ThinkingContent:
			if b.Thinking != "" {
				reasoning = append(reasoning, b.Thinking)
			}
			switch {
			case responses && b.ThinkingSignature != "":
				items = append(items, json.RawMessage(b.ThinkingSignature))
			case !responses && b.ThinkingSignature != "reasoning_content" && msg.ThinkingSignature == "":
				msg.ThinkingSignature = b.ThinkingSignature
			}
		case *ai.ToolCall:
			id := b.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", n)
			}
			args := b.RawArguments
			if args == "" {
				raw, _ := json.Marshal(b.Arguments)
				args = string(raw)
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: b.Name, Arguments: args}})
			n++
		}
	}
	msg.Content = strings.Join(text, "")
	sep := ""
	if responses {
		sep = "\n\n"
	}
	msg.ReasoningContent = strings.Join(reasoning, sep)
	if len(items) > 0 {
		msg.Reasoning = &ReasoningState{Model: am.Model, Items: items}
	}
	u := am.Usage
	res.Usage = Usage{PromptTokens: u.Input + u.CacheRead + u.CacheWrite, CompletionTokens: u.Output, CachedTokens: u.CacheRead, CacheWriteTokens: u.CacheWrite, Cost: u.Cost.Total}
	switch am.StopReason {
	case ai.StopLength:
		res.FinishReason = "length"
	case ai.StopToolUse:
		res.FinishReason = "tool_calls"
	default:
		res.FinishReason = "stop"
	}
	return res
}
