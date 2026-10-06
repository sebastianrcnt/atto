// Package prompts holds the text atto sends to the model as templates, one
// .md file each. A file's last newline is the editor's: Render drops it,
// so a template ends where its last line does, and a caller that wants a
// blank line after it (a prefix before the text it introduces) adds that.
package prompts

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed *.md
var files embed.FS

var tmpl = template.Must(parse())

func parse() (*template.Template, error) {
	t := template.New("")
	list, err := files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	for _, f := range list {
		b, err := files.ReadFile(f.Name())
		if err != nil {
			return nil, err
		}
		if _, err := t.New(strings.TrimSuffix(f.Name(), ".md")).Parse(string(b)); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// Render runs the template called name (its file without .md). Templates
// are embedded and tested, so an error is a bug and panics. Values are
// inserted as they are: nothing is escaped.
func Render(name string, data any) string {
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, name, data); err != nil {
		panic("prompts: " + name + ": " + err.Error())
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// Data of the templates that take any.

// System is the data of "system".
type System struct {
	Kind    string // "powershell", "cmd", or anything else for bash
	WinPS51 bool   // Windows PowerShell 5.1, which lacks && and ||
	Tool    string // the shell tool's name
	Sub     string // the paragraph about subagents, or ""
	MCP     string // the configured MCP servers, comma-separated, or ""
	Cwd     string
	OS      string
	Arch    string
	Shell   string // the shell's path
	Date    string
}

// Subagent is the data of "subagent".
type Subagent struct {
	Name, Preset, Instructions string
	Worktree, Branch           string // with -worktree
}

// Goal is the data of the goal_* templates.
type Goal struct {
	Objective string // already escaped
	Turns     int
	Label     string // the status as the user sees it: "paused", "stalled"...
	// Interrupted: the goal is paused because the user interrupted a turn.
	Interrupted bool
}
