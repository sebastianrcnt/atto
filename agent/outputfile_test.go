package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/outputs"
	"github.com/sebastianrcnt/atto/session"
)

// testLimits keep the tests independent of the disk they run on.
var testLimits = outputs.Limits{MinFree: -1}

// legacyCut is cutMiddle as it was before output went to files as it came:
// the reference for what the model is shown.
func legacyCut(s string, limit int) (body, note string, cut bool) {
	if len(s) <= limit {
		return s, "", false
	}
	half := limit / 2
	head := s[:half]
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i]
	}
	tail := s[len(s)-half:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	total := strings.Count(s, "\n") + 1
	omitted := total - (strings.Count(head, "\n") + 1) - (strings.Count(tail, "\n") + 1)
	note = fmt.Sprintf("[output truncated: %d lines, ~%d tokens; showing the start and the end", total, len(s)/4)
	return fmt.Sprintf("%s\n[… %d lines omitted …]\n%s", head, max(omitted, 0), tail), note, true
}

// content makes about n bytes of output in a style.
func content(style string, n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		switch style {
		case "crlf":
			fmt.Fprintf(&b, "row %d %s\r\n", i, strings.Repeat("c", i%53))
		case "long":
			fmt.Fprintf(&b, "%s\n", strings.Repeat("L", 700+i%300))
		default:
			fmt.Fprintf(&b, "line %d %s\n", i, strings.Repeat("x", i%41))
		}
	}
	s := b.String()[:n]
	switch style {
	case "blanks": // PowerShell pads tables
		s += "\n   \r\n\t\n\n"
	case "nonl": // no final newline
		s = strings.TrimRight(s, "\n")
	case "mixed":
		s = strings.ReplaceAll(s, "\n", "\r\n") + "tail\r"
	}
	return s
}

func stream(s string, chunk int) BashResult {
	w := newStreamWriter("sess", "call", nil)
	for rest := s; len(rest) > 0; {
		n := min(chunk, len(rest))
		w.Write([]byte(rest[:n]))
		rest = rest[n:]
	}
	var res BashResult
	w.finish(&res)
	return res
}

func readOutput(t *testing.T, path string) string {
	t.Helper()
	r, err := outputs.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// What the model reads of a streamed output is what cutting the whole text
// gave before, for every size around the budget and styles of output.
func TestStreamedOutputCutsAsBefore(t *testing.T) {
	defer SetToolOutputTokenLimit(0)
	for _, tokens := range []int{0, 100} {
		SetToolOutputTokenLimit(tokens)
		limit := int(maxOutputBytes.Load())
		sizes := []int{0, 1, limit / 2, limit - 1, limit, limit + 1, limit + 30, 2*limit - 1, 2 * limit, 2*limit + 1, 3 * limit, 11 * limit / 2}
		if tokens == 0 {
			sizes = append(sizes, 1<<20, 5<<20)
		}
		for _, style := range []string{"plain", "crlf", "long", "blanks", "nonl", "mixed"} {
			for _, n := range sizes {
				if style == "long" && n == 0 {
					continue
				}
				text := content(style, n)
				for _, chunk := range []int{1 << 20, 32 << 10, 4099} {
					if chunk < 5000 && n > 1<<20 {
						continue
					}
					name := fmt.Sprintf("limit %d %s %d bytes chunks %d", limit, style, n, chunk)
					res := stream(text, chunk)

					tidied := tidy(text)
					wantBody, wantNote, wantCut := legacyCut(tidied, limit)
					want := wantBody
					if wantCut {
						if res.FullOutput == "" {
							t.Fatalf("%s: no file for a cut output (%s)", name, res.NotSaved)
						}
						want = cutMessage(wantNote, wantBody, res.FullOutput, res.FullOutputOmitted, "")
					}
					if got := res.modelOutput(); got != want {
						t.Fatalf("%s:\ngot  %q\nwant %q", name, clip(got), clip(want))
					}
					// The same for the whole result.
					args := BashArgs{}
					if got, want := res.ForModel(args), (BashResult{Output: text}).ForModel(args); !wantCut && got != want {
						t.Fatalf("%s: ForModel %q, want %q", name, clip(got), clip(want))
					}
					// And the file holds the raw output, whole.
					if wantCut {
						if got := readOutput(t, res.FullOutput); got != text {
							t.Fatalf("%s: file differs (%d vs %d bytes)", name, len(got), len(text))
						}
						os.Remove(res.FullOutput)
					}
					if len(text) <= limit && res.FullOutput != "" {
						t.Fatalf("%s: a file for a short output", name)
					}
					if res.Output != strings.ReplaceAll(text, "\r\n", "\n") && len(text) <= 2*limit {
						t.Fatalf("%s: Output differs from the text (%d vs %d bytes)", name, len(res.Output), len(text))
					}
				}
			}
		}
	}
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:150] + " … " + s[len(s)-150:]
	}
	return s
}

// What the user's !commands add after their output is cut like before.
func TestUserShellExtrasCutAsBefore(t *testing.T) {
	defer SetToolOutputTokenLimit(0)
	SetToolOutputTokenLimit(100)
	limit := int(maxOutputBytes.Load())
	extra := "\n[timed out after 30m0s]"
	for _, n := range []int{0, 10, limit - len(extra) - 1, limit - len(extra), limit - len(extra) + 1, limit, 3 * limit} {
		for _, style := range []string{"plain", "blanks", "mixed"} {
			text := content(style, n)
			res := stream(text, 1000)
			v := res.textView().tidy().appendString(extra).trimNL()
			got, _, cut := v.cut(limit)
			want, _, wantCut := legacyCut(strings.TrimRight(tidy(text)+extra, "\n"), limit)
			if got != want || cut != wantCut {
				t.Fatalf("%s %d bytes:\ngot  %q\nwant %q", style, n, clip(got), clip(want))
			}
			if res.FullOutput != "" {
				os.Remove(res.FullOutput)
			}
		}
	}
}

// The tail of a command that moved to the background is what it was.
func TestBackgroundTailAsBefore(t *testing.T) {
	for _, n := range []int{0, 30, 500, 5000, 100000} {
		for _, style := range []string{"plain", "blanks", "mixed"} {
			text := content(style, n)
			res := stream(text, 777)
			res.Job, res.Background, res.Duration = 3, BackgroundUser, 0
			got := res.backgroundForModel(BashArgs{})
			lines := strings.Split(tidy(text), "\n")
			if len(lines) == 1 && lines[0] == "" {
				lines = nil
			}
			var want strings.Builder
			shown := ""
			if len(lines) > tailLines {
				shown = fmt.Sprintf(" (above: the last %d of %d lines so far)", tailLines, len(lines))
				lines = lines[len(lines)-tailLines:]
			}
			for _, l := range lines {
				want.WriteString(l + "\n")
			}
			ref := BashResult{Job: 3, Background: BackgroundUser, Output: "x"}.backgroundForModel(BashArgs{})
			_, rest, _ := strings.Cut(ref, "[the user moved")
			want.WriteString("[the user moved" + strings.Replace(rest, "job 3.", "job 3"+shown+".", 1))
			if got != want.String() {
				t.Fatalf("%s %d bytes:\ngot  %q\nwant %q", style, n, clip(got), clip(want.String()))
			}
			os.Remove(res.FullOutput)
		}
	}
}

// A command that became a job keeps no file: the job log has the output.
func TestJobKeepsNoOutputFile(t *testing.T) {
	w := newStreamWriter("sess-job", "call-job", nil)
	w.Write([]byte(content("plain", 200000)))
	res := BashResult{Job: 5}
	w.finish(&res)
	if res.FullOutput != "" || res.NotSaved != "" {
		t.Fatalf("%+v", res)
	}
	if ents, _ := os.ReadDir(outputs.SessionDir("sess-job")); len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}
}

// A hand-built result still reads as before, and keeps its full text.
func TestHandBuiltResultStillSavesFullText(t *testing.T) {
	text := content("plain", 100000)
	out := BashResult{Output: text}.ForModel(BashArgs{})
	path := between(out, "full output: ", " (zstd")
	if path == "" || readOutput(t, path) != text {
		t.Fatalf("%s", clip(out))
	}
	if !strings.Contains(out, "read it with: atto output "+path+" [-head N|-tail N|-grep RE]") {
		t.Fatal(clip(out))
	}
}

func between(s, a, b string) string {
	_, rest, ok := strings.Cut(s, a)
	if !ok {
		return ""
	}
	mid, _, _ := strings.Cut(rest, b)
	return mid
}

func TestCutExplainsUnsavedOutput(t *testing.T) {
	defer outputs.SetLimits(testLimits)
	outputs.SetLimits(outputs.Limits{MinFree: 1 << 62}) // more than any disk has
	text := content("plain", 100000)
	res := stream(text, 4096)
	if res.FullOutput != "" || res.NotSaved != "low disk space" {
		t.Fatalf("%+v", res)
	}
	out := res.ForModel(BashArgs{})
	if !strings.Contains(out, "; the full output was not saved: low disk space]\n") || strings.Contains(out, "full output:") {
		t.Fatal(clip(out))
	}
	want, _, _ := legacyCut(text, int(maxOutputBytes.Load()))
	if _, body, _ := strings.Cut(out, "]\n"); body != want {
		t.Fatal("the cut text differs")
	}
}

func TestCutSaysWhatTheFileOmits(t *testing.T) {
	defer outputs.SetLimits(testLimits)
	outputs.SetLimits(outputs.Limits{FileHead: 1 << 10, FileTail: 1 << 10, MinFree: -1})
	text := content("plain", 100000)
	res := stream(text, 4096)
	if res.FullOutputOmitted != int64(len(text)-2048) {
		t.Fatalf("omitted %d", res.FullOutputOmitted)
	}
	out := res.ForModel(BashArgs{})
	if !strings.Contains(out, fmt.Sprintf("; the file omits %d bytes from its middle]", len(text)-2048)) {
		t.Fatal(clip(out))
	}
	if got := readOutput(t, res.FullOutput); len(got) <= 2048 || !strings.HasPrefix(got, text[:1<<10]) || !strings.HasSuffix(got, text[len(text)-1<<10:]) {
		t.Fatal("file contents")
	}
}

func TestRunBashSavesBigOutputToSessionFile(t *testing.T) {
	skipOnWindows(t)
	env := []string{"ATTO_SESSION_ID=sess-big"}
	args := BashArgs{Description: "seq", Command: "seq 1 300000", callID: "call_42"}
	res := RunBash(context.Background(), t.TempDir(), env, args, nil)
	want := filepath.Join(outputs.SessionDir("sess-big"), "call_42.log.zst")
	if res.FullOutput != want {
		t.Fatalf("path %q, want %q", res.FullOutput, want)
	}
	var full strings.Builder
	for i := 1; i <= 300000; i++ {
		fmt.Fprintf(&full, "%d\n", i)
	}
	if readOutput(t, want) != full.String() {
		t.Fatal("the file differs from the output")
	}
	out := res.ForModel(args)
	body, note, _ := legacyCut(tidy(full.String()), int(maxOutputBytes.Load()))
	if out != cutMessage(note, body, want, 0, "") {
		t.Fatal(clip(out))
	}
	if !strings.HasPrefix(out, "[output truncated: 300000 lines, ~") {
		t.Fatal(clip(out))
	}
	// A short one leaves no file.
	res = RunBash(context.Background(), t.TempDir(), env, BashArgs{Command: "echo hi", callID: "c2"}, nil)
	if res.FullOutput != "" || res.Output != "hi\n" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(outputs.SessionDir("sess-big"), "c2.log.zst")); err == nil {
		t.Fatal("file for a short output")
	}
}

func TestRunDirectSavesBigOutputToo(t *testing.T) {
	skipOnWindows(t)
	old := ShellHost
	ShellHost = false
	defer func() { ShellHost = old }()
	res := RunBash(context.Background(), t.TempDir(), nil, BashArgs{Command: "seq 1 100000", callID: "direct"}, nil)
	if filepath.Base(res.FullOutput) != "direct.log.zst" || filepath.Base(filepath.Dir(res.FullOutput)) != "_nosession" {
		t.Fatalf("%q", res.FullOutput)
	}
	if got := readOutput(t, res.FullOutput); !strings.HasPrefix(got, "1\n2\n") || !strings.HasSuffix(got, "\n100000\n") {
		t.Fatal("file contents")
	}
}

// A command that wrote more than the file keeps is described honestly.
func TestHugeCommandOutput(t *testing.T) {
	skipOnWindows(t)
	if testing.Short() || raceEnabled {
		t.Skip("heavy")
	}
	// 150 MB of text from the shell itself.
	args := BashArgs{Command: "yes 'a fairly ordinary line of build output, with some words in it' 2>/dev/null | head -c 150000000", callID: "huge", Timeout: 30}
	runtime.GC()
	var base, ms runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak uint64
	calls := 0
	res := RunBash(context.Background(), t.TempDir(), []string{"ATTO_SESSION_ID=sess-huge"}, args, func(string) {
		if calls++; calls%300 == 0 {
			runtime.GC() // what is live, not garbage waiting to be collected
			runtime.ReadMemStats(&ms)
			peak = max(peak, ms.HeapAlloc)
		}
	})
	if grown := int64(peak) - int64(base.HeapAlloc); calls < 1000 || grown > 2<<20 {
		t.Errorf("150 MB through %d chunks grew the heap by %d bytes", calls, grown)
	} else {
		t.Logf("150 MB through %d chunks: live heap grew by %d KiB at most", calls, grown/1024)
	}
	if res.ExitCode != 0 || res.TimedOut || res.Err != nil {
		t.Fatalf("%+v", res)
	}
	if want := int64(150_000_000 - 64<<20); res.FullOutputOmitted != want {
		t.Fatalf("omitted %d, want %d", res.FullOutputOmitted, want)
	}
	out := res.ForModel(args)
	if !strings.Contains(out, "~37500000 tokens") || !strings.Contains(out, fmt.Sprintf("; the file omits %d bytes from its middle]", res.FullOutputOmitted)) {
		t.Fatal(clip(out))
	}
	r, err := outputs.Open(res.FullOutput)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n, err := io.Copy(io.Discard, r)
	if err != nil || n < 64<<20 || n > 64<<20+200 {
		t.Fatalf("file has %d bytes: %v", n, err)
	}
	if info, _ := os.Stat(res.FullOutput); info.Size() > 8<<20 {
		t.Logf("compressed file: %d bytes", info.Size())
	}
}

func TestRunUserShellOutputFile(t *testing.T) {
	skipOnWindows(t)
	ag := New(config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}, "", t.TempDir())
	x := ag.RunUserShell(context.Background(), "seq 1 50000; echo done >&2", false, nil)
	if !x.Truncated || !strings.HasSuffix(x.FullOutputPath, ".log.zst") {
		t.Fatalf("%+v", x)
	}
	if got := readOutput(t, x.FullOutputPath); !strings.HasPrefix(got, "1\n2\n") || !strings.HasSuffix(got, "50000\ndone\n") {
		t.Fatal("file contents")
	}
	want, _, _ := legacyCut(strings.TrimRight(got50000(), "\n"), int(maxOutputBytes.Load()))
	if x.Output != want {
		t.Fatalf("output differs: %q", clip(x.Output))
	}
	// Output that was not cut leaves no file.
	y := ag.RunUserShell(context.Background(), "seq 1 20", false, nil)
	if y.Truncated || y.FullOutputPath != "" {
		t.Fatalf("%+v", y)
	}
	text := BashExecutionText(session.BashExec{Command: "x", Output: "o", Truncated: true, FullOutputPath: "/p/q.log.zst"})
	if !strings.HasSuffix(text, "[Output truncated. Full output: /p/q.log.zst (zstd; read it with: atto output /p/q.log.zst [-head N|-tail N|-grep RE])]") {
		t.Fatal(text)
	}
}

func got50000() string {
	var b bytes.Buffer
	for i := 1; i <= 50000; i++ {
		fmt.Fprintf(&b, "%d\n", i)
	}
	return b.String() + "done"
}

// Output that tidy() shrinks below the budget does not keep its file.
func TestTidyShrunkOutputKeepsNoFile(t *testing.T) {
	limit := int(maxOutputBytes.Load())
	text := content("plain", limit-10) + strings.Repeat("   \n", 20)
	res := stream(text, 1000)
	if res.FullOutput == "" {
		t.Fatal("the raw output outgrew the budget: there is a file for now")
	}
	if out := res.ForModel(BashArgs{}); strings.Contains(out, "truncated") {
		t.Fatal(clip(out))
	}
	if _, err := os.Stat(res.FullOutput); err == nil {
		t.Fatal("the file of an output that was not cut should be gone")
	}
}
