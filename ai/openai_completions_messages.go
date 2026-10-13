package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

func hasToolHistory(messages []Message) bool {
	for _, msg := range messages {
		switch m := msg.(type) {
		case *ToolResultMessage:
			return true
		case *AssistantMessage:
			for _, b := range m.Content {
				if _, ok := b.(*ToolCall); ok {
					return true
				}
			}
		}
	}
	return false
}

var nonIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// ConvertCompletionsMessages converts the transcript to chat messages.
func ConvertCompletionsMessages(model *Model, context TranscriptContext, compat ResolvedCompletionsCompat) []object {
	normalized := ResolveTranscript(context, compat.SupportsMidConvoSystemMessages)
	params := []object{}

	normalizeToolCallID := func(id string, _ *Model, _ *AssistantMessage) string {
		// Responses API ids look like "{call_id}|{item_id}", up to 450+
		// characters; chat completions allows 40.
		if before, after, ok := strings.Cut(id, "|"); ok {
			callID := nonIDChars.ReplaceAllString(before, "_")
			itemID := nonIDChars.ReplaceAllString(after, "_")
			combined := callID
			if itemID != "" {
				combined = callID + "_" + itemID
			}
			if len(combined) <= 40 {
				return combined
			}
			hash := ShortHash(id)
			if len(hash) > 8 {
				hash = hash[:8]
			}
			prefix := callID[:min(len(callID), max(1, 40-len(hash)-1))]
			return prefix + "_" + hash
		}
		if model.Provider == "openai" && len(id) > 40 {
			return id[:40]
		}
		return id
	}
	transformed := TransformMessages(normalized.Messages, model, normalizeToolCallID)
	instructionRole := "system"
	if model.Reasoning && compat.SupportsDeveloperRole {
		instructionRole = "developer"
	}

	lastRole := ""
	for i := 0; i < len(transformed); i++ {
		msg := transformed[i]
		if compat.RequiresAssistantAfterToolResult && lastRole == "toolResult" && msg.MessageRole() == "user" {
			params = append(params, obj("role", "assistant", "content", "I have processed the tool results."))
		}
		switch m := msg.(type) {
		case *SystemMessage:
			text := RenderSystemMessageUpdate(m)
			if i == 0 {
				text = GetSystemMessageText(m)
			}
			if text != "" {
				params = append(params, obj("role", instructionRole, "content", text))
			}
		case *UserMessage:
			if m.IsText() {
				params = append(params, obj("role", "user", "content", m.Text))
				break
			}
			content := []object{}
			for _, item := range m.Parts {
				switch b := item.(type) {
				case *TextContent:
					if b.Text != "" {
						content = append(content, obj("type", "text", "text", b.Text))
					}
				case *ImageContent:
					content = append(content, obj("type", "image_url", "image_url", obj("url", "data:"+b.MimeType+";base64,"+b.Data)))
				}
			}
			if len(content) == 0 {
				continue
			}
			params = append(params, obj("role", "user", "content", content))
		case *AssistantMessage:
			var content any // nil marshals as null
			if compat.RequiresAssistantAfterToolResult {
				content = ""
			}
			assistant := obj("role", "assistant", "content", content)

			var textParts []object
			var assistantText strings.Builder
			var thinkingBlocks []*ThinkingContent
			var toolCalls []*ToolCall
			for _, b := range m.Content {
				switch blk := b.(type) {
				case *TextContent:
					if strings.TrimSpace(blk.Text) != "" {
						textParts = append(textParts, obj("type", "text", "text", blk.Text))
						assistantText.WriteString(blk.Text)
					}
				case *ThinkingContent:
					thinkingBlocks = append(thinkingBlocks, blk)
				case *ToolCall:
					toolCalls = append(toolCalls, blk)
				}
			}
			var preserved []map[string]any
			for _, t := range thinkingBlocks {
				if d := parseOpenAIReasoningDetails(t.ThinkingSignature); d != nil {
					preserved = d
					break
				}
			}
			if preserved == nil {
				for _, tc := range toolCalls {
					if d := parseLegacyEncryptedReasoningDetail(tc.ThoughtSignature); d != nil {
						preserved = append(preserved, d)
					}
				}
			}
			var nonEmpty []*ThinkingContent
			for _, t := range thinkingBlocks {
				if strings.TrimSpace(t.Thinking) != "" {
					nonEmpty = append(nonEmpty, t)
				}
			}
			if len(nonEmpty) > 0 {
				if compat.RequiresThinkingAsText {
					var ts []string
					for _, t := range nonEmpty {
						ts = append(ts, t.Thinking)
					}
					assistant.set("content", append([]object{obj("type", "text", "text", strings.Join(ts, "\n\n"))}, textParts...))
				} else {
					// Assistant content is always a plain string: some models
					// mirror a block array literally in their output.
					if assistantText.Len() > 0 {
						assistant.set("content", assistantText.String())
					}
					if preserved == nil {
						signature := nonEmpty[0].ThinkingSignature
						if model.Provider == "opencode-go" && signature == "reasoning" {
							signature = "reasoning_content"
						}
						if slices.Contains(openAICompletionsReasoningFields, signature) {
							var ts []string
							for _, t := range nonEmpty {
								ts = append(ts, t.Thinking)
							}
							assistant.set(signature, strings.Join(ts, "\n"))
						}
					}
				}
			} else if assistantText.Len() > 0 {
				assistant.set("content", assistantText.String())
			}
			if len(toolCalls) > 0 {
				calls := make([]object, 0, len(toolCalls))
				for _, tc := range toolCalls {
					calls = append(calls, obj("id", tc.ID, "type", "function", "function", obj("name", tc.Name, "arguments", toolCallArguments(tc))))
				}
				assistant.set("tool_calls", calls)
			}
			if preserved != nil {
				assistant.set("reasoning_details", preserved)
			}
			if compat.RequiresReasoningContentOnAssistantMessages && model.Reasoning {
				if _, ok := assistant.get("reasoning_content"); !ok {
					assistant.set("reasoning_content", "")
				}
			}
			// Skip assistant messages with neither content nor tool calls:
			// providers reject them (e.g. aborted turns without output).
			c, _ := assistant.get("content")
			hasContent := false
			switch cv := c.(type) {
			case string:
				hasContent = cv != ""
			case []object:
				hasContent = len(cv) > 0
			}
			if _, hasCalls := assistant.get("tool_calls"); !hasContent && !hasCalls {
				continue
			}
			params = append(params, assistant)
		case *ToolResultMessage:
			var imageBlocks []object
			j := i
			for ; j < len(transformed); j++ {
				tm, ok := transformed[j].(*ToolResultMessage)
				if !ok {
					break
				}
				text := ContentText(tm.Content, "\n")
				hasImages := false
				for _, c := range tm.Content {
					if _, ok := c.(*ImageContent); ok {
						hasImages = true
					}
				}
				switch {
				case text != "":
				case hasImages:
					text = "(see attached image)"
				default:
					text = "(no tool output)"
				}
				result := obj("role", "tool", "content", text, "tool_call_id", tm.ToolCallID)
				if compat.RequiresToolResultName && tm.ToolName != "" {
					result.set("name", tm.ToolName)
				}
				params = append(params, result)
				if hasImages && model.SupportsImages() {
					for _, c := range tm.Content {
						if im, ok := c.(*ImageContent); ok {
							imageBlocks = append(imageBlocks, obj("type", "image_url", "image_url", obj("url", "data:"+im.MimeType+";base64,"+im.Data)))
						}
					}
				}
			}
			i = j - 1
			if len(imageBlocks) > 0 {
				if compat.RequiresAssistantAfterToolResult {
					params = append(params, obj("role", "assistant", "content", "I have processed the tool results."))
				}
				parts := append([]object{obj("type", "text", "text", "Attached image(s) from tool result:")}, imageBlocks...)
				params = append(params, obj("role", "user", "content", parts))
				lastRole = "user"
			} else {
				lastRole = "toolResult"
			}
			continue
		}
		lastRole = msg.MessageRole()
	}
	return params
}

// toolCallArguments are the arguments to replay: the model's own bytes
// when known (atto), else pi's JSON.stringify of the parsed object. Bytes
// that aren't JSON (a model's broken call, kept as streamed) are replayed
// as the parsed object instead: servers that parse the history reject
// them, and every later request of the session would fail.
func toolCallArguments(tc *ToolCall) string {
	if tc.RawArguments != "" && json.Valid([]byte(tc.RawArguments)) {
		return tc.RawArguments
	}
	b, _ := json.Marshal(tc.Arguments)
	if tc.Arguments == nil {
		return "{}"
	}
	return string(b)
}

func convertCompletionsTools(tools []Tool, compat ResolvedCompletionsCompat) []object {
	out := make([]object, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		fn := obj("name", t.Name, "description", t.Description, "parameters", params)
		if compat.SupportsStrictMode {
			fn.set("strict", false)
		}
		out = append(out, obj("type", "function", "function", fn))
	}
	return out
}
