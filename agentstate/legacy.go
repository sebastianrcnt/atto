package agentstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The old layout (format 1) kept an agent under its parent:
//
//	agent-state/<parent>/<name>.json  (+ .turn.json, .turn.json.interrupt, <name>.lock,
//	                                   slots/, .tree.lock, .closed)
//	agent-state/_up/<session>.json    {parent, name}: the reverse index
//	agent-state/_closed/<session>.json {session, root, path}: a removed agent
//
// and, before agent-state existed, the same under subagents/ (later a
// symlink to agent-state). This file only reads it, for the migration.

// LegacyAgent is an agent found in the old layout.
type LegacyAgent struct {
	State State // as written then: Name, Parent and Session are set, the tree fields are not
	Turn  *Turn
	// Files are the agent's files: its record, turn record, interrupt request.
	Files []string
}

// Latest is the agent's latest turn as the old layout recorded it.
func (a LegacyAgent) Latest() Turn {
	if a.Turn != nil {
		return a.State.latest(*a.Turn, true)
	}
	return a.State.latest(Turn{}, false)
}

// LegacyTomb is a removed agent's tombstone.
type LegacyTomb struct {
	Session string `json:"session"`
	Root    string `json:"root"`
	Path    string `json:"path"`
}

// Legacy is the old layout's content.
type Legacy struct {
	Agents []LegacyAgent
	Tombs  []LegacyTomb
	// Remove is everything that belongs to the old layout and goes once it
	// is converted: parent directories, _up, _closed and the subagents tree.
	Remove []string
	// Problems are records that could not be read; a migration stops on them.
	Problems []string
}

// ScanLegacy reads the old layout, in agent-state/ and subagents/. A record
// in both is read from agent-state, as the old code did.
func ScanLegacy() (*Legacy, error) {
	l := &Legacy{}
	seen := map[string]bool{}
	scanned := map[string]bool{}
	for _, root := range []string{dir(), legacyDir()} {
		if _, err := os.Lstat(root); err != nil {
			continue
		}
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			l.Problems = append(l.Problems, fmt.Sprintf("%s: %v", root, err))
			continue
		}
		if scanned[real] {
			if root == legacyDir() { // an alias of agent-state
				l.Remove = append(l.Remove, root)
			}
			continue
		}
		scanned[real] = true
		ents, err := os.ReadDir(real)
		if err != nil {
			l.Problems = append(l.Problems, fmt.Sprintf("%s: %v", root, err))
			continue
		}
		for _, e := range ents {
			name := e.Name()
			path := filepath.Join(real, name)
			switch {
			case !e.IsDir() || name == ".coord":
				continue
			case name == "_up":
				l.Remove = append(l.Remove, path)
			case name == "_closed":
				l.Remove = append(l.Remove, path)
				l.readTombs(path, seen)
			case strings.HasPrefix(name, "."):
				continue
			default:
				l.Remove = append(l.Remove, path)
				l.readParent(name, path, seen)
			}
		}
		if root == legacyDir() {
			l.Remove = append(l.Remove, root)
		}
	}
	sort.Slice(l.Agents, func(i, j int) bool {
		a, b := l.Agents[i].State, l.Agents[j].State
		if a.Created.Equal(b.Created) {
			return a.Session < b.Session
		}
		return a.Created.Before(b.Created)
	})
	sort.Slice(l.Tombs, func(i, j int) bool { return l.Tombs[i].Session < l.Tombs[j].Session })
	return l, nil
}

func (l *Legacy) readParent(parent, path string, seen map[string]bool) {
	ents, err := os.ReadDir(path)
	if err != nil {
		l.Problems = append(l.Problems, fmt.Sprintf("%s: %v", path, err))
		return
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".turn.json") {
			continue
		}
		file := filepath.Join(path, name)
		data, err := os.ReadFile(file)
		if err != nil {
			l.Problems = append(l.Problems, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		var s State
		if err := json.Unmarshal(data, &s); err != nil {
			l.Problems = append(l.Problems, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		base := strings.TrimSuffix(name, ".json")
		if s.Name == "" {
			s.Name = base
		}
		if s.Parent == "" {
			s.Parent = parent
		}
		if s.Session == "" {
			l.Problems = append(l.Problems, fmt.Sprintf("%s: the record has no session", file))
			continue
		}
		if seen[s.Session] {
			continue
		}
		seen[s.Session] = true
		a := LegacyAgent{State: s, Files: []string{file}}
		turnFile := filepath.Join(path, base+".turn.json")
		if data, err := os.ReadFile(turnFile); err == nil {
			var t Turn
			if json.Unmarshal(data, &t) == nil {
				a.Turn = &t
			}
			a.Files = append(a.Files, turnFile)
		}
		if _, err := os.Stat(turnFile + ".interrupt"); err == nil {
			a.Files = append(a.Files, turnFile+".interrupt")
		}
		l.Agents = append(l.Agents, a)
	}
}

func (l *Legacy) readTombs(path string, seen map[string]bool) {
	ents, _ := os.ReadDir(path)
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, e.Name()))
		var t LegacyTomb
		if err != nil || json.Unmarshal(data, &t) != nil || t.Session == "" {
			continue // a tombstone only reserves an ID
		}
		l.Tombs = append(l.Tombs, t)
	}
}
