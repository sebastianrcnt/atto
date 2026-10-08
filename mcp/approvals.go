package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

// Project servers run commands a repository brings, so each needs the
// user's approval once, like a project extension. mcp-approvals.json
// records it by the .mcp.json file's absolute path and the server's name,
// with the hash of the entry as approved: a change to it needs approval
// again. "all" approves every server of a file, now and later.
type approvals struct {
	Approved map[string]string `json:"approved"`
	Denied   map[string]string `json:"denied,omitempty"`
	All      []string          `json:"all,omitempty"`
}

var approvalsMu sync.Mutex

func approvalKey(file, name string) string { return absPath(file) + "#" + name }

func loadApprovals() approvals {
	a := approvals{}
	if data, err := os.ReadFile(config.MCPApprovalsPath()); err == nil {
		_ = json.Unmarshal(data, &a)
	}
	if a.Approved == nil {
		a.Approved = map[string]string{}
	}
	if a.Denied == nil {
		a.Denied = map[string]string{}
	}
	return a
}

func (a approvals) save() error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(config.MCPApprovalsPath()), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(config.MCPApprovalsPath(), append(data, '\n'), 0o600)
}

func (a approvals) allowsAll(file string) bool {
	for _, f := range a.All {
		if f == absPath(file) {
			return true
		}
	}
	return false
}

// Approval is where a server stands with the user.
type Approval int

const (
	// Approved: it may start (user and local servers always are).
	Approved Approval = iota
	// Pending: a project server nobody has decided on yet, or whose entry
	// changed since it was.
	Pending
	// Denied: the user said no to this entry.
	Denied
)

// ApprovalOf reports whether s may start.
func ApprovalOf(s Server) Approval {
	if s.Scope != ScopeProject {
		return Approved
	}
	approvalsMu.Lock()
	defer approvalsMu.Unlock()
	a := loadApprovals()
	key, h := approvalKey(s.Path, s.Name), s.Config.Hash()
	switch {
	case a.Approved[key] == h || a.allowsAll(s.Path):
		return Approved
	case a.Denied[key] == h:
		return Denied
	}
	return Pending
}

// Approve records that the user lets project server s run as it is now.
func Approve(s Server) error {
	approvalsMu.Lock()
	defer approvalsMu.Unlock()
	a := loadApprovals()
	key := approvalKey(s.Path, s.Name)
	a.Approved[key] = s.Config.Hash()
	delete(a.Denied, key)
	return a.save()
}

// ApproveAll records that the user lets every server in s's file run,
// whatever it says later.
func ApproveAll(s Server) error {
	approvalsMu.Lock()
	defer approvalsMu.Unlock()
	a := loadApprovals()
	if !a.allowsAll(s.Path) {
		a.All = append(a.All, absPath(s.Path))
	}
	return a.save()
}

// Deny records that the user does not want project server s to run as it
// is now; approving it later (or changing it) lifts that.
func Deny(s Server) error {
	approvalsMu.Lock()
	defer approvalsMu.Unlock()
	a := loadApprovals()
	key := approvalKey(s.Path, s.Name)
	a.Denied[key] = s.Config.Hash()
	delete(a.Approved, key)
	return a.save()
}

// Revoke forgets a project's decision for s. A legacy approval of the whole
// file becomes approvals of its other current entries, never future ones.
func Revoke(s Server) error {
	approvalsMu.Lock()
	defer approvalsMu.Unlock()
	a := loadApprovals()
	if a.allowsAll(s.Path) {
		var files []string
		for _, file := range a.All {
			if file != absPath(s.Path) {
				files = append(files, file)
			}
		}
		a.All = files
		servers, _ := Load(filepath.Dir(s.Path))
		for _, other := range servers {
			if other.Scope == ScopeProject && absPath(other.Path) == absPath(s.Path) && other.Name != s.Name {
				a.Approved[approvalKey(other.Path, other.Name)] = other.Config.Hash()
			}
		}
	}
	key := approvalKey(s.Path, s.Name)
	delete(a.Approved, key)
	delete(a.Denied, key)
	return a.save()
}

// RevokeProject forgets this project's decisions, including removed servers
// and legacy approvals of the whole file.
func RevokeProject(root string) error {
	approvalsMu.Lock()
	defer approvalsMu.Unlock()
	a := loadApprovals()
	file := absPath(config.ProjectMCPPath(root))
	prefix := file + "#"
	for key := range a.Approved {
		if strings.HasPrefix(key, prefix) {
			delete(a.Approved, key)
		}
	}
	for key := range a.Denied {
		if strings.HasPrefix(key, prefix) {
			delete(a.Denied, key)
		}
	}
	a.All = slices.DeleteFunc(a.All, func(path string) bool { return path == file })
	return a.save()
}
