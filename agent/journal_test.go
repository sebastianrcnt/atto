package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"atto2/cortex"
	"atto2/kernel"
	"atto2/machine"
	"atto2/model"
)

type fakeModel struct {
	replies []model.Message
	next    int
}

func (f *fakeModel) Complete(context.Context, []model.Message, []model.Tool) (model.Message, model.Usage, error) {
	if f.next >= len(f.replies) {
		return model.Message{}, model.Usage{}, fmt.Errorf("no reply")
	}
	reply := f.replies[f.next]
	f.next++
	return reply, model.Usage{PromptTokens: 123, CompletionTokens: 7}, nil
}

func journalAgent(t *testing.T, j *Journal, client Completer) (*Agent, *bytes.Buffer) {
	t.Helper()
	k, err := kernel.WithGrant(j.Header.Directory, j.Header.Grant...)
	if err != nil {
		t.Fatal(err)
	}
	m := machine.New(k)
	t.Cleanup(m.Close)
	k.Exchange = j.Exchange
	var trace bytes.Buffer
	encoder := json.NewEncoder(&trace)
	a := &Agent{Machine: m, Cortex: cortex.New(cortex.Instructions(k, j.Header.Directory)), Model: client, Journal: j, MaxSteps: j.Header.MaxSteps, Trace: func(e Event) {
		if err := encoder.Encode(e); err != nil {
			t.Error(err)
		}
	}}
	return a, &trace
}

func recordedLife(t *testing.T) ([]byte, []byte, []kernel.Entry) {
	t.Helper()
	var buf bytes.Buffer
	h := Header{Input: "compute", Grant: []string{"now", "exit"}, Directory: "/not/a/directory", MaxSteps: 30}
	j, err := Record(&buf, h)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeModel{replies: []model.Message{
		{Role: "assistant", Content: "stdout"},
		luaReply(`stamp=sys.now(); print(stamp); total=0; for i=1,100 do total=total+i end; print(json.encode({sum=total}))`),
		luaReply(`print(text.trim(" hi ")); sys.exit(tostring(total)..":"..tostring(stamp))`),
	}}
	a, trace := journalAgent(t, j, fake)
	if _, err := a.Run(context.Background(), h.Input); err != nil {
		t.Fatal(err)
	}
	if a.CompletionTokens != 21 {
		t.Fatal(a.CompletionTokens)
	}
	return buf.Bytes(), trace.Bytes(), a.Machine.Kernel.Log
}

func TestRecordReplayTwice(t *testing.T) {
	raw, want, log := recordedLife(t)
	for range 2 {
		j, err := Replay(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		a, trace := journalAgent(t, j, j)
		report, err := a.Run(context.Background(), j.Header.Input)
		if err != nil || report == "" {
			t.Fatalf("%q %v", report, err)
		}
		if !bytes.Equal(trace.Bytes(), want) || !equal(log, a.Machine.Kernel.Log) {
			t.Fatalf("replay differs: %s", trace)
		}
	}
}

func tamper(t *testing.T, raw []byte, change func(*record)) []byte {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	var out bytes.Buffer
	e := json.NewEncoder(&out)
	for d.More() {
		var r record
		if err := d.Decode(&r); err != nil {
			t.Fatal(err)
		}
		change(&r)
		if err := e.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func TestReplayTampering(t *testing.T) {
	raw, _, _ := recordedLife(t)
	for _, c := range []struct {
		name   string
		change func(*record)
		step   string
	}{
		{"now", func(r *record) {
			if r.Step != nil && r.Step.Number == 2 {
				r.Step.Syscalls[0].Result = r.Step.Syscalls[0].Result.(float64) + 1
			}
		}, "step 2 machine output"},
		{"output", func(r *record) {
			if r.Step != nil && r.Step.Number == 2 {
				r.Step.Tools[0].Output = "tampered"
			}
		}, "step 2 machine output"},
		{"report", func(r *record) {
			if r.Final != nil {
				r.Final.Report = "tampered"
			}
		}, "step 3 final report"},
		{"syscall", func(r *record) {
			if r.Step != nil && r.Step.Number == 2 {
				r.Step.Syscalls[0].Name = "bash"
			}
		}, "step 2 syscall"},
	} {
		t.Run(c.name, func(t *testing.T) {
			j, err := Replay(bytes.NewReader(tamper(t, raw, c.change)))
			if err != nil {
				t.Fatal(err)
			}
			a, _ := journalAgent(t, j, j)
			_, err = a.Run(context.Background(), j.Header.Input)
			if err == nil || !strings.Contains(err.Error(), c.step) || !strings.Contains(err.Error(), "recorded") || !strings.Contains(err.Error(), "actual") {
				t.Fatal(err)
			}
		})
	}
}

func TestReplayInvalidRecords(t *testing.T) {
	for _, raw := range []string{"", `{}`, "{\"header\":{}}\n", "{\"header\":{}}\n{\"step\":{\"number\":2}}\n", "{\"header\":{}}\n{\"final\":{}}\n{}"} {
		if _, err := Replay(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestRecordFailuresAndDenials(t *testing.T) {
	for _, replies := range [][]model.Message{
		{luaReply(`return sys.bash("true")`), luaReply(`sys.exit("denied")`)},
		{luaReply(`return sys.missing()`), luaReply(`sys.exit("unknown")`)},
		{{Role: "assistant", Content: "not done"}},
	} {
		var raw bytes.Buffer
		j, err := Record(&raw, Header{Grant: []string{"now", "exit"}, MaxSteps: len(replies)})
		if err != nil {
			t.Fatal(err)
		}
		a, want := journalAgent(t, j, &fakeModel{replies: replies})
		report, runErr := a.Run(context.Background(), "")
		replay, err := Replay(bytes.NewReader(raw.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		b, got := journalAgent(t, replay, replay)
		report2, err2 := b.Run(context.Background(), "")
		if report != report2 || fmt.Sprint(runErr) != fmt.Sprint(err2) || !bytes.Equal(want.Bytes(), got.Bytes()) {
			t.Fatalf("%q %v / %q %v", report, runErr, report2, err2)
		}
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("write failed") }

func TestJournalWriteFailure(t *testing.T) {
	if _, err := Record(failedWriter{}, Header{}); err == nil || err.Error() != "write failed" {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	j, err := Record(&raw, Header{Grant: []string{"now", "exit"}, MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	j.encoder = json.NewEncoder(failedWriter{})
	a, _ := journalAgent(t, j, &fakeModel{replies: []model.Message{luaReply(`sys.exit("done")`)}})
	if _, err := a.Run(context.Background(), ""); err == nil || err.Error() != "write failed" {
		t.Fatal(err)
	}
}
