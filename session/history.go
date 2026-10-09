package session

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
)

// Find locates a session file by ID in the active and archived directories.
// An exact ID wins; otherwise a prefix that matches exactly one session is
// accepted, so the short IDs shown in listings can be pasted as typed.
// If no ID matches, a case-insensitive exact session name is accepted.
func Find(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("session %q not found", id)
	}
	var exact string
	prefix := map[string]string{} // full ID -> path
	names := map[string]string{}
	var paths []string
	for _, root := range []string{config.SessionsDir(), config.ArchivedDir()} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !IsSessionFile(path) {
				return nil
			}
			// Files are <YYYYMMDD-HHMMSS>-<id>.jsonl.
			if root == config.ArchivedDir() && archiveShadowed(path) {
				return nil
			}
			base := strings.TrimSuffix(strings.TrimSuffix(d.Name(), ".zst"), ".jsonl")
			_, full, ok := strings.CutLast(base, "-")
			if !ok {
				return nil
			}
			switch {
			case full == id:
				exact = path
				return fs.SkipAll
			case strings.HasPrefix(full, id):
				if _, exists := prefix[full]; !exists {
					prefix[full] = path
				}
			}
			paths = append(paths, path)
			return nil
		})
		if exact != "" {
			return exact, nil
		}
	}
	if len(prefix) == 0 {
		for _, path := range paths {
			if saved, err := Summarize(path); err == nil && saved.Name != "" && strings.EqualFold(saved.Name, id) {
				names[saved.ID] = path
			}
		}
		prefix = names
	}
	switch len(prefix) {
	case 0:
		return "", fmt.Errorf("session %s not found", id)
	case 1:
		for _, p := range prefix {
			return p, nil
		}
	}
	ids := make([]string, 0, len(prefix))
	for full := range prefix {
		ids = append(ids, full)
	}
	sort.Strings(ids)
	return "", fmt.Errorf("session %q is ambiguous: matches %s", id, strings.Join(ids, ", "))
}

// RelTime formats t as a short age: "now", "5m ago", "3d ago".
func RelTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 10*time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// Item is one searchable unit of a session: a message or compaction
// notes. N is its 1-based position among the session's entries, stable as
// the file grows.
type Item struct {
	N     int
	Label string // e.g. "user", "assistant", "tool: Run tests", "compaction"
	Text  string
	// OffBranch marks entries that are not on the active branch: the user
	// went back to an earlier point (/tree) and continued from there.
	OffBranch bool
}

// Items flattens entries into searchable text, on every branch. Assistant
// tool calls are rendered as "$ command" lines so commands can be found too.
func Items(entries []Entry) []Item {
	var out []Item
	calls := map[string]string{}
	for i, e := range entries {
		out = append(out, historyItems(e, i+1, calls)...)
	}
	active := OnActivePath(entries)
	for i := range out {
		out[i].OffBranch = !active[out[i].N-1]
	}
	return out
}

// VisitItems searches all branches without retaining their message strings.
func VisitItems(path string, visit func(Item) error) error {
	_, active, err := scanActive(path, true)
	if err != nil {
		return err
	}
	on := make(map[string]bool, len(active))
	for _, n := range active {
		on[n.ID] = true
	}
	calls := map[string]string{}
	return Visit(path, func(n int, e Entry) error {
		for _, it := range historyItems(e, n, calls) {
			it.OffBranch = !on[e.ID]
			if err := visit(it); err != nil {
				return err
			}
		}
		return nil
	})
}

func historyItems(e Entry, n int, calls map[string]string) []Item {
	var out []Item
	switch e.Type {
	case TypeCompaction:
		out = append(out, Item{N: n, Label: "compaction notes", Text: e.Notes})
	case TypeBranchSummary:
		out = append(out, Item{N: n, Label: "branch summary", Text: e.Summary})
	case TypeBashExecution:
		if b := e.Bash; b != nil {
			out = append(out, Item{N: n, Label: "user shell", Text: "! " + b.Command + "\n" + b.Output})
		}
	case TypeMessage:
		m := e.Message
		if m == nil {
			return nil
		}
		switch m.Role {
		case "user":
			out = append(out, Item{N: n, Label: "user", Text: m.Content})
		case "assistant":
			var b strings.Builder
			if m.ReasoningContent != "" {
				b.WriteString("[thinking]\n" + strings.TrimSpace(m.ReasoningContent) + "\n")
			}
			if m.Content != "" {
				b.WriteString(strings.TrimSpace(m.Content) + "\n")
			}
			for _, tc := range m.ToolCalls {
				desc, cmd := toolCallText(tc.Function.Arguments)
				calls[tc.ID] = desc
				fmt.Fprintf(&b, "[%s] $ %s\n", desc, cmd)
			}
			out = append(out, Item{N: n, Label: "assistant", Text: strings.TrimSpace(b.String())})
		case "tool":
			label := "tool output"
			if d := calls[m.ToolCallID]; d != "" {
				label += ": " + d
			}
			out = append(out, Item{N: n, Label: label, Text: m.Content})
		}
	}
	return out
}

// toolCallText pulls description and command out of bash arguments without
// importing the agent package.
func toolCallText(args string) (desc, cmd string) {
	var a struct {
		Description string `json:"description"`
		Command     string `json:"command"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "tool call", args
	}
	return a.Description, a.Command
}

// LastAssistant is the text of the last assistant message on the active
// branch of session id; "" if there is none or it can't be read.
func LastAssistant(id string) string {
	path, err := Find(id)
	if err != nil {
		return ""
	}
	var last string
	if err := VisitActive(path, func(e Entry) error {
		if m := e.Message; e.Type == TypeMessage && m != nil && m.Role == "assistant" {
			if text := strings.TrimSpace(m.Content); text != "" {
				last = text
			}
		}
		return nil
	}); err != nil {
		return ""
	}
	return last
}
