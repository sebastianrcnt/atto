package machine

import lua "github.com/yuin/gopher-lua"

func (m *Machine) openLibraries() {
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
}

func (m *Machine) removeWorldAccess() {
	for _, name := range []string{"dofile", "loadfile", "load", "loadstring", "require", "module", "setfenv", "getfenv", "_printregs", "collectgarbage", "newproxy"} {
		m.L.SetGlobal(name, lua.LNil)
	}
	m.L.GetGlobal("string").(*lua.LTable).RawSetString("dump", lua.LNil)
	// Randomness is absent, rather than sharing gopher-lua's process-global RNG.
	math := m.L.GetGlobal("math").(*lua.LTable)
	math.RawSetString("random", lua.LNil)
	math.RawSetString("randomseed", lua.LNil)
}

func (m *Machine) installDisplay() {
	m.L.SetGlobal("print", m.L.NewFunction(m.print))
	m.L.SetGlobal("tostring", m.L.NewFunction(m.toString))
}

func (m *Machine) wrapFormat() {
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
}
