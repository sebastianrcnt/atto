package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sebastianrcnt/atto/provider"
)

// ReadHeader reads only the first JSONL line, including in compressed archives.
func ReadHeader(path string) (Entry, error) {
	f, err := Open(path)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	line, err := readStreamLine(bufio.NewReaderSize(f, 64*1024))
	if err != nil && err != io.EOF {
		return Entry{}, err
	}
	var h Entry
	if err := json.Unmarshal(line, &h); err != nil {
		return Entry{}, fmt.Errorf("%s: invalid session header: %w", path, err)
	}
	if h.Type != TypeSession {
		return Entry{}, fmt.Errorf("%s: not an atto session", path)
	}
	return h, nil
}

// Visit reads every entry in file order, retaining only one decoded entry.
// Legacy IDs and malformed lines follow Load's conventions.
func Visit(path string, visit func(int, Entry) error) error {
	f, err := Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	line, err := readStreamLine(r)
	var header Entry
	if len(line) == 0 {
		return fmt.Errorf("%s: missing session header", path)
	}
	if e := json.Unmarshal(line, &header); e != nil {
		return fmt.Errorf("%s: invalid session header: %w", path, e)
	}
	if header.Type != TypeSession {
		return fmt.Errorf("%s: not an atto session", path)
	}
	if err != nil && err != io.EOF {
		return err
	}
	n, prev := 0, ""
	for {
		line, err := readStreamLine(r)
		if err != nil && err != io.EOF {
			return err
		}
		var e Entry
		if len(line) > 0 && json.Unmarshal(line, &e) == nil {
			n++
			if e.ID == "" {
				e.ID, e.Parent = fmt.Sprintf("#%d", n), prev
			}
			prev = e.ID
			if err := visit(n, e); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

// ReadEntry retrieves a full entry even when it is not in the loaded tail.
func ReadEntry(path, id string) (Entry, bool, error) {
	var out Entry
	found := false
	err := Visit(path, func(_ int, e Entry) error {
		if !found && e.ID == id {
			out, found = e, true
		}
		return nil
	})
	return out, found, err
}

func VisitPath(path, leaf string, visit func(Entry) error) error {
	if leaf == "" {
		return nil
	}
	_, nodes, err := scanPath(path, true, leaf)
	if err != nil {
		return err
	}
	return visitSelected(path, nodes, visit)
}

func ReadContextAt(path, leaf string) (ActiveFile, error) {
	out, nodes, err := scanPath(path, false, leaf)
	if err != nil {
		return ActiveFile{}, err
	}
	out.Entries, err = readSelected(path, nodes)
	return out, err
}

func BranchPointFile(path, id string) (leaf, text string, ok bool, err error) {
	e, ok, err := ReadEntry(path, id)
	if !ok || err != nil {
		return "", "", ok, err
	}
	if e.Type == TypeMessage && e.Message != nil && e.Message.Role == "user" {
		return e.Parent, e.Message.Content, true, nil
	}
	return id, "", true, nil
}

// AbandonedFile decodes only the branch being left, not unrelated history.
func AbandonedFile(path, from, to string) ([]Entry, error) {
	var entries []Entry
	err := VisitAbandoned(path, from, to, func(e Entry) error { entries = append(entries, e); return nil })
	return entries, err
}

// VisitAbandoned streams the path being left, retaining only its small topology.
func VisitAbandoned(path, from, to string, visit func(Entry) error) error {
	_, left, err := scanPath(path, true, from)
	if err != nil {
		return err
	}
	var right []summaryNode
	if to != "" {
		_, right, err = scanPath(path, true, to)
		if err != nil {
			return err
		}
	}
	keep := make(map[string]bool, len(right))
	for _, n := range right {
		keep[n.ID] = true
	}
	var selected []summaryNode
	for _, n := range left {
		if !keep[n.ID] {
			selected = append(selected, n)
		}
	}
	return visitSelected(path, selected, visit)
}

// ForkFile copies the selected path one entry at a time, including extension
// amendments and labels, without holding the source transcript in memory.
func ForkFile(src, cwd, leaf string) (*Writer, error) {
	labels := map[string]Entry{}
	if err := Visit(src, func(_ int, e Entry) error {
		if e.Type == TypeLabel {
			labels[e.TargetID] = e
		}
		return nil
	}); err != nil {
		return nil, err
	}
	w := New(cwd)
	w.parent = src
	newIDs := map[string]string{}
	var copied [][2]string
	err := VisitPath(src, leaf, func(e Entry) error {
		if e.Type == TypeBranch || e.Type == TypeLabel {
			return nil
		}
		old := e.ID
		if e.Type == TypeBlockDisplay || e.Type == TypeUIItemDisplay || e.Type == TypeUIBlockUpdate {
			e.TargetID = newIDs[e.TargetID]
			if e.TargetID == "" {
				return nil
			}
		}
		w.Append(e)
		newIDs[old] = w.Leaf()
		copied = append(copied, [2]string{old, w.Leaf()})
		return w.Err()
	})
	if err != nil {
		w.Close()
		return nil, err
	}
	for _, pair := range copied {
		old, id := pair[0], pair[1]
		if label, ok := labels[old]; ok && label.Label != "" {
			w.Append(Entry{Type: TypeLabel, TargetID: id, Label: label.Label, Time: label.Time})
		}
	}
	return w, w.Err()
}

// ReadTree returns the complete topology with bounded display previews. Full
// text is retrieved with ReadEntry; searching is performed by visiting disk.
func ReadTree(path string) ([]Entry, error) {
	var entries []Entry
	err := Visit(path, func(_ int, e Entry) error {
		e.Replacement = nil
		e.Usage = nil
		e.Goal = nil
		e.Notes = boundedTreeText(e.Notes, 4096)
		e.Summary = boundedTreeText(e.Summary, 4096)
		e.Display = treePreview(e.Display)
		if e.Message != nil {
			m := *e.Message
			m.Content = treePreview(m.Content)
			m.ReasoningContent = ""
			m.Images = nil
			calls := make([]provider.ToolCall, len(m.ToolCalls))
			copy(calls, m.ToolCalls)
			for i := range calls {
				var args struct {
					Command     string `json:"command"`
					Description string `json:"description"`
				}
				if json.Unmarshal([]byte(calls[i].Function.Arguments), &args) == nil {
					args.Command = treePreview(args.Command)
					args.Description = treePreview(args.Description)
					raw, _ := json.Marshal(args)
					calls[i].Function.Arguments = string(raw)
				} else {
					calls[i].Function.Arguments = "{}"
				}
			}
			m.ToolCalls = calls
			e.Message = &m
		}
		if e.Bash != nil {
			b := *e.Bash
			b.Output = treePreview(b.Output)
			e.Bash = &b
		}
		entries = append(entries, e)
		return nil
	})
	return entries, err
}

func treePreview(text string) string {
	if len([]rune(text)) <= 200 {
		return text
	}
	var b strings.Builder
	space, count := false, 0
	for _, r := range text {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			space = b.Len() > 0
			continue
		}
		if space {
			if count == 200 {
				break
			}
			b.WriteByte(' ')
			count++
			space = false
		}
		if count == 200 {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

func boundedTreeText(text string, n int) string {
	count := 0
	for pos := range text {
		if count == n {
			return strings.Clone(text[:pos])
		}
		count++
	}
	return text
}
