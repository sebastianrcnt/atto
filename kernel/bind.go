package kernel

import (
	"fmt"
	"sort"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// Bind installs closures: aliases still pass through exactly this boundary.
// Absent names resolve through the same boundary, including logged denials.
func (k *Kernel) Bind(L *lua.LState) {
	t := L.NewTable()
	names := make([]string, 0, len(k.registry))
	for name := range k.grant {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := k.registry[name]
		t.RawSetString(name, L.NewFunction(k.boundCall(s)))
	}
	// Unknown names are also checked and logged rather than a nil-function error.
	mt := L.NewTable()
	mt.RawSetString("__index", L.NewFunction(k.unknownCall))
	L.SetMetatable(t, mt)
	L.SetGlobal("sys", t)
}

func (k *Kernel) boundCall(s Syscall) lua.LGFunction {
	return func(L *lua.LState) int {
		entry := Entry{Time: time.Now(), Name: s.Name, Args: snapshotArgs(L, s)}
		index := len(k.Log)
		k.Log = append(k.Log, entry)
		defer func() { k.Log[index] = entry }()
		args, err := arguments(L, s)
		if k.Exited {
			err = fmt.Errorf("agent exited")
		} else if !k.grant[s.Name] {
			err = fmt.Errorf("sys.%s is not granted to this agent", s.Name)
		}
		if err != nil {
			entry.Error = err.Error()
		}
		invoke := func() (any, error) {
			if err != nil {
				return nil, err
			}
			return s.Call(k.ctx, args)
		}
		entry = k.exchange(entry, invoke)
		if k.Exited {
			k.cancel()
			L.RaiseError("agent exited")
		}
		if entry.Error != "" {
			L.Error(lua.LString(entry.Error), 0)
			return 0
		}
		L.Push(ToLua(L, entry.Result))
		return 1
	}
}

func (k *Kernel) unknownCall(L *lua.LState) int {
	name := L.CheckString(2)
	if s, ok := k.registry[name]; ok {
		L.Push(L.NewFunction(k.boundCall(s)))
		return 1
	}
	L.Push(L.NewFunction(func(L *lua.LState) int {
		err := "sys." + name + ": unknown syscall"
		entry := k.exchange(Entry{Time: time.Now(), Name: name, Args: snapshotArgs(L, Syscall{}), Error: err}, func() (any, error) { return nil, fmt.Errorf("%s", err) })
		k.Log = append(k.Log, entry)
		L.RaiseError("%s", entry.Error)
		return 0
	}))
	return 1
}

func (k *Kernel) exchange(entry Entry, invoke func() (any, error)) Entry {
	call := func() Entry {
		result, err := invoke()
		entry.Result = result
		if err != nil {
			entry.Error = err.Error()
		}
		return entry
	}
	if k.Exchange != nil {
		return k.Exchange(entry, call)
	}
	return call()
}
