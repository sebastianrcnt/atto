package machine

import (
	"sort"

	lua "github.com/yuin/gopher-lua"
)

func (m *Machine) installIteration() {
	m.L.SetGlobal("next", m.L.NewFunction(deterministicNext))
	m.L.SetGlobal("pairs", m.L.NewFunction(deterministicPairs))
}

func deterministicPairs(L *lua.LState) int {
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
