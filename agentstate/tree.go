package agentstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sebastianrcnt/atto/fsutil"
)

// Agents form trees, as in codex: a session that starts agents is their
// parent, an agent may start agents of its own (up to the depth the user
// allows), and each has a path from the tree's root, "/root/tests/lint".
// The path is how agents address each other.

// RootPath is the path of a tree's root session.
const RootPath = "/root"

// upPath records which agent a session is: its parent and name, so the
// tree can be walked up from any session.
func upPath(session string) string {
	return existingPath("_up", session+".json")
}

type up struct {
	Parent string `json:"parent"`
	Name   string `json:"name"`
}

func saveUp(s State) error {
	if s.Session == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(upPath(s.Session)), 0o755); err != nil {
		return err
	}
	data, _ := json.Marshal(up{s.Parent, s.Name})
	return fsutil.WriteAtomic(upPath(s.Session), data, 0o644)
}

// ParentOf returns the session that started session as an agent, and its
// name there; ok is false for a root.
func ParentOf(session string) (parent, name string, ok bool) {
	data, err := os.ReadFile(upPath(session))
	var u up
	if err == nil && json.Unmarshal(data, &u) == nil && u.Parent != "" {
		return u.Parent, u.Name, true
	}
	// Older agents have state but no reverse index. Repair it from state.
	for _, parent := range parentIDs() {
		for _, s := range List(parent) {
			if s.Session == session {
				_ = saveUp(s)
				return s.Parent, s.Name, true
			}
		}
	}
	return "", "", false
}

// maxHops bounds walks up a tree, against a corrupt loop.
const maxHops = 64

// Depth is how many agents up session's root is: 0 for a root.
func Depth(session string) int {
	n := 0
	for s := session; n < maxHops; n++ {
		p, _, ok := ParentOf(s)
		if !ok {
			return n
		}
		s = p
	}
	return n
}

// Root is the root session of session's tree.
func Root(session string) string {
	s := session
	for range maxHops {
		p, _, ok := ParentOf(s)
		if !ok {
			return s
		}
		s = p
	}
	return s
}

// PathOf is session's path in its tree: "/root" for a root.
func PathOf(session string) string {
	var names []string
	s := session
	for range maxHops {
		p, n, ok := ParentOf(s)
		if !ok {
			break
		}
		names = append([]string{n}, names...)
		s = p
	}
	return strings.Join(append([]string{RootPath}, names...), "/")
}

// Target is what an address names: a session, and its state when it is
// an agent (not a root).
type Target struct {
	Session string
	Path    string
	State   *State
}

// Resolve finds the agent an address names, seen from session from: a
// child's name ("tests"), a relative path ("tests/lint"), ".." for
// from's parent, a path from the root ("/root", "/root/tests"), or "@"
// plus a session ID or unique prefix of at least six characters. ID addresses
// stay in from's tree; an empty from permits any tree (external callers only).
func Resolve(from, addr string) (Target, error) {
	if strings.HasPrefix(addr, "@") {
		return resolveID(from, addr)
	}
	cur, path := from, PathOf(from)
	rest := addr
	switch {
	case addr == "..":
		p, _, ok := ParentOf(from)
		if !ok {
			return Target{}, errors.New("this session is a root: it has no parent agent")
		}
		t := Target{Session: p, Path: PathOf(p)}
		if grandparent, name, ok := ParentOf(p); ok {
			s, err := Load(grandparent, name)
			if err != nil {
				return Target{}, err
			}
			t.State = &s
		}
		return t, nil
	case addr == RootPath || strings.HasPrefix(addr, RootPath+"/"):
		cur, path = Root(from), RootPath
		rest = strings.TrimPrefix(strings.TrimPrefix(addr, RootPath), "/")
	}
	t := Target{Session: cur, Path: path}
	if rest == "" {
		return t, nil
	}
	for name := range strings.SplitSeq(rest, "/") {
		s, err := Load(cur, name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return Target{}, fmt.Errorf("%w %q under %s (see atto agent list)", ErrNotFound, name, path)
			}
			return Target{}, err
		}
		cur, path = s.Session, path+"/"+name
		t = Target{Session: cur, Path: path, State: &s}
	}
	return t, nil
}

// Message types, as codex's.
const (
	NewTask     = "NEW_TASK"
	Message     = "MESSAGE"
	FinalAnswer = "FINAL_ANSWER"
)

// Envelope wraps text one agent sends another, the way the receiver sees
// it: inserted by atto (not typed by the user), with its kind and ends.
func Envelope(kind, from, to, text string) string {
	return fmt.Sprintf("<atto_internal_context source=\"agent\">\nMessage Type: %s\nFrom: %s\nTo: %s\n\n%s\n</atto_internal_context>", kind, from, to, strings.TrimSpace(text))
}

// ShortID is the session ID shown in agent command output.
func ShortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ErrClosed distinguishes a removed agent from an unknown address.
var ErrClosed = errors.New("agent is closed")

// A tombstone keeps the original tree and path after removal has freed the
// name and reverse index. It never appears in List or ListAll.
type closedAgent struct {
	Session string `json:"session"`
	Root    string `json:"root"`
	Path    string `json:"path"`
}

func rememberClosed(s State) error {
	if s.Session == "" {
		return nil
	}
	c := closedAgent{Session: s.Session, Root: Root(s.Session), Path: PathOf(s.Session)}
	path := existingPath("_closed", s.Session+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.Marshal(c)
	return fsutil.WriteAtomic(path, data, 0o644)
}

func resolveID(from, addr string) (Target, error) {
	id := strings.TrimPrefix(addr, "@")
	if len(id) < 6 {
		return Target{}, errors.New("agent session ID prefixes must be at least 6 characters (see atto agent list; outside atto use atto agent list -all)")
	}
	type match struct {
		target Target
		closed bool
	}
	matches := map[string]match{}
	root := ""
	if from != "" {
		root = Root(from)
	}
	for _, s := range ListAll() {
		if strings.HasPrefix(s.Session, id) && (from == "" || Root(s.Session) == root) {
			matches[s.Session] = match{target: Target{Session: s.Session, Path: PathOf(s.Session), State: &s}}
		}
	}
	for _, stateRoot := range stateRoots() {
		paths, _ := filepath.Glob(filepath.Join(stateRoot, "_closed", "*.json"))
		for _, path := range paths {
			data, err := os.ReadFile(path)
			var c closedAgent
			if err != nil || json.Unmarshal(data, &c) != nil {
				continue
			}
			if _, live := matches[c.Session]; live {
				continue
			}
			if strings.HasPrefix(c.Session, id) && (from == "" || c.Root == root) {
				matches[c.Session] = match{target: Target{Session: c.Session, Path: c.Path}, closed: true}
			}
		}
	}
	// An exact ID wins even if another ID starts with it.
	m, exact := matches[id]
	if !exact {
		switch len(matches) {
		case 0:
			return Target{}, fmt.Errorf("%w %q (see atto agent list; outside atto use atto agent list -all)", ErrNotFound, addr)
		case 1:
			for _, candidate := range matches {
				m = candidate
			}
		default:
			candidates := make([]string, 0, len(matches))
			for _, candidate := range matches {
				candidates = append(candidates, "@"+candidate.target.Session+" ("+candidate.target.Path+")")
			}
			sort.Strings(candidates)
			return Target{}, fmt.Errorf("ambiguous agent session ID %q: %s", addr, strings.Join(candidates, ", "))
		}
	}
	if m.closed {
		return Target{}, fmt.Errorf("%w: @%s (%s); its archived transcript remains available via atto sessions show %s", ErrClosed, m.target.Session, m.target.Path, m.target.Session)
	}
	return m.target, nil
}
