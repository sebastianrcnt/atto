package kernel

import (
	"fmt"
	"sort"
	"time"

	lua "github.com/yuin/gopher-lua"
)

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
	}
}

func (k *Kernel) unknownCall(L *lua.LState) int {
	name := L.CheckString(2)
	L.Push(L.NewFunction(func(L *lua.LState) int {
		err := "sys." + name + ": unknown syscall"
		k.Log = append(k.Log, Entry{Time: time.Now(), Name: name, Args: snapshotArgs(L, Syscall{}), Error: err})
		L.RaiseError("%s", err)
		return 0
	}))
	return 1
}
