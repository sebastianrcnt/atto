package agent

import (
	"context"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/outputs"
	"github.com/sebastianrcnt/atto/session"
)

func TestBashExecutionText(t *testing.T) {
	for _, c := range []struct {
		in   session.BashExec
		want string
	}{
		{session.BashExec{Command: "ls", Output: "a\nb"}, "Ran `ls`\n```\na\nb\n```"},
		{session.BashExec{Command: "true"}, "Ran `true`\n(no output)"},
		{session.BashExec{Command: "false", ExitCode: 1}, "Ran `false`\n(no output)\n\nCommand exited with code 1"},
		{session.BashExec{Command: "sleep 9", Output: "x", Cancelled: true, ExitCode: -1}, "Ran `sleep 9`\n```\nx\n```\n\n(command cancelled)"},
		{session.BashExec{Command: "seq", Output: "1", Truncated: true, FullOutputPath: "/tmp/f.log"}, "Ran `seq`\n```\n1\n```\n\n[Output truncated. Full output: /tmp/f.log]"},
	} {
		if got := BashExecutionText(c.in); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestAddShellRecordsAndRestores(t *testing.T) {
	ag := New(config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}, "", t.TempDir())
	var rec []session.Entry
	ag.Record = func(e session.Entry) { rec = append(rec, e) }
	in := session.BashExec{Command: "echo a", Output: "a"}
	hidden := session.BashExec{Command: "echo b", Output: "b", Exclude: true}
	ag.AddShell(in)
	ag.AddShell(hidden)
	if len(rec) != 2 || rec[0].Type != session.TypeBashExecution || rec[1].Bash.Command != "echo b" {
		t.Fatalf("recorded %+v", rec)
	}
	live := ag.Messages()
	if len(live) != 1 || live[0].Content != BashExecutionText(in) {
		t.Fatalf("context %+v", live)
	}
	ag.Restore(rec)
	if got := ag.Messages(); !reflect.DeepEqual(got, live) {
		t.Fatalf("restored %+v, live %+v", got, live)
	}
	if !HasBranchContent(rec[:1]) || HasBranchContent(rec[1:]) {
		t.Fatal("only a command the model sees is branch content")
	}
}

// skipOnWindows skips tests written in bash syntax (seq, ;).
func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash syntax")
	}
}

func TestRunUserShell(t *testing.T) {
	skipOnWindows(t)
	ag := New(config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}, "", t.TempDir())
	var streamed strings.Builder
	x := ag.RunUserShell(context.Background(), "echo hi; exit 4", true, func(s string) { streamed.WriteString(s) })
	if x.Output != "hi" || x.ExitCode != 4 || !x.Exclude || x.Cancelled || streamed.String() != "hi\n" {
		t.Fatalf("%+v streamed %q", x, streamed.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if x := ag.RunUserShell(ctx, "sleep 30", false, nil); !x.Cancelled {
		t.Fatalf("%+v", x)
	}
}

func TestRunUserShellCutsLongOutput(t *testing.T) {
	skipOnWindows(t)
	ag := New(config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}, "", t.TempDir())
	x := ag.RunUserShell(context.Background(), "seq 1 20000", false, nil)
	if !x.Truncated || x.FullOutputPath == "" || len(x.Output) > int(maxOutputBytes.Load())+200 {
		t.Fatalf("truncated=%v path=%q len=%d", x.Truncated, x.FullOutputPath, len(x.Output))
	}
	defer os.Remove(x.FullOutputPath)
	r, err := outputs.Open(x.FullOutputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	full, err := io.ReadAll(r)
	if err != nil || !strings.HasPrefix(string(full), "1\n2\n") || !strings.Contains(string(full), "\n20000") {
		t.Fatalf("full output file: %v", err)
	}
	if !strings.Contains(BashExecutionText(x), "[Output truncated. Full output: "+x.FullOutputPath+" (zstd; read it with: atto output "+x.FullOutputPath+" ") {
		t.Fatal("no truncation note for the model")
	}
}
