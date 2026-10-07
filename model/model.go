// Package model talks to a model over the OpenAI chat completions API,
// which local servers (llama.cpp, vLLM) and most providers speak.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message is one message of the conversation.
type Message struct {
	Role       string     `json:"role"` // system, user, assistant, tool
	Content    string     `json:"content"`
	Reasoning  string     `json:"reasoning_content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a call the model made.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // function
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON
	} `json:"function"`
}

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON schema
}

// Usage is what one call cost.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// Client is a chat completions endpoint and a model on it.
type Client struct {
	BaseURL string // e.g. http://host:8081/v1
	Model   string
	APIKey  string // "" for local servers
	HTTP    *http.Client
}

// Complete sends the conversation and returns the model's reply.
func (c *Client) Complete(ctx context.Context, msgs []Message, tools []Tool) (Message, Usage, error) {
	type fn struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	type tool struct {
		Type     string `json:"type"`
		Function fn     `json:"function"`
	}
	req := map[string]any{"model": c.Model, "messages": msgs}
	if len(tools) > 0 {
		var ts []tool
		for _, t := range tools {
			ts = append(ts, tool{Type: "function", Function: fn{t.Name, t.Description, t.Parameters}})
		}
		req["tools"] = ts
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Message{}, Usage{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, Usage{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(hr)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Message{}, Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Message{}, Usage{}, fmt.Errorf("model: %s: %s", resp.Status, bytes.TrimSpace(raw))
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Message{}, Usage{}, fmt.Errorf("model: %w", err)
	}
	if len(out.Choices) == 0 {
		return Message{}, Usage{}, fmt.Errorf("model: no choices in %s", raw)
	}
	m := out.Choices[0].Message
	m.Role = "assistant"
	return m, out.Usage, nil
}
