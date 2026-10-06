package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"strings"
	"unicode"
)

// Port of src/api/transform-messages.ts.

const (
	nonVisionUserImagePlaceholder = "(image omitted: model does not support images)"
	nonVisionToolImagePlaceholder = "(tool image omitted: model does not support images)"
)

func replaceImagesWithPlaceholder(content []Content, placeholder string) []Content {
	var out []Content
	previousWasPlaceholder := false
	for _, block := range content {
		if _, ok := block.(*ImageContent); ok {
			if !previousWasPlaceholder {
				out = append(out, NewText(placeholder))
			}
			previousWasPlaceholder = true
			continue
		}
		out = append(out, block)
		t, ok := block.(*TextContent)
		previousWasPlaceholder = ok && t.Text == placeholder
	}
	return out
}

func downgradeUnsupportedImages(messages []Message, model *Model) []Message {
	if model.SupportsImages() {
		return messages
	}
	out := make([]Message, len(messages))
	for i, msg := range messages {
		switch m := msg.(type) {
		case *UserMessage:
			if !m.IsText() {
				c := *m
				c.Parts = replaceImagesWithPlaceholder(m.Parts, nonVisionUserImagePlaceholder)
				if c.Parts == nil {
					c.Parts = []Content{}
				}
				msg = &c
			}
		case *ToolResultMessage:
			c := *m
			c.Content = replaceImagesWithPlaceholder(m.Content, nonVisionToolImagePlaceholder)
			msg = &c
		}
		out[i] = msg
	}
	return out
}

// TransformMessages prepares a transcript for model: it downgrades images
// for non-vision models, converts or drops thinking from other models,
// normalizes tool call IDs of foreign messages, skips errored assistant
// turns and adds "No result provided" results for orphaned tool calls.
func TransformMessages(messages []Message, model *Model, normalizeToolCallID func(id string, model *Model, source *AssistantMessage) string) []Message {
	toolCallIDMap := map[string]string{}
	imageAware := downgradeUnsupportedImages(messages, model)

	transformed := make([]Message, 0, len(imageAware))
	for _, msg := range imageAware {
		switch m := msg.(type) {
		case *ToolResultMessage:
			if id, ok := toolCallIDMap[m.ToolCallID]; ok && id != m.ToolCallID {
				c := *m
				c.ToolCallID = id
				msg = &c
			}
		case *AssistantMessage:
			sameModel := m.Provider == model.Provider && m.Api == model.Api && m.Model == model.ID
			var content []Content
			for _, block := range m.Content {
				switch b := block.(type) {
				case *ThinkingContent:
					if b.Redacted {
						if sameModel {
							content = append(content, b)
						}
						continue
					}
					// Same model: keep signed thinking (needed for replay) even
					// when its text is empty (OpenAI encrypted reasoning).
					if sameModel && b.ThinkingSignature != "" {
						content = append(content, b)
						continue
					}
					if isBlank(b.Thinking) {
						continue
					}
					if sameModel {
						content = append(content, b)
					} else {
						content = append(content, NewText(b.Thinking))
					}
				case *TextContent:
					if sameModel {
						content = append(content, b)
					} else {
						content = append(content, NewText(b.Text))
					}
				case *ToolCall:
					tc := b
					if !sameModel && tc.ThoughtSignature != "" {
						c := *tc
						c.ThoughtSignature = ""
						tc = &c
					}
					if !sameModel && normalizeToolCallID != nil {
						if id := normalizeToolCallID(tc.ID, model, m); id != tc.ID {
							toolCallIDMap[tc.ID] = id
							c := *tc
							c.ID = id
							tc = &c
						}
					}
					content = append(content, tc)
				default:
					content = append(content, block)
				}
			}
			c := *m
			c.Content = content
			msg = &c
		}
		transformed = append(transformed, msg)
	}

	// Second pass: synthetic results for orphaned tool calls. System
	// messages between a call and its results are held back until after
	// them.
	var result []Message
	var pending []*ToolCall
	existing := map[string]bool{}
	var held []Message
	closePending := func() {
		for _, tc := range pending {
			if !existing[tc.ID] {
				result = append(result, &ToolResultMessage{
					Role: "toolResult", ToolCallID: tc.ID, ToolName: tc.Name,
					Content: []Content{NewText("No result provided")}, IsError: true, Timestamp: nowMillis(),
				})
			}
		}
		pending, existing = nil, map[string]bool{}
		result = append(result, held...)
		held = nil
	}
	for _, msg := range transformed {
		switch m := msg.(type) {
		case *AssistantMessage:
			closePending()
			// Errored or aborted turns are not replayed: they may hold
			// reasoning without a following item, or half-formed calls.
			if m.StopReason == StopError || m.StopReason == StopAborted {
				continue
			}
			var calls []*ToolCall
			for _, b := range m.Content {
				if tc, ok := b.(*ToolCall); ok {
					calls = append(calls, tc)
				}
			}
			if len(calls) > 0 {
				pending, existing = calls, map[string]bool{}
			}
			result = append(result, msg)
		case *ToolResultMessage:
			existing[m.ToolCallID] = true
			result = append(result, msg)
		case *SystemMessage:
			if len(pending) > 0 {
				held = append(held, msg)
			} else {
				result = append(result, msg)
			}
		case *UserMessage:
			closePending()
			result = append(result, msg)
		default:
			result = append(result, msg)
		}
	}
	closePending()
	return mergeUserMessages(result)
}

// mergeUserMessages joins adjacent user messages into one, as many servers
// reject or mishandle consecutive user turns (e.g. after a failed turn the
// user sent again, or a skipped errored reply). Text is joined with a blank
// line and images stay in order. It only shapes the request: the stored
// transcript is unchanged, and the result depends only on the messages, so
// the request prefix stays the same from one request to the next.
func mergeUserMessages(messages []Message) []Message {
	var out []Message
	for _, msg := range messages {
		cur, ok := msg.(*UserMessage)
		if !ok || len(out) == 0 {
			out = append(out, msg)
			continue
		}
		prev, ok := out[len(out)-1].(*UserMessage)
		if !ok {
			out = append(out, msg)
			continue
		}
		out[len(out)-1] = joinUserMessages(prev, cur)
	}
	return out
}

func joinUserMessages(a, b *UserMessage) *UserMessage {
	merged := &UserMessage{Role: "user", Timestamp: a.Timestamp}
	if a.IsText() && b.IsText() {
		merged.Text = joinText(a.Text, b.Text)
		return merged
	}
	parts := userParts(a)
	for _, p := range userParts(b) {
		last, lastText := (*TextContent)(nil), false
		if n := len(parts); n > 0 {
			last, lastText = parts[n-1].(*TextContent)
		}
		if t, ok := p.(*TextContent); ok && lastText {
			parts[len(parts)-1] = NewText(joinText(last.Text, t.Text))
			continue
		}
		parts = append(parts, p)
	}
	merged.Parts = parts
	return merged
}

// userParts is m's content as blocks, never nil (nil means plain text).
func userParts(m *UserMessage) []Content {
	if m.IsText() {
		if m.Text == "" {
			return []Content{}
		}
		return []Content{NewText(m.Text)}
	}
	return append([]Content{}, m.Parts...)
}

func joinText(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + "\n\n" + b
}

func isBlank(s string) bool {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0xfeff }) == ""
}
