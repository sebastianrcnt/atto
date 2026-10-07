// Package kernel owns an agent's granted, checked and logged access to the world.
package kernel

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
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

// Bind installs closures: aliases still pass through exactly this boundary.
// An ungranted registered syscall remains callable only to return a logged denial.
func (k *Kernel) Bind(L *lua.LState) {
	t := L.NewTable()
	names := make([]string, 0, len(k.registry))
	for name := range k.registry {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := k.registry[name]
		t.RawSetString(name, L.NewFunction(func(L *lua.LState) int {
			entry := Entry{Time: time.Now(), Name: s.Name, Args: snapshotArgs(L, s)}
			index := len(k.Log)
			k.Log = append(k.Log, entry)
			defer func() { k.Log[index] = entry }()
			args, err := arguments(L, s)
			if k.Exited {
				err = fmt.Errorf("agent exited")
			} else if !k.grant[s.Name] {
				err = fmt.Errorf("sys.%s: not granted", s.Name)
			}
			var result any
			if err == nil {
				result, err = s.Call(k.ctx, args)
			}
			entry.Result = result
			if err != nil {
				entry.Error = err.Error()
			}
			if k.Exited {
				k.cancel()
				L.RaiseError("agent exited")
			}
			if err != nil {
				L.RaiseError("%s", err)
				return 0
			}
			L.Push(ToLua(L, result))
			return 1
		}))
	}
	// Unknown names are also checked and logged rather than a nil-function error.
	mt := L.NewTable()
	mt.RawSetString("__index", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(2)
		L.Push(L.NewFunction(func(L *lua.LState) int {
			err := "sys." + name + ": unknown syscall"
			k.Log = append(k.Log, Entry{Time: time.Now(), Name: name, Args: snapshotArgs(L, Syscall{}), Error: err})
			L.RaiseError("%s", err)
			return 0
		}))
		return 1
	}))
	L.SetMetatable(t, mt)
	L.SetGlobal("sys", t)
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

func arguments(L *lua.LState, s Syscall) (Args, error) {
	args := Args{}
	values := map[string]lua.LValue{}
	if t, ok := L.Get(1).(*lua.LTable); ok && L.GetTop() == 1 {
		var bad string
		t.ForEach(func(key, v lua.LValue) {
			name, ok := key.(lua.LString)
			known := false
			for _, f := range s.Fields {
				if f.Name == string(name) && ok {
					known = true
				}
			}
			if !known {
				// Sorted errors keep validation deterministic even though ForEach uses Go maps.
				label := keyLabel(key)
				if bad == "" || label < bad {
					bad = label
				}
			} else {
				values[string(name)] = v
			}
		})
		if bad != "" {
			return args, fmt.Errorf("sys.%s: unknown field %s; use %s", s.Name, bad, Form(s))
		}
	} else {
		if L.GetTop() > len(s.Fields) {
			return args, fmt.Errorf("sys.%s: too many arguments; use %s", s.Name, Form(s))
		}
		for i := 1; i <= L.GetTop(); i++ {
			values[s.Fields[i-1].Name] = L.Get(i)
		}
	}
	for _, f := range s.Fields {
		v := values[f.Name]
		if v == nil || v == lua.LNil {
			if f.Required {
				return args, fmt.Errorf("sys.%s: field %s required (%s); use %s", s.Name, f.Name, f.Type, Form(s))
			}
			continue
		}
		if v.Type().String() != f.Type {
			return args, fmt.Errorf("sys.%s: field %s expected %s; use %s", s.Name, f.Name, f.Type, Form(s))
		}
		switch x := v.(type) {
		case lua.LString:
			args[f.Name] = string(x)
		case lua.LNumber:
			args[f.Name] = float64(x)
		case lua.LBool:
			args[f.Name] = bool(x)
		}
	}
	return args, nil
}

// ToLua converts syscall results (JSON-shaped values) into VM-owned values.
func ToLua(L *lua.LState, v any) lua.LValue {
	switch x := v.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(x)
	case bool:
		return lua.LBool(x)
	case int:
		return lua.LNumber(x)
	case int64:
		return lua.LNumber(x)
	case float64:
		return lua.LNumber(x)
	case map[string]any:
		t := L.NewTable()
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			t.RawSetString(key, ToLua(L, x[key]))
		}
		return t
	case []any:
		t := L.NewTable()
		for _, v := range x {
			t.Append(ToLua(L, v))
		}
		return t
	default:
		panic(fmt.Sprintf("unsupported syscall result %T", v))
	}
}

// Snapshot arguments before validation, so even rejected calls retain what
// was supplied. Valid positional and table forms have identical field names.
func snapshotArgs(L *lua.LState, s Syscall) Args {
	args := Args{}
	if t, ok := L.Get(1).(*lua.LTable); ok && L.GetTop() == 1 {
		t.ForEach(func(k, v lua.LValue) { args[keyLabel(k)] = snapshotValue(v, 0) })
	} else {
		for i := 1; i <= L.GetTop(); i++ {
			name := fmt.Sprintf("$%d", i)
			if i <= len(s.Fields) {
				name = s.Fields[i-1].Name
			}
			args[name] = snapshotValue(L.Get(i), 0)
		}
	}
	return args
}

func keyLabel(v lua.LValue) string {
	switch v.Type() {
	case lua.LTString, lua.LTNumber, lua.LTBool:
		return v.String()
	}
	return "<" + v.Type().String() + " key>"
}

func snapshotValue(v lua.LValue, depth int) any {
	switch x := v.(type) {
	case lua.LString:
		return string(x)
	case lua.LNumber:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return x.String()
		}
		return float64(x)
	case lua.LBool:
		return bool(x)
	case *lua.LTable:
		if depth >= 5 {
			return "<table>"
		}
		out := map[string]any{}
		x.ForEach(func(k, v lua.LValue) { out[keyLabel(k)] = snapshotValue(v, depth+1) })
		return out
	}
	if v == lua.LNil {
		return nil
	}
	return "<" + v.Type().String() + ">"
}
