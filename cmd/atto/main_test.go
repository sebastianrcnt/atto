package main

import (
	"flag"
	"strings"
	"testing"
)

func TestUnknownCommand(t *testing.T) {
	known := subcommandNames()
	for _, c := range []struct {
		args []string
		msg  string
		ok   bool
	}{
		{[]string{"upgrade"}, `atto: unknown command "upgrade". Did you mean "update"?`, true},
		{[]string{"histroy"}, `atto: unknown command "histroy". Did you mean "history"?`, true},
		{[]string{"xyz"}, `atto: unknown command "xyz" (see atto -h)`, true},
		{[]string{"_supervis"}, `atto: unknown command "_supervis" (see atto -h)`, true}, // hidden never suggested
		{[]string{"fix the build"}, "", false},
		{[]string{"fix", "the", "build"}, "", false},
		{[]string{"fix", "build"}, "", false},
		{nil, "", false},
	} {
		msg, ok := unknownCommand(c.args, known)
		if msg != c.msg || ok != c.ok {
			t.Errorf("%q: got %q, %v", c.args, msg, ok)
		}
	}
}

func TestInitialPrompt(t *testing.T) {
	for _, args := range [][]string{{"fix the build"}, {"fix", "the", "build"}} {
		if got := initialPrompt(args); got != "fix the build" {
			t.Errorf("%q -> %q", args, got)
		}
	}
	if initialPrompt(nil) != "" {
		t.Error("no words, no prompt")
	}
}

func TestEditDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		d    int
	}{{"", "abc", 3}, {"abc", "abc", 0}, {"kitten", "sitting", 3}} {
		if got := editDistance(c.a, c.b); got != c.d {
			t.Errorf("%s/%s = %d", c.a, c.b, got)
		}
	}
}

func TestResumeArgs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"atto resume", "atto -resume"},
		{"atto resume ab12", "atto -session ab12"},
		{"atto resume -m x ab12", "atto -m x -session ab12"},
		{"atto resume -m x", "atto -m x -resume"},
	}
	for _, c := range cases {
		got := strings.Join(resumeArgs(strings.Fields(c.in)), " ")
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
	if !nestedRefused["resume"] {
		t.Error("resume starts an agent and must be refused inside one")
	}
}

// The agent may reload its own session and look at what it loaded.
func TestNestedAllowsReloadAndContext(t *testing.T) {
	for _, cmd := range []string{"reload", "context"} {
		if nestedRefused[cmd] {
			t.Errorf("%s must work from the agent's shell", cmd)
		}
		if subcommands()[cmd] == nil {
			t.Errorf("%s is not a subcommand", cmd)
		}
	}
}

func TestImageFlagRepeats(t *testing.T) {
	fs := flag.NewFlagSet("atto", flag.ContinueOnError)
	p := fs.Bool("p", false, "")
	var imgs stringList
	fs.Var(&imgs, "image", "")
	words := parseInterleaved(fs, []string{"-p", "-image", "a.png", "what", "is", "-image=b.jpg", "this"})
	if !*p || strings.Join(imgs, ",") != "a.png,b.jpg" || strings.Join(words, " ") != "what is this" {
		t.Fatalf("p %v, images %q, words %q", *p, imgs, words)
	}
}

// A top-level agent may drive agents from its shell (atto agent
// refuses start/next for an agent itself); plain atto stays refused.
func TestNestedAllowsAgent(t *testing.T) {
	for _, cmd := range []string{"agent", "_agent-turn"} {
		if nestedRefused[cmd] || subcommands()[cmd] == nil {
			t.Errorf("%s must work from the agent's shell", cmd)
		}
	}
	if !nestedRefused[""] {
		t.Error("atto itself must stay refused")
	}
}

// "atto -h" is the only place the subcommands are listed, so a new one that
// is not mentioned there is invisible: every command a user can run must
// appear in the usage text (hidden ones, "_foo", are deliberately not).
func TestUsageListsEverySubcommand(t *testing.T) {
	if !strings.Contains(usage, `atto -p [flags] "prompt"          standalone session, not an agent in a tree`) {
		t.Error("usage does not distinguish print mode from an agent in a tree")
	}
	listed := map[string]bool{}
	for f := range strings.FieldsSeq(strings.ReplaceAll(usage, "|", " ")) {
		f = strings.Trim(f, "[]<>(),.:;?!/…")
		if f != "" {
			listed[strings.ToLower(f)] = true
		}
	}
	for _, name := range subcommandNames() {
		if strings.HasPrefix(name, "_") {
			continue
		}
		if !listed[name] {
			t.Errorf("subcommand %q is not in the usage text:\n%s", name, usage)
		}
	}
}
