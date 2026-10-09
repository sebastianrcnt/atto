package session

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// AgentMetaVersion is the version of the agent object in a session header.
const AgentMetaVersion = 1

// Where an agent came from.
const (
	// OriginExternal: started by atto agent from a plain shell.
	OriginExternal = "external"
	// OriginAgent: started by a model session, or by an agent.
	OriginAgent = "agent"
)

// AgentMeta is what a session's header says about the session as an agent
// (atto agent): its place in a tree, never where its state is stored. The
// session ID is the agent's identity; the parent, the role and the path
// label are attributes. A session with this object is a managed agent, even
// when it has no parent (an agent started from a shell is the root of a tree
// of its own). Ordinary sessions have none: they are the root of their tree
// by implication, at depth 0, path /root.
type AgentMeta struct {
	Version int `json:"version"`
	// ParentSessionID is the session that started this one; null for a root.
	ParentSessionID *string `json:"parentSessionId"`
	// RootSessionID is the root of the tree: the session itself for a root.
	RootSessionID string `json:"rootSessionId"`
	// Depth counts the edges from the root: 0 for a root.
	Depth int `json:"depth"`
	// Path is the label of the agent in its tree: /root for a root,
	// /root/tests/lint below it. Labels may be reused after an agent closes.
	Path string `json:"path"`
	// Name is the agent's name among its parent's children; for a root
	// started from a shell, the label atto agent finds it by in its project.
	Name string `json:"name"`
	// Role is the preset the agent started from.
	Role string `json:"role,omitempty"`
	// SpawnCwd is the directory atto agent ran in when it started the agent
	// (the agent may work in a worktree elsewhere).
	SpawnCwd string `json:"spawnCwd,omitempty"`
	// Project is the canonical project (git root, else directory) of the
	// spawn, and is inherited by every descendant.
	Project string `json:"project,omitempty"`
	// Origin is OriginExternal or OriginAgent.
	Origin string `json:"origin,omitempty"`
}

// Parent is the ID of the session that started this one, "" for a root.
func (m AgentMeta) Parent() string {
	if m.ParentSessionID == nil {
		return ""
	}
	return *m.ParentSessionID
}

// IsRoot reports whether the agent has no parent.
func (m AgentMeta) IsRoot() bool { return m.Parent() == "" }

// Validate checks that the metadata is consistent for the session id.
func (m AgentMeta) Validate(id string) error {
	switch {
	case m.Version < 1:
		return errors.New("agent metadata has no version")
	case m.Version > AgentMetaVersion:
		return fmt.Errorf("agent metadata version %d is newer than this atto understands (%d): upgrade atto", m.Version, AgentMetaVersion)
	case id == "":
		return errors.New("agent metadata without a session ID")
	case m.Depth < 0:
		return fmt.Errorf("agent %s: negative depth %d", id, m.Depth)
	case m.RootSessionID == "":
		return fmt.Errorf("agent %s: no root session", id)
	case m.Parent() == id:
		return fmt.Errorf("agent %s is its own parent", id)
	case m.IsRoot() && (m.RootSessionID != id || m.Depth != 0 || m.Path != "/root"):
		return fmt.Errorf("agent %s has no parent, so it must be its own root at depth 0 with path /root (root %s, depth %d, path %q)", id, m.RootSessionID, m.Depth, m.Path)
	case !m.IsRoot() && m.Depth < 1:
		return fmt.Errorf("agent %s has a parent but depth %d", id, m.Depth)
	case !m.IsRoot() && m.RootSessionID == id:
		return fmt.Errorf("agent %s has a parent but is its own root", id)
	case !strings.HasPrefix(m.Path, "/root"):
		return fmt.Errorf("agent %s: path %q does not start at /root", id, m.Path)
	case !m.IsRoot() && path.Base(m.Path) != m.Name:
		return fmt.Errorf("agent %s: path %q does not end in its name %q", id, m.Path, m.Name)
	}
	return nil
}

// NewManaged prepares the session of a managed agent in cwd. The session ID
// is chosen first, so that build (which makes the agent metadata) and
// anything created for the agent (worktrees, branches) can use it. The
// header also keeps AgentOf for a child: older readers filter on it.
func NewManaged(cwd string, build func(id string) AgentMeta) *Writer {
	w := New(cwd)
	meta := build(w.ID)
	w.agent = &meta
	w.agentOf = meta.Parent()
	return w
}

// NewAgent is NewManaged for a child of parent that has no more metadata
// than that: a root of parent's tree is assumed (tests, older callers).
func NewAgent(cwd, parent string) *Writer {
	return NewManaged(cwd, func(id string) AgentMeta {
		p := parent
		return AgentMeta{Version: AgentMetaVersion, ParentSessionID: &p, RootSessionID: parent, Depth: 1, Path: "/root/" + id, Name: id, Origin: OriginAgent}
	})
}

// NewExternal prepares the lightweight parent older versions made for agents
// started from a shell. Nothing writes these any more.
//
// Deprecated: removed once atto agent stops using it.
func NewExternal(cwd string) *Writer {
	w := New(cwd)
	w.external = true
	return w
}

// IsAgent reports whether the summary is of an agent session, however it
// was recorded: by its agent object, or only by the AgentOf of older
// headers.
func (s Summary) IsAgent() bool { return s.Agent != nil || s.AgentOf != "" }

// IsAgent is Summary.IsAgent for a header.
func (e Entry) IsAgent() bool { return e.Agent != nil || e.AgentOf != "" }
