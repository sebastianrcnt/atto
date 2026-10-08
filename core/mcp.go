package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/mcp"
)

// LoadMCP reads the MCP server configuration for ag's directory and makes
// ag's system prompt name the servers. Servers start on first use, inside
// this process, and stay until the manager is closed: close it when the
// session's front end ends. Call it before Bind (which builds the prompt),
// or Reload the agent after it.
func LoadMCP(ag *agent.Agent) *mcp.Manager {
	m := mcp.New(mcp.Options{Cwd: ag.Cwd, Root: agent.ProjectRoot(ag.Cwd)})
	ag.MCP = m
	if x := ExtensionsOf(ag); x != nil {
		x.SetMCP(m)
	}
	return m
}

// MCPOf is the MCP manager ag runs, or nil.
func MCPOf(ag *agent.Agent) *mcp.Manager {
	m, _ := ag.MCP.(*mcp.Manager)
	return m
}

// MCPApproveHint says how to approve a project MCP server.
func MCPApproveHint(name string) string {
	return "atto mcp approve " + name
}

// mcpInfos reports ag's MCP servers.
func mcpInfos(ag *agent.Agent) (infos []mcp.Info, warnings []string) {
	m := MCPOf(ag)
	if m == nil {
		return nil, nil
	}
	infos, _ = m.Servers(context.Background())
	warnings = m.Issues()
	for _, in := range infos {
		switch in.Status {
		case mcp.NeedsApproval:
			warnings = append(warnings, fmt.Sprintf("project MCP server %s is not started until approved: %s", in.Name, MCPApproveHint(in.Name)))
		case mcp.DeniedStatus:
			warnings = append(warnings, fmt.Sprintf("project MCP server %s was denied; to allow it: %s", in.Name, MCPApproveHint(in.Name)))
		case mcp.Failed:
			if in.Invalid {
				warnings = append(warnings, fmt.Sprintf("MCP server %s: %s", in.Name, oneLine(in.Error)))
			}
		}
	}
	return infos, warnings
}

// mcpIgnoredText explains a repository's .atto/mcp.json that is not read.
func mcpIgnoredText(path, cwd string) string {
	return fmt.Sprintf("%s is ignored (a repository must not start commands unapproved): move it to %s with atto mcp add -scope local, or share it as .mcp.json, which needs approval",
		ShortPath(path), ShortPath(config.LocalMCPPath(agent.ProjectRoot(cwd))))
}

// mcpSummary is the MCP row of the collapsed Loaded block.
func mcpSummary(infos []mcp.Info) string {
	var names []string
	other := map[string]int{}
	running := 0
	for _, in := range infos {
		switch in.Status {
		case mcp.NeedsApproval, mcp.DeniedStatus, mcp.Failed:
			other[in.Status]++
		case mcp.Running:
			running++
			fallthrough
		default:
			names = append(names, in.Name)
		}
	}
	text := "none usable"
	if len(names) > 0 {
		text = fmt.Sprintf("%d: %s", len(names), strings.Join(names, ", "))
		if running > 0 {
			text += fmt.Sprintf(" (%d running)", running)
		}
	}
	for _, st := range []string{mcp.Failed, mcp.NeedsApproval, mcp.DeniedStatus} {
		if n := other[st]; n > 0 {
			text += fmt.Sprintf("; %d %s", n, st)
		}
	}
	return text
}

// mcpRow describes one MCP server for the expanded Loaded block.
func mcpRow(in mcp.Info) Row {
	text := fmt.Sprintf("%s · %s · %s", in.Scope, in.Transport, clipWithEllipsis(oneLine(in.Target), 70))
	switch in.Status {
	case mcp.Running:
		text += " · " + plural(in.Tools, "tool") + ", running"
	case mcp.Failed:
		text += " · failed: " + oneLine(in.Error)
	case mcp.NeedsApproval:
		text += " · needs approval: " + MCPApproveHint(in.Name)
	case mcp.DeniedStatus:
		text += " · denied: " + MCPApproveHint(in.Name) + " to allow"
	default:
		text += " · " + in.Status
	}
	return Row{in.Name, text}
}
