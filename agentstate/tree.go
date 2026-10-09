package agentstate

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/session"
)

// Agents form trees, as in codex: a session that starts agents is their
// parent, an agent may start agents of its own, and each has a path from
// the tree's root, "/root/tests/lint". The path is how agents address each
// other. An agent started from a shell has no parent: it is the root of a
// tree of its own, at /root, and its name is only a label to find it by in
// its project. Tree position lives in each agent's record (and session
// header); the storage layout has no trees.

// RootPath is the path of a tree's root.
const RootPath = "/root"

// maxHops bounds walks up a tree, against a corrupt loop. It is not a
// limit on how deep trees grow.
const maxHops = 256

// Root is the root session of session's tree: the session itself for an
// ordinary session (or an unknown one), its recorded root for an agent.
func Root(session string) string {
	if sum, ok := summaryOf(session); ok && sum.Root != "" {
		return sum.Root
	}
	return session
}

// Depth is how many edges session is below its root: 0 for a root.
func Depth(session string) int {
	if sum, ok := summaryOf(session); ok {
		return sum.Depth
	}
	return 0
}

// PathOf is session's path in its tree: "/root" for a root.
func PathOf(session string) string {
	if sum, ok := summaryOf(session); ok && sum.Path != "" {
		return sum.Path
	}
	return RootPath
}

// ParentOf returns the session that started session as an agent, and its
// name there; ok is false for a root and for an ordinary session.
func ParentOf(session string) (parent, name string, ok bool) {
	if sum, found := summaryOf(session); found && sum.Parent != "" {
		return sum.Parent, sum.Name, true
	}
	return "", "", false
}

// IsAgent reports whether session is a managed agent with a record.
func IsAgent(session string) bool {
	_, ok := summaryOf(session)
	return ok
}

// managedSession reports whether the session's own header marks it as a
// managed agent: its record must then exist.
func managedSession(id string) bool {
	path, err := session.Find(id)
	if err != nil {
		return false
	}
	s, err := session.Summarize(path)
	return err == nil && s.Agent != nil
}

// Ancestry is the chain from session up to its root, session first. It
// fails closed on a broken tree: a loop, a missing record of a managed
// parent, or a record that disagrees with its parent about root or depth.
// An ordinary session ends the chain (it has no record by design).
func Ancestry(id string) ([]string, error) {
	chain := []string{id}
	seen := map[string]bool{id: true}
	first, isAgent := summaryOf(id)
	cur := id
	for range maxHops {
		sum, ok := summaryOf(cur)
		if !ok {
			if cur != id && managedSession(cur) {
				return chain, fmt.Errorf("agent %s names a parent, %s, that has no record: %w", chain[len(chain)-2], cur, ErrBrokenTree)
			}
			return chain, checkPosition(first, isAgent, cur, chain) // cur is an ordinary session: the root
		}
		if sum.Parent == "" {
			if sum.Root != sum.ID || sum.Depth != 0 {
				return chain, fmt.Errorf("agent %s has no parent but records root %s at depth %d: %w", sum.ID, sum.Root, sum.Depth, ErrBrokenTree)
			}
			return chain, checkPosition(first, isAgent, cur, chain)
		}
		if seen[sum.Parent] {
			return chain, fmt.Errorf("agent ancestry of %s has a loop at %s: %w", id, sum.Parent, ErrBrokenTree)
		}
		seen[sum.Parent] = true
		chain = append(chain, sum.Parent)
		cur = sum.Parent
	}
	return chain, fmt.Errorf("agent ancestry of %s is longer than %d: %w", id, maxHops, ErrBrokenTree)
}

// checkPosition compares the record of the session the walk started from
// with where its parents led: the same root, at the same depth.
func checkPosition(first Summary, isAgent bool, root string, chain []string) error {
	if !isAgent {
		return nil
	}
	if first.Root != root || first.Depth != len(chain)-1 {
		return fmt.Errorf("agent %s records root %s at depth %d, but its parents lead to %s at depth %d: %w", first.ID, first.Root, first.Depth, root, len(chain)-1, ErrBrokenTree)
	}
	return nil
}

// ErrBrokenTree is wrapped by Ancestry's failures.
var ErrBrokenTree = errors.New("the agent tree is inconsistent")

// Target is what an address names: a session, and its state when it is a
// managed agent (an ordinary session that roots a tree has none).
type Target struct {
	Session string
	Path    string
	State   *State
}

func targetOf(s State) Target { return Target{Session: s.Session, Path: s.Path, State: &s} }

// ordinary is the target of an ordinary session.
func ordinary(id string) Target { return Target{Session: id, Path: RootPath} }

// Resolve finds the agent an address names, seen from session from: a
// child's name ("tests"), a relative path ("tests/lint"), ".." for from's
// parent, a path from the root ("/root", "/root/tests"), or "@" plus a
// session ID or unique prefix of at least six characters. ID addresses stay
// in from's tree; an empty from permits any tree (callers outside atto only).
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
		if s, err := Load(p); err == nil {
			return targetOf(s), nil
		}
		return ordinary(p), nil
	case addr == RootPath || strings.HasPrefix(addr, RootPath+"/"):
		cur, path = Root(from), RootPath
		rest = strings.TrimPrefix(strings.TrimPrefix(addr, RootPath), "/")
	}
	return descend(cur, path, rest)
}

// descend follows the child names in rest from session cur at path.
func descend(cur, path, rest string) (Target, error) {
	t := ordinary(cur)
	t.Path = path
	if s, err := Load(cur); err == nil {
		t = targetOf(s)
	}
	if rest == "" {
		return t, nil
	}
	for name := range strings.SplitSeq(rest, "/") {
		s, err := LoadChild(cur, name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return Target{}, fmt.Errorf("%w %q under %s (see atto agent list)", ErrNotFound, name, path)
			}
			return Target{}, err
		}
		cur, path = s.Session, s.Path
		t = targetOf(s)
	}
	return t, nil
}

// ResolveOutside finds the agent an address names for a caller outside atto
// in project. Bare names select the open agents started from a shell in the
// project (roots, which may share a name); the rest of a path descends into
// that root's own tree. "/root/NAME" is a compatibility spelling of NAME.
// There is no selected tree outside, so "/root" and ".." need -session.
func ResolveOutside(project, addr string) (Target, error) {
	if addr == RootPath || addr == ".." {
		return Target{}, fmt.Errorf("no selected parent session: pass -session PARENT to address %q, or use an agent's @id", addr)
	}
	path := strings.TrimPrefix(addr, RootPath+"/")
	first, rest, _ := strings.Cut(path, "/")
	if err := ValidName(first); err != nil {
		return Target{}, err
	}
	var matches []State
	for _, st := range ExternalRoots(project, false) {
		if st.Name == first {
			matches = append(matches, st)
		}
	}
	switch len(matches) {
	case 0:
		return Target{}, fmt.Errorf("%w %q in this project (see atto agent list; atto agent list -all shows other projects)", ErrNotFound, first)
	case 1:
		root := matches[0]
		if rest == "" {
			return targetOf(root), nil
		}
		return descend(root.Session, root.Path, rest)
	default:
		var candidates []string
		for _, st := range matches {
			candidates = append(candidates, fmt.Sprintf("@%s (%s, %s)", st.Session, st.Latest().Status, Age(st.Created)))
		}
		return Target{}, fmt.Errorf("%d agents named %s: %s; use @id", len(matches), first, strings.Join(candidates, "; "))
	}
}

// Age is a coarse age for listings of candidates.
func Age(created time.Time) string {
	if created.IsZero() {
		return "unknown age"
	}
	age := max(time.Since(created), 0)
	switch {
	case age >= time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	case age >= time.Minute:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	}
}

// Label is how listings spell an agent. Inside a tree that is its path;
// for a caller outside atto, an agent started from a shell is its project
// label ("tests"), and what is below it is spelled from there ("tests/lint"),
// so nothing implies one tree for the whole project.
func Label(s State, outside bool) string {
	if !outside {
		return s.Path
	}
	if s.IsRoot() {
		return s.Name
	}
	if root, err := Load(s.Root); err == nil && root.IsRoot() {
		return root.Name + strings.TrimPrefix(s.Path, RootPath)
	}
	return s.Path
}

// Message types, as codex's.
const (
	NewTask     = "NEW_TASK"
	Message     = "MESSAGE"
	FinalAnswer = "FINAL_ANSWER"
)

// OutsideSender is the From of a task given from outside atto.
const OutsideSender = "external"

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

func resolveID(from, addr string) (Target, error) {
	id := strings.TrimPrefix(addr, "@")
	if len(id) < 6 {
		return Target{}, errors.New("agent session ID prefixes must be at least 6 characters (see atto agent list; outside atto use atto agent list -all)")
	}
	root := ""
	if from != "" {
		root = Root(from)
	}
	type match struct {
		sum Summary
		// ordinary: the ordinary session that roots the caller's tree.
		ordinary bool
	}
	matches := map[string]match{}
	for _, sum := range inventory() {
		if strings.HasPrefix(sum.ID, id) && (from == "" || sum.Root == root) {
			matches[sum.ID] = match{sum: sum}
		}
	}
	// IDs can name the ordinary session that roots the caller's tree.
	if from != "" && strings.HasPrefix(root, id) {
		if _, isAgent := matches[root]; !isAgent {
			matches[root] = match{sum: Summary{ID: root, Root: root, Path: RootPath}, ordinary: true}
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
				candidates = append(candidates, "@"+candidate.sum.ID+" ("+candidate.sum.Path+")")
			}
			sort.Strings(candidates)
			return Target{}, fmt.Errorf("ambiguous agent session ID %q: %s", addr, strings.Join(candidates, ", "))
		}
	}
	if m.ordinary {
		return ordinary(m.sum.ID), nil
	}
	if m.sum.Lifecycle == Closed {
		return Target{}, fmt.Errorf("%w: @%s (%s); its archived transcript remains available via atto sessions show %s", ErrClosed, m.sum.ID, m.sum.Path, m.sum.ID)
	}
	s, err := Load(m.sum.ID)
	if err != nil {
		return Target{}, err
	}
	return targetOf(s), nil
}
