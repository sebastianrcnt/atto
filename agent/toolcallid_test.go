//go:build !windows

package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/jobs"
)

// Every command the model runs gets the ID of its tool call, next to the
// session ID; the user's own commands get none.
func TestToolCallIDInCommandEnvironment(t *testing.T) {
	t.Setenv(config.EnvToolCallID, "stale") // atto itself started from a model command
	srv, _ := fakeServer(t, toolCall(`echo "call=$ATTO_TOOL_CALL_ID session=$ATTO_SESSION_ID"`), text("done"))
	a := newTestAgent(srv.URL)
	a.SetSession("s1", []string{"ATTO_SESSION_ID=s1", config.EnvAgent + "=1"})
	var out string
	if err := a.Run(context.Background(), "go", func(ev any) {
		if e, ok := ev.(ToolEnd); ok {
			out = e.Text
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "call=c1 session=s1") {
		t.Fatalf("model command: %q", out)
	}
	x := a.RunUserShell(context.Background(), `echo "call=[$ATTO_TOOL_CALL_ID] session=$ATTO_SESSION_ID"`, false, nil)
	if !strings.Contains(x.Output, "call=[] session=s1") {
		t.Fatalf("a command the user ran has no call ID: %q", x.Output)
	}
}

// A command run in the background keeps the ID of the call that started it.
func TestToolCallIDReachesBackgroundCommands(t *testing.T) {
	s, env := bgSession(t)
	args := BashArgs{Description: "bg", Command: `echo "bg=$ATTO_TOOL_CALL_ID"`, Background: true}
	res := RunBash(context.Background(), t.TempDir(), append(env, config.EnvToolCallID+"=call-9"), args, nil)
	if res.Err != nil || res.Job == 0 {
		t.Fatalf("job: %+v", res)
	}
	if _, _, err := jobs.Wait(s, res.Job, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	out, err := jobs.Tail(s, res.Job, 10)
	if err != nil || !strings.Contains(out, "bg=call-9") {
		t.Fatalf("job output %q %v", out, err)
	}
}
