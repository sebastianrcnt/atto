package mcp

import (
	"path/filepath"
	"slices"

	"github.com/sebastianrcnt/atto/approval"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

// Project servers run commands a repository brings, so each needs the
// user's approval once, like a project extension. mcp-approvals.json
// records it by the .mcp.json file's absolute path and the server's name,
// with the hash of the entry as approved: a change to it needs approval
// again. "all" approves every server of a file, now and later.
type approvals struct {
	approval.Decisions
	All []string `json:"all,omitempty"`
}

func approvalKey(file, name string) string { return approval.Path(file) + "#" + name }

func loadApprovals() approvals {
	a, _ := fsutil.ReadJSON[approvals](config.MCPApprovalsPath())
	return a
}

func editApprovals(edit func(*approvals)) error {
	return fsutil.EditJSON(config.MCPApprovalsPath(), func(a *approvals) { a.Init(); edit(a) })
}

func (a approvals) allowsAll(file string) bool {
	for _, f := range a.All {
		if f == approval.Path(file) {
			return true
		}
	}
	return false
}

// Approval is where a server stands with the user.
type Approval = approval.Status

const (
	// Approved: it may start (user and local servers always are).
	Approved Approval = approval.Approved
	// Pending: a project server nobody has decided on yet, or whose entry
	// changed since it was.
	Pending = approval.Pending
	// Denied: the user said no to this entry.
	Denied = approval.Denied
)

// ApprovalOf reports whether s may start.
func ApprovalOf(s Server) Approval {
	if s.Scope != ScopeProject {
		return Approved
	}
	a := loadApprovals()
	if a.allowsAll(s.Path) {
		return Approved
	}
	return a.Of(approvalKey(s.Path, s.Name), s.Config.Hash())
}

// Approve records that the user lets project server s run as it is now.
func Approve(s Server) error {
	return editApprovals(func(a *approvals) { a.Set(approvalKey(s.Path, s.Name), s.Config.Hash(), true) })
}

// ApproveAll records the legacy approval of every server in s's file.
func ApproveAll(s Server) error {
	return editApprovals(func(a *approvals) {
		if !a.allowsAll(s.Path) {
			a.All = append(a.All, approval.Path(s.Path))
		}
	})
}

// Deny records that the user does not want this project's current entry.
func Deny(s Server) error {
	return editApprovals(func(a *approvals) { a.Set(approvalKey(s.Path, s.Name), s.Config.Hash(), false) })
}

// Revoke forgets a project's decision for s. A legacy approval of the whole
// file becomes approvals of its other current entries, never future ones.
func Revoke(s Server) error {
	return editApprovals(func(a *approvals) {
		if a.allowsAll(s.Path) {
			var files []string
			for _, file := range a.All {
				if file != approval.Path(s.Path) {
					files = append(files, file)
				}
			}
			a.All = files
			servers, _ := Load(filepath.Dir(s.Path))
			for _, other := range servers {
				if other.Scope == ScopeProject && approval.Path(other.Path) == approval.Path(s.Path) && other.Name != s.Name {
					a.Approved[approvalKey(other.Path, other.Name)] = other.Config.Hash()
				}
			}
		}
		a.Forget(approvalKey(s.Path, s.Name))
	})
}

// RevokeProject forgets this project's decisions, including removed servers
// and legacy approvals of the whole file.
func RevokeProject(root string) error {
	return editApprovals(func(a *approvals) {
		file := approval.Path(config.ProjectMCPPath(root))
		a.ForgetPrefix(file + "#")
		a.All = slices.DeleteFunc(a.All, func(path string) bool { return path == file })
	})
}
