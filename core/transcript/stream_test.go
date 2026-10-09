package transcript

import (
	"reflect"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func TestStreamReplayMatchesAndBoundsTail(t *testing.T) {
	entries := []session.Entry{
		{Type: session.TypeMessage, ID: "u", Message: &provider.Message{Role: "user", Content: "question"}},
		{Type: session.TypeMessage, ID: "a", Message: &provider.Message{Role: "assistant", Content: "answer", ReasoningContent: "thinking", ToolCalls: []provider.ToolCall{{ID: "call", Function: provider.FunctionCall{Name: "bash", Arguments: `{"command":"echo yes"}`}}}}},
		{Type: session.TypeMessage, ID: "t", Message: &provider.Message{Role: "tool", ToolCallID: "call", Content: "yes"}},
		{Type: session.TypeCompaction, Notes: "summary"},
		{Type: session.TypeMessage, ID: "end", Message: &provider.Message{Role: "assistant", Content: "last answer"}},
	}
	full := Builder{IDPrefix: "thread-i"}
	full.Replay(entries)
	stream := Builder{IDPrefix: "thread-i"}
	for _, e := range entries {
		stream.ReplayEntry(e)
	}
	stream.FinishReplay()
	if !reflect.DeepEqual(stream.Items(), full.Items()) {
		t.Fatal("stream replay differs, including tool result or IDs")
	}
	bounded := Builder{IDPrefix: "thread-i", MaxItems: 2}
	for _, e := range entries {
		bounded.ReplayEntry(e)
	}
	bounded.FinishReplay()
	want := full.Items()
	if !reflect.DeepEqual(bounded.Items(), want[len(want)-2:]) {
		t.Fatal("bounded tail differs")
	}
	bounded.Reset()
	if bounded.MaxItems != 2 {
		t.Fatal("reset lost retention cap")
	}
}
