package machine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

func (m *Machine) installHelpers() {
	L := m.L
	text := L.NewTable()
	add := func(name string, f lua.LGFunction) { text.RawSetString(name, L.NewFunction(f)) }
	array := func(items []string) *lua.LTable {
		t := L.NewTable()
		for _, s := range items {
			t.Append(lua.LString(s))
		}
		return t
	}
	add("split", func(L *lua.LState) int { L.Push(array(strings.Split(L.CheckString(1), L.CheckString(2)))); return 1 })
	add("lines", func(L *lua.LState) int {
		s := L.CheckString(1)
		var lines []string
		if s != "" {
			lines = strings.Split(strings.TrimSuffix(s, "\n"), "\n")
			for i := range lines {
				lines[i] = strings.TrimSuffix(lines[i], "\r")
			}
		}
		L.Push(array(lines))
		return 1
	})
	add("trim", func(L *lua.LState) int { L.Push(lua.LString(strings.TrimSpace(L.CheckString(1)))); return 1 })
	add("match_all", func(L *lua.LState) int {
		s, pattern := L.CheckString(1), L.CheckString(2)
		re, err := regexp.Compile(pattern)
		if err != nil {
			L.RaiseError("text.match_all: %s", err)
			return 0
		}
		t := L.NewTable()
		for _, match := range re.FindAllStringSubmatch(s, -1) {
			if re.NumSubexp() == 0 {
				t.Append(lua.LString(match[0]))
			} else {
				t.Append(array(match[1:]))
			}
		}
		L.Push(t)
		return 1
	})
	L.SetGlobal("text", text)
	j := L.NewTable()
	m.null = L.NewUserData()
	j.RawSetString("null", m.null)
	j.RawSetString("encode", L.NewFunction(func(L *lua.LState) int {
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
	}))
	j.RawSetString("decode", L.NewFunction(func(L *lua.LState) int {
		var v any
		if err := json.Unmarshal([]byte(L.CheckString(1)), &v); err != nil {
			L.RaiseError("json.decode: %s", err)
			return 0
		}
		L.Push(m.decodeValue(v))
		return 1
	}))
	L.SetGlobal("json", j)
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
	default:
		switch x := v.(type) {
		case string:
			return lua.LString(x)
		case float64:
			return lua.LNumber(x)
		case bool:
			return lua.LBool(x)
		}
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
		if active[x] {
			return nil, fmt.Errorf("cyclic table")
		}
		active[x] = true
		defer delete(active, x)
		keys := orderedKeys(x)
		array := true
		if kind, ok := m.jsonKinds[x]; ok {
			array = kind
		}
		for i, k := range keys {
			if k != lua.LNumber(i+1) {
				array = false
				break
			}
		}
		if array {
			a := make([]any, 0, len(keys))
			for _, k := range keys {
				item, err := m.jsonValue(x.RawGet(k), active)
				if err != nil {
					return nil, err
				}
				a = append(a, item)
			}
			return a, nil
		}
		obj := map[string]any{}
		for _, k := range keys {
			key, ok := k.(lua.LString)
			if !ok {
				return nil, fmt.Errorf("object keys must be strings; arrays must be dense")
			}
			item, err := m.jsonValue(x.RawGet(k), active)
			if err != nil {
				return nil, err
			}
			obj[string(key)] = item
		}
		return obj, nil
	default:
		return nil, fmt.Errorf("unsupported %s", v.Type())
	}
}
