// Package machine is an agent's pure Lua 5.1 computer. World access is
// installed only through the kernel's checked Go-function boundary.
package machine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"atto2/kernel"
	lua "github.com/yuin/gopher-lua"
)

const (
	RunTimeout = 30 * time.Second
	MaxOutput  = 16 << 10
)

type Result struct {
	Output string
	Pure   bool
}
type Machine struct {
	L         *lua.LState
	Kernel    *kernel.Kernel
	out       *output
	ids       map[lua.LValue]int
	jsonKinds map[*lua.LTable]bool // true: decoded JSON array; false: object
	null      *lua.LUserData
}

func New(k *kernel.Kernel) *Machine {
	m := &Machine{Kernel: k, out: &output{}, ids: map[lua.LValue]int{}, jsonKinds: map[*lua.LTable]bool{}}
	m.L = lua.NewState(lua.Options{SkipOpenLibs: true})
	for _, lib := range []struct {
		name string
		open lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase}, {lua.TabLibName, lua.OpenTable}, {lua.StringLibName, lua.OpenString}, {lua.MathLibName, lua.OpenMath},
	} {
		m.L.Push(m.L.NewFunction(lib.open))
		m.L.Push(lua.LString(lib.name))
		m.L.Call(1, 0)
	}
	for _, name := range []string{"dofile", "loadfile", "load", "loadstring", "require", "module", "setfenv", "getfenv", "_printregs", "collectgarbage", "newproxy"} {
		m.L.SetGlobal(name, lua.LNil)
	}
	m.L.GetGlobal("string").(*lua.LTable).RawSetString("dump", lua.LNil)
	// Randomness is absent, rather than sharing gopher-lua's process-global RNG.
	math := m.L.GetGlobal("math").(*lua.LTable)
	math.RawSetString("random", lua.LNil)
	math.RawSetString("randomseed", lua.LNil)
	m.L.SetGlobal("print", m.L.NewFunction(m.print))
	m.L.SetGlobal("tostring", m.L.NewFunction(m.toString))
	m.L.SetGlobal("next", m.L.NewFunction(deterministicNext))
	m.L.SetGlobal("pairs", m.L.NewFunction(func(L *lua.LState) int {
		t := L.CheckTable(1)
		keys := orderedKeys(t)
		index := 0
		L.Push(L.NewFunction(func(L *lua.LState) int {
			for index < len(keys) {
				key := keys[index]
				index++
				if value := t.RawGet(key); value != lua.LNil {
					L.Push(key)
					L.Push(value)
					return 2
				}
			}
			L.Push(lua.LNil)
			return 1
		}))
		L.Push(t)
		L.Push(lua.LNil)
		return 3
	}))
	// gopher-lua's format uses fmt.Sprintf; never pass host pointers to it.
	format := m.L.GetGlobal("string").(*lua.LTable).RawGetString("format").(*lua.LFunction)
	m.L.GetGlobal("string").(*lua.LTable).RawSetString("format", m.L.NewFunction(func(L *lua.LState) int {
		n := L.GetTop()
		L.Push(format)
		for i := 1; i <= n; i++ {
			v := L.Get(i)
			if v.Type() == lua.LTTable || v.Type() == lua.LTFunction || v.Type() == lua.LTUserData || v.Type() == lua.LTThread {
				v = lua.LString(m.identity(v))
			}
			L.Push(v)
		}
		L.Call(n, 1)
		return 1
	}))
	m.installHelpers()
	if k != nil {
		k.Bind(m.L)
	}
	return m
}

func (m *Machine) Close() { m.L.Close() }

var namedArgs = regexp.MustCompile(`\bsys\.([a-zA-Z_][a-zA-Z_0-9]*)\s*\(\s*([a-zA-Z_][a-zA-Z_0-9]*)\s*=\s*([^\)\n]+)\)`)

func (m *Machine) Run(ctx context.Context, code string) (result Result, err error) {
	result.Pure = true
	if m.Kernel != nil && m.Kernel.Exited {
		return result, fmt.Errorf("agent exited")
	}
	// Syscalls have their own timeout; the execution context limits pure computation.
	// Use a generous run bound so a bash call can use its default 60-second timeout.
	timeout := RunTimeout
	if m.Kernel != nil {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	before := 0
	if m.Kernel != nil {
		before = len(m.Kernel.Log)
		m.Kernel.BeginRun(ctx, cancel)
	}
	defer func() {
		result.Output = m.out.String()
		if m.Kernel != nil {
			result.Pure = len(m.Kernel.Log) == before
			m.Kernel.EndRun(result.Pure)
		}
	}()
	m.out = &output{}
	m.L.SetContext(ctx)
	defer m.L.RemoveContext()
	fn, err := m.L.LoadString(code)
	if err != nil {
		if args := namedArgs.FindStringSubmatch(code); args != nil {
			return result, fmt.Errorf("Lua has no named arguments; use sys.%s{%s = %s}", args[1], args[2], strings.TrimSpace(args[3]))
		}
		return result, fmt.Errorf("syntax: %v", err)
	}
	top := m.L.GetTop()
	m.L.Push(fn)
	err = m.L.PCall(0, lua.MultRet, nil)
	if m.Kernel != nil && m.Kernel.Exited {
		err = nil
	} else if err == nil {
		for i := top + 1; i <= m.L.GetTop(); i++ {
			m.out.line(m.show(m.L.Get(i), 0))
		}
	}
	m.L.SetTop(top)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("stopped after %s", timeout)
		} else if le, ok := errors.AsType[*lua.ApiError](err); ok {
			err = errors.New(m.show(le.Object, 0))
		}
	}
	return result, err
}

// LTable.Next follows insertion-ordered keys, unlike ForEach's Go maps. But
// libraries are themselves registered from Go maps: sort primitive keys and
// preserve Next's insertion order for identity keys (tables/functions).
func orderedKeys(t *lua.LTable) []lua.LValue {
	var keys []lua.LValue
	for k, _ := t.Next(lua.LNil); k != lua.LNil; {
		keys = append(keys, k)
		k, _ = t.Next(k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Type() != b.Type() {
			return a.Type() < b.Type()
		}
		switch x := a.(type) {
		case lua.LNumber:
			return x < b.(lua.LNumber)
		case lua.LString:
			return x < b.(lua.LString)
		case lua.LBool:
			return !bool(x) && bool(b.(lua.LBool))
		}
		return false
	})
	return keys
}

func deterministicNext(L *lua.LState) int {
	t := L.CheckTable(1)
	previous := L.Get(2)
	keys := orderedKeys(t)
	found := previous == lua.LNil
	for _, k := range keys {
		if found {
			L.Push(k)
			L.Push(t.RawGet(k))
			return 2
		}
		if k == previous {
			found = true
		}
	}
	if !found {
		L.RaiseError("next: invalid key")
	}
	L.Push(lua.LNil)
	return 1
}

func (m *Machine) identity(v lua.LValue) string {
	id, ok := m.ids[v]
	if !ok {
		id = len(m.ids) + 1
		m.ids[v] = id
	}
	return fmt.Sprintf("%s:%d", v.Type(), id)
}
func (m *Machine) toString(L *lua.LState) int {
	v := L.CheckAny(1)
	if fn := L.GetMetaField(v, "__tostring"); fn != lua.LNil {
		L.Push(fn)
		L.Push(v)
		L.Call(1, 1)
		return 1
	}
	if v.Type() == lua.LTTable {
		L.Push(lua.LString(m.identity(v)))
	} else {
		L.Push(lua.LString(m.show(v, 0)))
	}
	return 1
}
func (m *Machine) print(L *lua.LState) int {
	var parts []string
	for i := 1; i <= L.GetTop(); i++ {
		parts = append(parts, m.show(L.Get(i), 0))
	}
	m.out.line(strings.Join(parts, "\t"))
	return 0
}
func (m *Machine) show(v lua.LValue, depth int) string {
	if v == m.null {
		return "null"
	}
	if t, ok := v.(*lua.LTable); ok {
		if depth >= 5 {
			return "{…}"
		}
		var parts []string
		for i, k := range orderedKeys(t) {
			if i == 50 {
				parts = append(parts, "…")
				break
			}
			val := t.RawGet(k)
			text := m.show(val, depth+1)
			if s, ok := val.(lua.LString); ok {
				text = fmt.Sprintf("%q", string(s))
			}
			parts = append(parts, m.show(k, depth+1)+" = "+text)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	switch v.Type() {
	case lua.LTFunction, lua.LTUserData, lua.LTThread:
		return m.identity(v)
	}
	return v.String()
}

type output struct {
	b       strings.Builder
	dropped int
}

func (o *output) line(s string) {
	if o.b.Len()+len(s)+1 > MaxOutput {
		o.dropped += len(s) + 1
		return
	}
	o.b.WriteString(s)
	o.b.WriteByte('\n')
}
func (o *output) String() string {
	if o.dropped > 0 {
		return o.b.String() + fmt.Sprintf("[%d more bytes of output not shown]\n", o.dropped)
	}
	return o.b.String()
}
