package session

import (
	"encoding/json"
	"time"

	"github.com/sebastianrcnt/atto/provider"
)

// TreeRow is the topology and preview fields used by tree pickers. Keeping this
// compact avoids allocating a full Entry (with model request/replacement fields)
// for every historical row just to encode a tree response.
type TreeRow struct {
	Type         string            `json:"type"`
	Time         time.Time         `json:"time"`
	ID           string            `json:"id"`
	Parent       string            `json:"parentId,omitempty"`
	Message      *provider.Message `json:"message,omitempty"`
	Tool         *ToolMeta         `json:"tool,omitempty"`
	Notes        string            `json:"notes,omitempty"`
	TokensBefore int               `json:"tokensBefore,omitempty"`
	Summary      string            `json:"summary,omitempty"`
	Bash         *BashExec         `json:"bash,omitempty"`
	Provider     string            `json:"provider,omitempty"`
	Model        string            `json:"model,omitempty"`
	Effort       string            `json:"effort,omitempty"`
	Name         string            `json:"name,omitempty"`
	LongContext  bool              `json:"longContext,omitempty"`
	Label        string            `json:"label,omitempty"`
	TargetID     string            `json:"targetId,omitempty"`
	FromID       string            `json:"fromId,omitempty"`
	Ext          string            `json:"ext,omitempty"`
	Title        string            `json:"title,omitempty"`
}

func ReadTreeRows(path string) ([]TreeRow, error) {
	var rows []TreeRow
	err := visitRaw(path, func(_ int, id, parent string, line []byte) error {
		var wire struct {
			TreeRow
			Message *struct {
				Role       string              `json:"role"`
				Content    string              `json:"content"`
				ToolCallID string              `json:"tool_call_id"`
				ToolCalls  []provider.ToolCall `json:"tool_calls"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &wire); err != nil {
			return nil
		}
		row := wire.TreeRow
		row.ID, row.Parent = id, parent
		row.Notes = boundedTreeText(row.Notes, 4096)
		row.Summary = boundedTreeText(row.Summary, 4096)

		if m := wire.Message; m != nil {
			msg := provider.Message{Role: m.Role, Content: treePreview(m.Content), ToolCallID: m.ToolCallID}
			for _, tc := range m.ToolCalls {
				var args struct {
					Command     string `json:"command"`
					Description string `json:"description"`
				}
				if json.Unmarshal([]byte(tc.Function.Arguments), &args) == nil {
					args.Command = treePreview(args.Command)
					args.Description = treePreview(args.Description)
					raw, _ := json.Marshal(args)
					tc.Function.Arguments = string(raw)
				} else {
					tc.Function.Arguments = "{}"
				}
				msg.ToolCalls = append(msg.ToolCalls, tc)
			}
			row.Message = &msg
		}
		if b := row.Bash; b != nil {
			x := *b
			x.Command = treePreview(x.Command)
			x.Output = treePreview(x.Output)
			row.Bash = &x
		}
		rows = append(rows, row)
		return nil
	})
	return rows, err
}
