package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"atto2/kernel"
	"atto2/model"
)

// Header supplies the input and grants needed to start a fresh machine.
type Header struct {
	Input        string   `json:"input"`
	Grant        []string `json:"grant"`
	Directory    string   `json:"directory"`
	Instructions string   `json:"instructions"`
	MaxSteps     int      `json:"max_steps"`
}

type recordedTool struct {
	Call   model.ToolCall `json:"call"`
	Code   string         `json:"code"`
	Output string         `json:"output"`
}

type recordedStep struct {
	Number   int            `json:"number"`
	Reply    model.Message  `json:"reply"`
	Usage    model.Usage    `json:"usage"`
	Tools    []recordedTool `json:"tools"`
	Syscalls []kernel.Entry `json:"syscalls"`
}

type final struct {
	Report string `json:"report"`
	Error  string `json:"error,omitempty"`
}

type record struct {
	Header *Header       `json:"header,omitempty"`
	Step   *recordedStep `json:"step,omitempty"`
	Final  *final        `json:"final,omitempty"`
}

// Journal records or verifies one life; it owns no machine or global state.
type Journal struct {
	Header  Header
	encoder *json.Encoder
	steps   []recordedStep
	final   final
	current recordedStep
	next    int
	err     error
}

func Record(w io.Writer, h Header) (*Journal, error) {
	j := &Journal{Header: h, encoder: json.NewEncoder(w)}
	return j, j.encoder.Encode(record{Header: &h})
}

func Replay(r io.Reader) (*Journal, error) {
	d := json.NewDecoder(r)
	var first record
	if err := d.Decode(&first); err != nil {
		return nil, err
	}
	if first.Header == nil {
		return nil, fmt.Errorf("replay: missing header")
	}
	j := &Journal{Header: *first.Header}
	for {
		var item record
		if err := d.Decode(&item); err != nil {
			return nil, fmt.Errorf("replay: %w", err)
		}
		if item.Final != nil {
			j.final = *item.Final
			break
		}
		if item.Step == nil || item.Step.Number != len(j.steps)+1 {
			return nil, fmt.Errorf("replay: invalid step")
		}
		j.steps = append(j.steps, *item.Step)
	}
	var extra record
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("replay: trailing record")
	}
	return j, nil
}

func (j *Journal) Complete(context.Context, []model.Message, []model.Tool) (model.Message, model.Usage, error) {
	if j.next >= len(j.steps) {
		return model.Message{}, model.Usage{}, fmt.Errorf("replay: no reply for step %d", j.next+1)
	}
	s := j.steps[j.next]
	j.next++
	return s.Reply, s.Usage, nil
}

func (j *Journal) Begin(number int, reply model.Message, usage model.Usage) {
	j.current = recordedStep{Number: number, Reply: reply, Usage: usage}
}

func (j *Journal) Tool(call model.ToolCall, code, out string) {
	tool := recordedTool{call, code, out}
	if j.encoder == nil && j.err == nil {
		want := j.expected().Tools
		i := len(j.current.Tools)
		if i >= len(want) {
			j.mismatch("tool", nil, tool)
		} else if !equal(want[i], tool) {
			j.mismatch("machine output", want[i], tool)
		}
	}
	j.current.Tools = append(j.current.Tools, tool)
}

func (j *Journal) expected() recordedStep { return j.steps[j.current.Number-1] }

func (j *Journal) Exchange(entry kernel.Entry, invoke func() kernel.Entry) kernel.Entry {
	if j.encoder != nil {
		entry = invoke()
	} else {
		entry = j.replaySyscall(entry, invoke)
	}
	j.current.Syscalls = append(j.current.Syscalls, entry)
	return entry
}

func (j *Journal) replaySyscall(entry kernel.Entry, invoke func() kernel.Entry) kernel.Entry {
	i := len(j.current.Syscalls)
	want := j.expected().Syscalls
	if i >= len(want) || entry.Name != want[i].Name || !equal(entry.Args, want[i].Args) || (entry.Error != "" && entry.Error != want[i].Error) {
		j.mismatch("syscall", want, entry)
		entry.Error = j.err.Error()
		return entry
	}
	recorded := want[i]
	// Exit has machine lifecycle effects, not world effects. Validate it afresh.
	if entry.Name == "exit" {
		got := invoke()
		got.Time = recorded.Time
		if !equal(recorded, got) {
			j.mismatch("exit", recorded, got)
		}
	}
	return recorded
}

func (j *Journal) EndStep() error {
	if j.encoder != nil {
		return j.encoder.Encode(record{Step: &j.current})
	}
	if j.err == nil && !equal(j.expected(), j.current) {
		j.mismatch("step", j.expected(), j.current)
	}
	return j.err
}

func (j *Journal) Finish(report string, runErr error) error {
	got := final{Report: report}
	if runErr != nil {
		got.Error = runErr.Error()
	}
	if j.encoder != nil {
		return j.encoder.Encode(record{Final: &got})
	}
	if j.err != nil {
		return j.err
	}
	if j.current.Number != len(j.steps) {
		j.mismatch("step count", len(j.steps), j.current.Number)
	}
	if !equal(j.final, got) {
		j.mismatch("final report", j.final, got)
	}
	return j.err
}

func equal(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func (j *Journal) mismatch(kind string, want, got any) {
	if j.err != nil {
		return
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	j.err = fmt.Errorf("replay step %d %s: recorded %s; actual %s", j.current.Number, kind, a, b)
}
