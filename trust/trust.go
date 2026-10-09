// Package trust lists and records decisions about executable project content.
// Each kind keeps its existing approval file; user-owned configuration is not
// part of project trust.
package trust

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/mcp"
)

const (
	Hook      = "hook"
	MCP       = "mcp"
	Extension = "ext"

	Approved = "approved"
	Pending  = "pending"
	Denied   = "denied"
	Disabled = "disabled"
	Failed   = "failed"
)

// Item is executable content brought by the current project. Hash is the
// content shown to the user, not a promise to approve whatever appears later.
type Item struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Hash   string `json:"hash,omitempty"`
	Target string `json:"target"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`

	hook      config.ProjectHook
	server    mcp.Server
	extension extensions.Spec
}

// Key identifies the content for prompts already shown in this session.
func (in Item) Key() string { return in.Kind + ":" + in.Path + "#" + in.Name + "#" + in.Hash }

// Label describes an item without exposing environment variables or headers.
func (in Item) Label() string { return fmt.Sprintf("%s  %s  %s", in.Kind, in.Name, in.Target) }

// Command is the command the user can run in their terminal to approve in.
func (in Item) Command() string {
	return "atto trust approve " + in.Kind + " " + shellQuote(in.Name)
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\r\n'\"\\$`;&|<>()*?[]{}!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func status(a mcp.Approval) string {
	switch a {
	case mcp.Approved:
		return Approved
	case mcp.Denied:
		return Denied
	default:
		return Pending
	}
}

// Discover inspects the current project without starting servers or running
// extensions. Disabled or invalid items are listed but never prompted for.
func Discover(cwd string) ([]Item, error) {
	out := []Item{}
	hooks, hookErr := config.ProjectHooks(cwd)
	for _, h := range hooks {
		label := h.Event
		if h.Matcher != "" {
			label += " [" + h.Matcher + "]"
		}
		out = append(out, Item{Kind: Hook, Name: h.Name(), Path: h.Path, Hash: h.Hash(), Target: label + "  " + h.Target(), Status: config.HookApprovalOf(h), hook: h})
	}
	servers, issues := mcp.Load(agent.ProjectRoot(cwd))
	for _, s := range servers {
		if s.Scope != mcp.ScopeProject {
			continue
		}
		in := Item{Kind: MCP, Name: s.Name, Path: s.Path, Hash: s.Config.Hash(), Target: s.Config.Target(), Status: status(mcp.ApprovalOf(s)), server: s}
		if err := s.Config.Validate(); err != nil {
			in.Status, in.Error = Failed, err.Error()
		}
		out = append(out, in)
	}
	for _, info := range extensions.Inspect(cwd) {
		if info.Source != extensions.Project || !extensions.Supported {
			continue
		}
		s := extensions.Spec{Name: info.Name, Path: info.Path, Source: info.Source}
		in := Item{Kind: Extension, Name: info.Name, Path: info.Path, Hash: info.Hash, Target: info.Path, Status: status(extensions.ApprovalOf(s, info.Hash)), extension: s}
		switch info.Status {
		case extensions.Disabled:
			in.Status = Disabled
		case extensions.Failed:
			in.Status, in.Error = Failed, info.Error
		}
		out = append(out, in)
	}
	if hookErr != nil {
		issues = append(issues, hookErr.Error())
	}
	if len(issues) > 0 {
		return out, fmt.Errorf("%s", strings.Join(issues, "; "))
	}
	return out, nil
}

// Unapproved selects the content that needs a decision. Denials are remembered
// for this content, just as approvals are, until it changes or is revoked.
func Unapproved(items []Item) []Item {
	var out []Item
	for _, in := range items {
		if in.Status == Pending {
			out = append(out, in)
		}
	}
	return out
}

// Approve approves exactly the item that was displayed.
func Approve(in Item) error { return decide(in, true) }

// Deny records that this content should stay off, without prompting again.
func Deny(in Item) error { return decide(in, false) }

func decide(in Item, allow bool) error {
	if in.Status == Failed || in.Status == Disabled || in.Hash == "" {
		return fmt.Errorf("%s %s cannot be approved: %s", in.Kind, in.Name, in.Status)
	}
	switch in.Kind {
	case Hook:
		return config.SetHookApproval(in.hook, allow)
	case MCP:
		if allow {
			return mcp.Approve(in.server)
		}
		return mcp.Deny(in.server)
	case Extension:
		return extensions.SetApproval(in.extension, in.Hash, allow)
	}
	return fmt.Errorf("unknown trust item kind %q", in.Kind)
}

// Revoke forgets a decision, so this item needs approval again.
func Revoke(in Item) error {
	switch in.Kind {
	case Hook:
		return config.RevokeHook(in.hook)
	case MCP:
		return mcp.Revoke(in.server)
	case Extension:
		return extensions.Revoke(in.extension)
	}
	return fmt.Errorf("unknown trust item kind %q", in.Kind)
}

// Warn names unapproved content left off where there is no UI to ask.
func Warn(out io.Writer, items []Item) {
	for _, in := range items {
		if in.Status == Pending || in.Status == Denied {
			fmt.Fprintf(out, "atto: project %s %s is %s and will not run (%s). Approve from your terminal: %s\n", in.Kind, in.Name, in.Status, in.Target, in.Command())
		}
	}
}

// WarnProject inspects a headless session's configuration and reports content
// left off. Parse failures are warnings too; other valid items still appear.
func WarnProject(out io.Writer, cwd string) {
	items, err := Discover(cwd)
	if err != nil {
		fmt.Fprintf(out, "atto: project trust: %s\n", err)
	}
	Warn(out, items)
}

// RevokeAll forgets all of the project's decisions, including content that was
// removed or changed after approval. Restoring old content cannot restore trust.
func RevokeAll(cwd string) error {
	return errors.Join(config.RevokeProjectHooks(cwd), mcp.RevokeProject(agent.ProjectRoot(cwd)), extensions.RevokeProject(cwd))
}
