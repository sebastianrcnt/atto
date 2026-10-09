package core

import (
	"fmt"
	"strings"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/extensions"
)

// LoadExtensions starts the extensions for ag's directory, talking to the
// front end through host, and makes ag run them. Collect lists them and
// Reload reloads them from then on. Close the manager when the session's
// front end ends.
func LoadExtensions(ag *agent.Agent, host extensions.Host) *extensions.Manager {
	m := extensions.Load(extensions.Options{Cwd: ag.Cwd, Agent: ag, Host: host})
	ag.Extensions = m
	if mm := MCPOf(ag); mm != nil {
		m.SetMCP(mm)
	}
	return m
}

// ExtensionsOf is the extension manager ag runs, or nil.
func ExtensionsOf(ag *agent.Agent) *extensions.Manager {
	m, _ := ag.Extensions.(*extensions.Manager)
	return m
}

// ApproveHint says how to approve a project extension.
func ApproveHint(name string) string {
	return fmt.Sprintf("/extensions approve %s (or: atto extensions approve %s)", name, name)
}

// extensionWarnings are the extensions that need attention: failed, or
// waiting for approval.
func extensionWarnings(exts []extensions.Info) []string {
	var out []string
	if !extensions.Supported {
		return out
	}
	for _, e := range exts {
		switch e.Status {
		case extensions.Failed:
			out = append(out, fmt.Sprintf("extension %s failed: %s", e.Name, oneLine(e.Error)))
		case extensions.NeedsApproval:
			out = append(out, fmt.Sprintf("project extension %s is not loaded until approved: %s", e.Name, ApproveHint(e.Name)))
		}
	}
	return out
}

// extensionSummary is the Extensions row of the collapsed Loaded block.
func extensionSummary(exts []extensions.Info) string {
	if !extensions.Supported {
		return extensions.UnsupportedMessage(extensions.IgnoredCount(exts))
	}
	if len(exts) == 0 {
		return "none"
	}
	var loaded []string
	other := map[string]int{}
	for _, e := range exts {
		if e.Status == extensions.Loaded {
			loaded = append(loaded, e.Name)
		} else {
			other[e.Status]++
		}
	}
	text := "none loaded"
	if len(loaded) > 0 {
		text = fmt.Sprintf("%d: %s", len(loaded), strings.Join(loaded, ", "))
	}
	for _, st := range []string{extensions.Failed, extensions.NeedsApproval, extensions.Disabled} {
		if n := other[st]; n > 0 {
			text += fmt.Sprintf("; %d %s", n, st)
		}
	}
	return text
}

// completeSummary counts an extension's atto.complete requests per model:
// "model calls: 3 to p/m, 1 to p/n (1 failed)", "" when there are none.
func completeSummary(stats []extensions.CompleteStat) string {
	var parts []string
	failed := 0
	for _, s := range stats {
		parts = append(parts, fmt.Sprintf("%d to %s", s.Calls, s.Model))
		failed += s.Failed
	}
	if len(parts) == 0 {
		return ""
	}
	text := "model calls: " + strings.Join(parts, ", ")
	if failed > 0 {
		text += fmt.Sprintf(" (%d failed)", failed)
	}
	return text
}

// extensionRow describes one extension for the expanded Loaded block.
func extensionRow(e extensions.Info) Row {
	text := e.Status + " · " + e.Source + " · " + ShortPath(e.Path)
	switch e.Status {
	case extensions.Failed:
		text = "failed: " + e.Error + " · " + ShortPath(e.Path)
	case extensions.NeedsApproval:
		text = "needs approval: " + ApproveHint(e.Name) + " · " + ShortPath(e.Path)
	case extensions.Disabled:
		text = "disabled in settings.json · " + ShortPath(e.Path)
	}
	if len(e.Commands) > 0 {
		text += " · commands /" + strings.Join(e.Commands, ", /")
	}
	if len(e.Events) > 0 {
		text += " · on " + strings.Join(e.Events, ", ")
	}
	if c := completeSummary(e.Completes); c != "" {
		text += " · " + c
	}
	return Row{e.Name, text}
}
