package kernel

import (
	"fmt"
	"math"
	"sort"

	lua "github.com/yuin/gopher-lua"
)

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
