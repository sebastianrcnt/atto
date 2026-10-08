package agentstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/config"
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
	return filepath.Join(config.AgentStateDir(), "_up", session+".json")
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
	dirs, _ := os.ReadDir(config.AgentStateDir())
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "_up" {
			continue
		}
		for _, s := range List(d.Name()) {
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
// from's parent, or a path from the root ("/root", "/root/tests").
func Resolve(from, addr string) (Target, error) {
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
