// Package kernel owns an agent's granted, checked and logged access to the world.
package kernel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Field struct {
	Name, Type string
	Required   bool
}
type Args map[string]any
type Syscall struct {
	Name, Description string
	Fields            []Field
	Call              func(context.Context, Args) (any, error)
}
type Entry struct {
	Time   time.Time `json:"time"`
	Name   string    `json:"name"`
	Args   Args      `json:"args"`
	Result any       `json:"result,omitempty"`
	Error  string    `json:"error,omitempty"`
}

// Kernel belongs to one agent, and is used by its single-threaded VM.
type Kernel struct {
	registry             map[string]Syscall
	grant                map[string]bool
	Log                  []Entry
	Exchange             func(Entry, func() Entry) Entry
	PureRuns, ImpureRuns int
	Exited               bool
	Report               string
	ctx                  context.Context
	cancel               context.CancelFunc
}

func New() *Kernel { return &Kernel{registry: map[string]Syscall{}, grant: map[string]bool{}} }

func (k *Kernel) Register(s Syscall) error {
	if s.Name == "" || s.Call == nil {
		return fmt.Errorf("syscall needs a name and function")
	}
	if _, ok := k.registry[s.Name]; ok {
		return fmt.Errorf("sys.%s already registered", s.Name)
	}
	seen := map[string]bool{}
	for _, f := range s.Fields {
		if f.Name == "" || seen[f.Name] {
			return fmt.Errorf("sys.%s: duplicate or empty field", s.Name)
		}
		if f.Type != "string" && f.Type != "number" && f.Type != "boolean" {
			return fmt.Errorf("sys.%s.%s: unsupported type %s", s.Name, f.Name, f.Type)
		}
		seen[f.Name] = true
	}
	s.Fields = append([]Field(nil), s.Fields...)
	k.registry[s.Name] = s
	return nil
}

func (k *Kernel) Grant(names ...string) error {
	for _, n := range names {
		if _, ok := k.registry[n]; !ok {
			return fmt.Errorf("unknown syscall sys.%s", n)
		}
	}
	for _, n := range names {
		k.grant[n] = true
	}
	return nil
}

// Form is the canonical table form used in instructions and errors.
func Form(s Syscall) string {
	fields := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		fields[i] = f.Name + " = " + f.Type
		if !f.Required {
			fields[i] += " (optional)"
		}
	}
	if len(fields) == 0 {
		return "sys." + s.Name + "()"
	}
	return "sys." + s.Name + "{" + strings.Join(fields, ", ") + "}"
}

func (k *Kernel) Instructions() string {
	var names []string
	for n := range k.grant {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		s := k.registry[n]
		fmt.Fprintf(&b, "%s -> %s\n", Form(s), s.Description)
	}
	return b.String()
}

func (k *Kernel) BeginRun(ctx context.Context, cancel context.CancelFunc) {
	k.ctx, k.cancel = ctx, cancel
}
func (k *Kernel) EndRun(pure bool) {
	if pure {
		k.PureRuns++
	} else {
		k.ImpureRuns++
	}
	k.ctx, k.cancel = nil, nil
}

func (k *Kernel) Granted(name string) bool { return k.grant[name] }
