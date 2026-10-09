package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/sebastianrcnt/atto/provider"
)

// ActiveFile keeps the active conversation and session-wide snapshots,
// without decoding the messages on abandoned branches.
type ActiveFile struct {
	Header     Entry
	Entries    []Entry
	State      []Entry // latest choices and goal, without message text
	Usage      provider.Usage
	LastUsage  provider.Usage
	UsageModel string
	LastRole   string // active branch, including messages before compaction
}

type activeLine struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Parent      string          `json:"parentId"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	Effort      string          `json:"effort"`
	Name        string          `json:"name"`
	LongContext bool            `json:"longContext"`
	Usage       *provider.Usage `json:"usage"`
	Goal        json.RawMessage `json:"goal"`
	Message     *struct {
		Role string `json:"role"`
	} `json:"message"`
}

// ReadActive scans the tree using line offsets, then decodes only its active
// path. Like Load it skips malformed entries and links legacy ID-less ones.
func ReadActive(path string) (ActiveFile, error) { return readActive(path, true) }

// ReadContext is ReadActive without the entries before the last active
// compaction, which only transcript display needs.
func ReadContext(path string) (ActiveFile, error) { return readActive(path, false) }

// VisitActive replays the active branch one entry at a time. It retains only
// the lightweight tree index, not the message strings preceding compaction.
// Returning an error from visit stops the scan immediately.
func VisitActive(path string, visit func(Entry) error) error {
	_, nodes, err := scanActive(path, true)
	if err != nil {
		return err
	}
	return visitSelected(path, nodes, visit)
}

func readActive(path string, display bool) (ActiveFile, error) {
	out, nodes, err := scanActive(path, display)
	if err != nil {
		return ActiveFile{}, err
	}
	out.Entries, err = readSelected(path, nodes)
	return out, err
}

func scanActive(path string, display bool) (ActiveFile, []summaryNode, error) {
	return scanPath(path, display, "")
}

func scanPath(path string, display bool, leaf string) (ActiveFile, []summaryNode, error) {
	f, err := Open(path)
	if err != nil {
		return ActiveFile{}, nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	line, err := readStreamLine(r)
	var h Entry
	if len(line) == 0 {
		return ActiveFile{}, nil, fmt.Errorf("%s: missing session header", path)
	}
	if parseErr := json.Unmarshal(line, &h); parseErr != nil {
		return ActiveFile{}, nil, fmt.Errorf("%s: invalid session header: %w", path, parseErr)
	}
	if h.Type != TypeSession {
		return ActiveFile{}, nil, fmt.Errorf("%s: not an atto session", path)
	}
	off := int64(len(line))
	if err != nil && err != io.EOF {
		return ActiveFile{}, nil, err
	}
	out := ActiveFile{Header: h}
	stateIndexes := map[string]int{}
	model := ""
	var nodes []summaryNode
	var compactions []bool
	byID := map[string]int{}
	for {
		line, err := readStreamLine(r)
		if err != nil && err != io.EOF {
			return ActiveFile{}, nil, err
		}
		if len(line) != 0 {
			var l activeLine
			if json.Unmarshal(line, &l) == nil {
				n := summaryNode{ID: l.ID, Parent: l.Parent, Off: off, Len: int64(len(line))}
				if l.Type == TypeMessage && l.Message != nil {
					switch l.Message.Role {
					case "user":
						n.Role = 'u'
					case "assistant":
						n.Role = 'a'
					case "tool":
						n.Role = 't'
					default:
						n.Role = '?'
					}
				}
				if n.ID == "" {
					n.ID = "#" + strconv.Itoa(len(nodes)+1)
					if len(nodes) != 0 {
						n.Parent = nodes[len(nodes)-1].ID
					}
				}
				if _, ok := byID[n.ID]; !ok {
					byID[n.ID] = len(nodes)
				}
				nodes = append(nodes, n)
				compactions = append(compactions, l.Type == TypeCompaction)
				if l.Type == TypeModel {
					model = l.Provider + "/" + l.Model
				}
				if l.Usage != nil {
					x := *l.Usage
					out.Usage.PromptTokens += x.PromptTokens
					out.Usage.CachedTokens += x.CachedTokens
					out.Usage.CompletionTokens += x.CompletionTokens
					out.Usage.CacheWriteTokens += x.CacheWriteTokens
					out.Usage.Cost += x.Cost
					out.LastUsage, out.UsageModel = x, model
				}
				if l.Type == TypeModel || l.Type == TypeEffort || l.Type == TypeContext || l.Type == TypeName || l.Type == TypeGoal {
					e := Entry{Type: l.Type, ID: n.ID, Parent: n.Parent, Provider: l.Provider, Model: l.Model, Effort: l.Effort, Name: l.Name, LongContext: l.LongContext, Goal: l.Goal}
					if i, ok := stateIndexes[l.Type]; ok {
						out.State[i] = e
					} else {
						stateIndexes[l.Type] = len(out.State)
						out.State = append(out.State, e)
					}
				}
			}
			off += int64(len(line))
		}
		if err == io.EOF {
			break
		}
	}
	if len(nodes) == 0 {
		return out, nil, nil
	}
	var reverse []int
	seen := map[string]bool{}
	if leaf == "" {
		leaf = nodes[len(nodes)-1].ID
	}
	for id := leaf; id != "" && !seen[id]; {
		i, ok := byID[id]
		if !ok {
			break
		}
		seen[id] = true
		reverse = append(reverse, i)
		id = nodes[i].Parent
	}
	for _, i := range reverse {
		if role := nodes[i].Role; role != 0 {
			out.LastRole = map[byte]string{'u': "user", 'a': "assistant", 't': "tool", '?': "other"}[role]
			break
		}
	}
	start := len(reverse) - 1
	if !display {
		for j, i := range reverse {
			if compactions[i] {
				start = j
				break
			}
		}
	}
	// Decode only selected entries. Plain files use offsets; compressed files
	// make a second streaming pass, never a decompressed temporary copy.
	selected := make([]summaryNode, 0, start+1)
	for j := start; j >= 0; j-- {
		selected = append(selected, nodes[reverse[j]])
	}
	return out, selected, nil
}

// Context is the part of an active branch the agent needs to restore: its
// last compaction and everything after it. Replay still uses the whole branch.
func Context(entries []Entry) []Entry {
	for i, e := range slices.Backward(entries) {
		if e.Type == TypeCompaction {
			return entries[i:]
		}
	}
	return entries
}
