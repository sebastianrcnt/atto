// Package skills finds Agent Skills (agentskills.io): directories with a
// SKILL.md whose YAML frontmatter names and describes them. The rules follow
// pi's skills.ts so a skill written for pi works here unchanged.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxNameLength        = 64
	maxDescriptionLength = 1024
)

type Skill struct {
	Name        string
	Description string
	FilePath    string // the SKILL.md
	BaseDir     string // its directory, which relative paths in the skill resolve against
	// DisableModelInvocation hides the skill from the system prompt; it can
	// still be run explicitly with /skill:name.
	DisableModelInvocation bool
	// Source is Builtin for the skills inside atto; empty for those found
	// in a directory.
	Source string
}

// Builtin is the Source of a skill that ships inside atto.
const Builtin = "builtin"

// Dirs lists where skills are looked for, highest priority first (the first
// skill of a name wins): userDir (~/.atto/skills), then the project's
// .atto/skills. Directories other tools read too (.agents/skills,
// .claude/skills) are not: what they hold was installed for those tools,
// and it would all go into atto's prompt.
func Dirs(userDir, root string) []string {
	dirs := []string{userDir}
	if root != "" {
		dirs = append(dirs, filepath.Join(root, ".atto", "skills"))
	}
	return dirs
}

// Issue is a problem with a skill file. Skipped ones are not loaded
// (unreadable, bad frontmatter, no description, a name already taken);
// the others load with a warning (an invalid name, say), as in pi.
type Issue struct {
	Path    string `json:"path"`
	Reason  string `json:"reason"`
	Skipped bool   `json:"skipped,omitempty"`
}

func (i Issue) String() string { return i.Path + ": " + i.Reason }

// Load discovers the skills of dirs. The warnings (invalid names, duplicates,
// unreadable files) are for the user; the skills are still usable.
func Load(dirs []string) (skills []Skill, warnings []string) {
	skills, issues := LoadIssues(dirs)
	for _, is := range issues {
		warnings = append(warnings, is.String())
	}
	return skills, warnings
}

// LoadIssues is Load with the warnings as Issues.
func LoadIssues(dirs []string) (skills []Skill, issues []Issue) {
	byName := map[string]string{} // name -> winner's path
	seen := map[string]bool{}     // real paths, so symlinked copies are silent
	for _, dir := range dirs {
		found, warns := loadDir(dir, true)
		issues = append(issues, warns...)
		for _, s := range found {
			real, err := filepath.EvalSymlinks(s.FilePath)
			if err != nil {
				real = s.FilePath
			}
			if seen[real] {
				continue
			}
			if winner, dup := byName[s.Name]; dup {
				issues = append(issues, Issue{s.FilePath, fmt.Sprintf("skill %q ignored: %s already defines it", s.Name, winner), true})
				continue
			}
			byName[s.Name] = s.FilePath
			seen[real] = true
			skills = append(skills, s)
		}
	}
	return skills, issues
}

// loadDir applies pi's discovery rules: a directory with SKILL.md is a skill
// root and is not searched further; otherwise subdirectories are searched,
// and (only at the top) loose .md files that have a description count too.
func loadDir(dir string, top bool) (skills []Skill, warnings []Issue) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		if e.Name() != "SKILL.md" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			continue
		}
		s, warns := loadFile(p)
		if s != nil {
			skills = append(skills, *s)
		}
		return skills, warns
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		p := filepath.Join(dir, name)
		st, err := os.Stat(p) // follows symlinks; broken ones are skipped
		if err != nil {
			continue
		}
		switch {
		case st.IsDir():
			s, w := loadDir(p, false)
			skills, warnings = append(skills, s...), append(warnings, w...)
		case top && st.Mode().IsRegular() && strings.HasSuffix(name, ".md"):
			s, w := loadFile(p)
			if s != nil {
				skills = append(skills, *s)
			}
			warnings = append(warnings, w...)
		}
	}
	return skills, warnings
}

var validName = regexp.MustCompile(`^[a-z0-9-]+$`)

func loadFile(path string) (*Skill, []Issue) {
	declared := filepath.Base(path) == "SKILL.md"
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []Issue{{path, err.Error(), true}}
	}
	fm, _, err := ParseFrontmatter(string(data))
	if err != nil {
		if declared {
			return nil, []Issue{{path, err.Error(), true}}
		}
		return nil, nil
	}
	desc := fm["description"]
	hasDesc := strings.TrimSpace(desc) != ""
	if !declared && !hasDesc {
		return nil, nil // a plain markdown file, not a skill
	}
	var warns []Issue
	warn := func(format string, args ...any) {
		warns = append(warns, Issue{Path: path, Reason: fmt.Sprintf(format, args...)})
	}
	switch {
	case !hasDesc:
		warn("description is required")
	case len(desc) > maxDescriptionLength:
		warn("description exceeds %d characters (%d)", maxDescriptionLength, len(desc))
	}
	dir := filepath.Dir(path)
	name := fm["name"]
	if name == "" {
		name = filepath.Base(dir)
	}
	if len(name) > maxNameLength {
		warn("name exceeds %d characters (%d)", maxNameLength, len(name))
	}
	if !validName.MatchString(name) {
		warn("name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		warn("name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		warn("name must not contain consecutive hyphens")
	}
	if !hasDesc {
		for i := range warns {
			warns[i].Skipped = true
		}
		return nil, warns // without a description there is nothing to show the model
	}
	return &Skill{
		Name: name, Description: desc, FilePath: path, BaseDir: dir,
		DisableModelInvocation: fm["disable-model-invocation"] == "true",
	}, warns
}

// FormatForPrompt is pi's <available_skills> block, in its bash variant: the
// model loads a skill's file with the shell tool. It is empty without
// visible skills, so prompts of users who have none stay unchanged. tool is
// the name of the shell tool.
func FormatForPrompt(skills []Skill, tool string) string {
	var b strings.Builder
	n := 0
	for _, s := range skills {
		if s.DisableModelInvocation {
			continue
		}
		if n == 0 {
			fmt.Fprintf(&b, "\nThe following skills provide specialized instructions for specific tasks.\n"+
				"Use %s to load a skill's file when the task matches its description.\n"+
				"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.\n\n"+
				"<available_skills>\n", tool)
		}
		n++
		fmt.Fprintf(&b, "  <skill>\n    <name>%s</name>\n    <description>%s</description>\n    <location>%s</location>\n  </skill>\n",
			escapeXML(s.Name), escapeXML(s.Description), escapeXML(s.FilePath))
	}
	if n > 0 {
		b.WriteString("</available_skills>\n")
	}
	return b.String()
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func escapeXML(s string) string { return xmlEscaper.Replace(s) }

// Expand turns "/skill:name args" into the skill's instructions followed by
// the args, as pi does. ok is false when text is not a /skill: command or the
// skill is unknown (pi passes those through unchanged).
func Expand(skills []Skill, text string) (expanded string, ok bool, err error) {
	rest, found := strings.CutPrefix(text, "/skill:")
	if !found {
		return text, false, nil
	}
	name, args := rest, ""
	if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
		name, args = rest[:i], strings.TrimSpace(rest[i:])
	}
	for _, s := range skills {
		if s.Name != name {
			continue
		}
		data, err := os.ReadFile(s.FilePath)
		if err != nil {
			return text, false, err
		}
		_, body, _ := ParseFrontmatter(string(data))
		block := fmt.Sprintf("<skill name=\"%s\" location=\"%s\">\nReferences are relative to %s.\n\n%s\n</skill>", s.Name, s.FilePath, s.BaseDir, strings.TrimSpace(body))
		if args != "" {
			block += "\n\n" + args
		}
		return block, true, nil
	}
	return text, false, nil
}
