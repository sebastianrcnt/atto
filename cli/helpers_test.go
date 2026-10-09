package cli

import (
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/session"
)

func TestParseInterleaved(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	one := fs.String("one", "", "")
	two := fs.Bool("two", false, "")
	words, err := parseInterleaved(fs, []string{"a", "-one", "value", "b", "-two", "c"})
	if err != nil || !reflect.DeepEqual(words, []string{"a", "b", "c"}) || *one != "value" || !*two {
		t.Fatalf("words %v, one %q, two %v, err %v", words, *one, *two, err)
	}
	if _, err := parseInterleaved(fs, []string{"a", "-missing"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
}

func TestApprovalCommandsRequireUserTerminal(t *testing.T) {
	for _, env := range []string{config.EnvAgent, "ATTO_SESSION_ID"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(config.EnvAgent, "")
			t.Setenv("ATTO_SESSION_ID", "")
			t.Setenv(env, "set")
			for name, run := range map[string]func() error{
				"trust":      func() error { return RunTrust([]string{"approve", "all"}, io.Discard) },
				"mcp":        func() error { return mcpApprove([]string{"one"}, io.Discard) },
				"extensions": func() error { return RunExtensions([]string{"approve", "one"}, io.Discard) },
			} {
				if name == "extensions" && !extensions.Supported {
					continue
				}
				if err := run(); err == nil || !strings.Contains(err.Error(), "not from an agent's shell") {
					t.Errorf("%s: %v", name, err)
				}
			}
		})
	}
}

func TestFirstNonEmptyLine(t *testing.T) {
	if got := firstNonEmptyLine("\n  \n first  \nsecond"); got != "first" {
		t.Fatal(got)
	}
}

func TestSessionModelReadsLatestChoicesAcrossBranches(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	w := session.New(t.TempDir())
	w.Append(session.Entry{Type: session.TypeModel, Provider: "p", Model: "old"})
	root := w.Leaf()
	w.Append(session.Entry{Type: session.TypeModel, Provider: "p", Model: "latest"})
	w.Append(session.Entry{Type: session.TypeEffort, Effort: "high"})
	w.Branch(root)
	w.Append(session.Entry{Type: session.TypeCompaction})
	w.Close()
	if model, effort := sessionModel(w.ID); model != "p/latest" || effort != "high" {
		t.Fatalf("%s %s", model, effort)
	}
}
