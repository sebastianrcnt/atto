// Package subagent keeps what atto agent needs on disk: the presets that
// describe kinds of subagents, the state of the subagents a session
// started, and the slots that cap how many of their turns run at once.
//
// A subagent is a named child session working for a parent session. Each
// of its turns runs headless in the background as a job of the parent
// (atto _agent-turn), so atto job's wait and kill apply to it.
package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/skills"
)

// Preset is a kind of subagent: a Markdown file whose frontmatter names
// and describes it and may pick its model and effort. The body is added to
// the subagent's system prompt. Subagents start only from presets, so the
// model can't pick a model or instructions of its own.
type Preset struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	Instructions string `json:"-"`
	Path         string `json:"path,omitempty"` // "" for the built-in one
}

// General is the built-in preset. A preset file of the same name replaces
// it.
var General = Preset{
	Name:        "general",
	Description: "a general worker for a self-contained task",
	Instructions: "Do the task you are given on your own, then end with a concise report: what you did, what you found, " +
		"and anything left undone or uncertain. Include the details the other agent needs (paths, commands, results); it sees nothing else of your work.",
}

// Dirs are the preset directories for a session in cwd, lowest priority
// first: the user's (~/.atto/agents), then .atto/agents in each directory
// from the project root down to cwd, as AGENTS.md files are found.
func Dirs(cwd, root string) []string {
	var dirs []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, filepath.Join(dir, ".atto", "agents"))
		if dir == root || filepath.Dir(dir) == dir {
			break
		}
	}
	slices.Reverse(dirs)
	return append([]string{config.AgentsDir()}, dirs...)
}

// LoadPresets returns the built-in preset and the *.md presets of dirs. A
// later directory's preset replaces an earlier one (or the built-in one)
// of the same name. Files that can't be read or parsed come back as
// warnings. The result is sorted by name.
func LoadPresets(dirs []string) ([]Preset, []string) {
	byName := map[string]Preset{General.Name: General}
	var warns []string
	for _, dir := range dirs {
		paths, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		sort.Strings(paths)
		for _, path := range paths {
			p, err := ReadPreset(path)
			if err != nil {
				warns = append(warns, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			byName[p.Name] = p
		}
	}
	out := make([]Preset, 0, len(byName))
	for _, p := range byName {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warns
}

// ReadPreset parses one preset file. Without a name in the frontmatter
// the file's name (without .md) is the preset's.
func ReadPreset(path string) (Preset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Preset{}, err
	}
	fm, body, err := skills.ParseFrontmatter(string(data))
	if err != nil {
		return Preset{}, err
	}
	p := Preset{
		Name: strings.TrimSpace(fm["name"]), Description: strings.TrimSpace(fm["description"]),
		Model: strings.TrimSpace(fm["model"]), Effort: strings.TrimSpace(fm["effort"]),
		Instructions: strings.TrimSpace(body), Path: path,
	}
	if p.Name == "" {
		p.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if err := ValidName(p.Name); err != nil {
		return Preset{}, fmt.Errorf("preset %w", err)
	}
	return p, nil
}

// Find returns the preset called name.
func Find(presets []Preset, name string) (Preset, error) {
	for _, p := range presets {
		if p.Name == name {
			return p, nil
		}
	}
	var names []string
	for _, p := range presets {
		names = append(names, p.Name)
	}
	return Preset{}, fmt.Errorf("no preset %q (presets: %s; add one as <name>.md in %s or the project's .atto/agents)",
		name, strings.Join(names, ", "), config.AgentsDir())
}

// PromptList is the system prompt's list of presets.
func PromptList(presets []Preset) string {
	var b strings.Builder
	b.WriteString("Subagent presets:\n")
	for _, p := range presets {
		if p.Description != "" {
			fmt.Fprintf(&b, "- %s: %s\n", p.Name, strings.Join(strings.Fields(p.Description), " "))
		} else {
			fmt.Fprintf(&b, "- %s\n", p.Name)
		}
	}
	return b.String()
}
