package session

import (
	"encoding/json"
	"strings"
)

// SearchTree searches full entry text on disk, including command text for tool
// results. Its retained state is labels and per-call token matches, not output.
func SearchTree(path, query string) ([]string, error) {
	tokens := strings.Fields(strings.ToLower(query))
	labels := map[string]string{}
	if err := Visit(path, func(_ int, e Entry) error {
		if e.Type == TypeLabel {
			labels[e.TargetID] = e.Label
		}
		return nil
	}); err != nil {
		return nil, err
	}
	calls := map[string][]bool{}
	var ids []string
	err := Visit(path, func(_ int, e Entry) error {
		parts := []string{labels[e.ID], e.Type, e.Name, e.Model, e.Provider, e.Effort, e.Label, e.Ext, e.Title}
		switch e.Type {
		case TypeEffort:
			parts = append(parts, "thinking")
		case TypeName:
			parts = append(parts, "title")
		case TypeBlockDisplay, TypeExtText:
			parts = append(parts, "display")
		}
		var commandMatches []bool
		if m := e.Message; m != nil {
			parts = append(parts, m.Role, m.Content)
			for _, tc := range m.ToolCalls {
				var args struct {
					Command string `json:"command"`
				}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				command := strings.ToLower(args.Command)
				bits := make([]bool, len(tokens))
				for i, tok := range tokens {
					bits[i] = strings.Contains(command, tok)
				}
				calls[tc.ID] = bits
			}
			if m.Role == "tool" {
				commandMatches = calls[m.ToolCallID]
			}
		}
		if e.Type == TypeBranchSummary {
			parts = append(parts, e.Summary, "branch summary")
		}
		if e.Bash != nil {
			parts = append(parts, e.Bash.Command, e.Bash.Output, "bash")
		}
		text := strings.ToLower(strings.Join(parts, " "))
		for i, tok := range tokens {
			if !strings.Contains(text, tok) && !(i < len(commandMatches) && commandMatches[i]) {
				return nil
			}
		}
		ids = append(ids, e.ID)
		return nil
	})
	return ids, err
}
