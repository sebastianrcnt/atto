package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"encoding/json"
	"maps"
)

func isOpenAIReasoningDetail(d map[string]any) bool {
	if id, ok := d["id"]; ok && id != nil {
		if _, s := id.(string); !s {
			return false
		}
	}
	if f, ok := d["format"]; ok {
		if _, s := f.(string); !s {
			return false
		}
	}
	if i, ok := d["index"]; ok {
		if _, n := i.(float64); !n {
			return false
		}
	}
	switch d["type"] {
	case "reasoning.summary":
		_, ok := d["summary"].(string)
		return ok
	case "reasoning.encrypted":
		_, ok := d["data"].(string)
		return ok
	case "reasoning.text":
		if _, ok := d["text"].(string); !ok {
			return false
		}
		if s, ok := d["signature"]; ok && s != nil {
			_, str := s.(string)
			return str
		}
		return true
	}
	return false
}

func parseOpenAIReasoningDetails(signature string) []map[string]any {
	if signature == "" {
		return nil
	}
	var parsed []map[string]any
	if json.Unmarshal([]byte(signature), &parsed) != nil || len(parsed) == 0 {
		return nil
	}
	for _, d := range parsed {
		if d == nil || !isOpenAIReasoningDetail(d) {
			return nil
		}
	}
	return parsed
}

func parseLegacyEncryptedReasoningDetail(signature string) map[string]any {
	if signature == "" {
		return nil
	}
	var d map[string]any
	if json.Unmarshal([]byte(signature), &d) != nil || d == nil || !isOpenAIReasoningDetail(d) || d["type"] != "reasoning.encrypted" {
		return nil
	}
	if id, _ := d["id"].(string); id == "" {
		return nil
	}
	if data, _ := d["data"].(string); data == "" {
		return nil
	}
	return d
}

func fillMissingCommonReasoningDetailFields(target, source map[string]any) {
	if v, ok := target["id"]; !ok || v == nil {
		if s, ok := source["id"]; ok {
			target["id"] = s
		}
	}
	if f, _ := target["format"].(string); f == "" {
		if s, ok := source["format"]; ok {
			target["format"] = s
		}
	}
	if _, ok := target["index"]; !ok {
		if s, ok := source["index"]; ok {
			target["index"] = s
		}
	}
}

func appendOpenAIReasoningDetail(details []map[string]any, d map[string]any) []map[string]any {
	if n := len(details); n > 0 {
		last := details[n-1]
		if d["type"] == "reasoning.text" && last["type"] == "reasoning.text" {
			last["text"] = last["text"].(string) + d["text"].(string)
			if s, _ := last["signature"].(string); s == "" {
				if v, ok := d["signature"]; ok {
					last["signature"] = v
				}
			}
			fillMissingCommonReasoningDetailFields(last, d)
			return details
		}
		if d["type"] == "reasoning.summary" && last["type"] == "reasoning.summary" {
			last["summary"] = last["summary"].(string) + d["summary"].(string)
			fillMissingCommonReasoningDetailFields(last, d)
			return details
		}
	}
	c := make(map[string]any, len(d))
	maps.Copy(c, d)
	return append(details, c)
}

var openAICompletionsReasoningFields = []string{"reasoning", "reasoning_content", "reasoning_text"}
