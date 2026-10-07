package machine

import (
	"encoding/json"
	"fmt"
	"sort"

	lua "github.com/yuin/gopher-lua"
)

func (m *Machine) installJSON() {
	j := m.L.NewTable()
	m.null = m.L.NewUserData()
	j.RawSetString("null", m.null)
	j.RawSetString("encode", m.L.NewFunction(m.encodeJSON))
	j.RawSetString("decode", m.L.NewFunction(m.decodeJSON))
	m.L.SetGlobal("json", j)
}

func (m *Machine) encodeJSON(L *lua.LState) int {
	v, err := m.jsonValue(L.CheckAny(1), map[*lua.LTable]bool{})
	if err == nil {
		var b []byte
		b, err = json.Marshal(v)
		if err == nil {
			L.Push(lua.LString(b))
			return 1
		}
	}
	L.RaiseError("json.encode: %s", err)
	return 0
}

func (m *Machine) decodeJSON(L *lua.LState) int {
	var v any
	if err := json.Unmarshal([]byte(L.CheckString(1)), &v); err != nil {
		L.RaiseError("json.decode: %s", err)
		return 0
	}
	L.Push(m.decodeValue(v))
	return 1
}

func (m *Machine) decodeValue(v any) lua.LValue {
	switch x := v.(type) {
	case nil:
		return m.null
	case []any:
		t := m.L.NewTable()
		m.jsonKinds[t] = true
		for _, v := range x {
			t.Append(m.decodeValue(v))
		}
		return t
	case map[string]any:
		t := m.L.NewTable()
		m.jsonKinds[t] = false
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.RawSetString(k, m.decodeValue(x[k]))
		}
		return t
	case string:
		return lua.LString(x)
	case float64:
		return lua.LNumber(x)
	case bool:
		return lua.LBool(x)
	}
	panic("invalid decoded JSON value")
}

func (m *Machine) jsonValue(v lua.LValue, active map[*lua.LTable]bool) (any, error) {
	if v == m.null || v == lua.LNil {
		return nil, nil
	}
	switch x := v.(type) {
	case lua.LString:
		return string(x), nil
	case lua.LNumber:
		return float64(x), nil
	case lua.LBool:
		return bool(x), nil
	case *lua.LTable:
		return m.jsonTable(x, active)
	default:
		return nil, fmt.Errorf("unsupported %s", v.Type())
	}
}

func (m *Machine) jsonTable(table *lua.LTable, active map[*lua.LTable]bool) (any, error) {
	if active[table] {
		return nil, fmt.Errorf("cyclic table")
	}
	active[table] = true
	defer delete(active, table)
	keys := orderedKeys(table)
	array := true
	if kind, ok := m.jsonKinds[table]; ok {
		array = kind
	}
	for i, k := range keys {
		if k != lua.LNumber(i+1) {
			array = false
			break
		}
	}
	if array {
		return m.jsonArray(table, keys, active)
	}
	return m.jsonObject(table, keys, active)
}

func (m *Machine) jsonArray(table *lua.LTable, keys []lua.LValue, active map[*lua.LTable]bool) (any, error) {
	a := make([]any, 0, len(keys))
	for _, k := range keys {
		item, err := m.jsonValue(table.RawGet(k), active)
		if err != nil {
			return nil, err
		}
		a = append(a, item)
	}
	return a, nil
}

func (m *Machine) jsonObject(table *lua.LTable, keys []lua.LValue, active map[*lua.LTable]bool) (any, error) {
	obj := map[string]any{}
	for _, k := range keys {
		key, ok := k.(lua.LString)
		if !ok {
			return nil, fmt.Errorf("object keys must be strings; arrays must be dense")
		}
		item, err := m.jsonValue(table.RawGet(k), active)
		if err != nil {
			return nil, err
		}
		obj[string(key)] = item
	}
	return obj, nil
}
