package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
)

func printEvents(p *printer) {
	for _, ev := range []any{
		agent.ReasoningDelta{Text: "think"},
		agent.TextDelta{Text: "Hi"},
		agent.StepEnd{Usage: provider.Usage{PromptTokens: 10, CompletionTokens: 2}},
		agent.ToolStart{ID: "c1", Args: agent.BashArgs{Description: "List", Command: "ls"}},
		agent.ToolOutput{ID: "c1", Chunk: "a\n"},
		agent.ToolEnd{ID: "c1", Result: agent.BashResult{Output: "a\n", ExitCode: 2, Duration: 5 * time.Millisecond}, Text: "a\n[exit code 2]"},
		agent.HookNotice{Event: "PostToolUse", Message: "note"},
		agent.CompactStart{},
		agent.CompactEnd{Notes: "n", Before: 100, After: 10},
		agent.TextDelta{Text: "Bye"},
		agent.StepEnd{Usage: provider.Usage{PromptTokens: 20, CachedTokens: 10, CompletionTokens: 1}},
	} {
		p.event(ev)
	}
	p.tr.End()
	p.flushStep()
}

func TestPrinterStreamJSON(t *testing.T) {
	var out bytes.Buffer
	res := printResult{}
	p := &printer{format: "stream-json", out: &out, errOut: &out, res: &res}
	printEvents(p)
	want := `{"reasoning":"think","text":"Hi","type":"assistant"}
{"command":"ls","description":"List","id":"c1","type":"tool_use"}
{"description":"List","duration_ms":5,"exit_code":2,"id":"c1","output":"a\n[exit code 2]","timed_out":false,"type":"tool_result"}
{"blocked":false,"event":"PostToolUse","message":"note","type":"hook"}
{"tokens_after":10,"tokens_before":100,"type":"compaction"}
{"reasoning":"","text":"Bye","type":"assistant"}
`
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
	if res.NumSteps != 2 || res.Usage.InputTokens != 30 || res.Usage.CachedInputTokens != 10 || p.lastText != "Bye" {
		t.Fatalf("result %+v, last %q", res, p.lastText)
	}
}

func TestPrinterText(t *testing.T) {
	var out, errOut bytes.Buffer
	p := &printer{verbose: true, out: &out, errOut: &errOut, res: &printResult{}}
	printEvents(p)
	if out.String() != "HiBye" {
		t.Fatalf("stdout %q", out.String())
	}
	if e := errOut.String(); e != "\n● List  $ ls\n  └ exit 2 · 5ms\n⚑ PostToolUse: note\n" {
		t.Fatalf("stderr %q", e)
	}
	if strings.Contains(out.String(), "{") {
		t.Fatal("text mode writes no JSON")
	}
}

func TestRunPrintMissingTierNotice(t *testing.T) {
	goalServer(t, 0, 0, "")
	t.Chdir(t.TempDir())
	data, err := os.ReadFile(config.ModelsPath())
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"contextWindow":10000`), []byte(`"contextWindow":10000,"cost":{"input":1}`), 1)
	if err := os.WriteFile(config.ModelsPath(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	quiet(t)
	errOut, err := os.CreateTemp(t.TempDir(), "err")
	if err != nil {
		t.Fatal(err)
	}
	defer errOut.Close()
	os.Stderr = errOut
	if err := RunPrint(PrintOptions{Prompt: "hello", NoSave: true}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(errOut.Name())
	if !strings.Contains(string(data), "No price-tier cap for fake/m:") || !strings.Contains(string(data), "catalog could not be loaded") {
		t.Fatalf("stderr %q", data)
	}
}
