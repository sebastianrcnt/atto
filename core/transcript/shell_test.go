package transcript

import (
	"reflect"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// A command the user ran looks the same live and replayed.
func TestShellLiveAndReplayAreTheSame(t *testing.T) {
	x := session.BashExec{Command: "make", Output: "ok\ndone", ExitCode: 2, Exclude: true, DurationMs: 1500,
		Truncated: true, FullOutputPath: "/tmp/f.log"}
	var live Builder
	live.Event(Input{Text: "hi"})
	live.Event(ShellStart{Command: x.Command, Exclude: true})
	live.Event(ShellOutput{Chunk: "ok\r\n"})
	live.Event(ShellOutput{Chunk: "done\r\n"})
	live.Event(ShellEnd{Exec: x})

	got := FromEntries("", []session.Entry{
		{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "hi"}},
		{Type: session.TypeBashExecution, Bash: &x},
	})
	want := live.Items()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed %+v\nlive     %+v", got, want)
	}
	it := got[1]
	if it.Kind != Shell || !it.Excluded || it.Status != Failed || it.Output != "ok\ndone" || !it.Truncated || it.FullOutput != "/tmp/f.log" {
		t.Fatalf("%+v", it)
	}
}

// A command still running when the run is cut short ends failed.
func TestShellInterrupted(t *testing.T) {
	var b Builder
	b.Event(ShellStart{Command: "sleep 9"})
	b.End()
	if it := b.Items()[0]; it.Status != Failed {
		t.Fatalf("%+v", it)
	}
}

func TestShellOutlivesModelTurn(t *testing.T) {
	var b Builder
	b.Event(ShellStart{Command: "make"})
	b.Event(ShellOutput{Chunk: "first\n"})
	b.EndTurn()
	if it := b.Items()[0]; it.Status != InProgress {
		t.Fatalf("shell closed with turn: %+v", it)
	}
	b.Event(ShellOutput{Chunk: "last\n"})
	if it := b.Items()[0]; it.Output != "first\nlast\n" {
		t.Fatalf("lost late output: %+v", it)
	}
	b.Event(ShellEnd{Exec: session.BashExec{Command: "make", Output: "first\nlast\n"}})
	if it := b.Items()[0]; it.Status != Completed || it.Result == nil || it.Result.Failed() {
		t.Fatalf("lost shell completion: %+v", it)
	}
}
