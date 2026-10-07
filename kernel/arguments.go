package kernel

import (
	"fmt"

	lua "github.com/yuin/gopher-lua"
)

func arguments(L *lua.LState, s Syscall) (Args, error) {
	args := Args{}
	values, err := argumentValues(L, s)
	if err != nil {
		return args, err
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

func argumentValues(L *lua.LState, s Syscall) (map[string]lua.LValue, error) {
	if t, ok := L.Get(1).(*lua.LTable); ok && L.GetTop() == 1 {
		return tableArguments(t, s)
	}
	values := map[string]lua.LValue{}
	if L.GetTop() > len(s.Fields) {
		return values, fmt.Errorf("sys.%s: too many arguments; use %s", s.Name, Form(s))
	}
	for i := 1; i <= L.GetTop(); i++ {
		values[s.Fields[i-1].Name] = L.Get(i)
	}
	return values, nil
}

func tableArguments(t *lua.LTable, s Syscall) (map[string]lua.LValue, error) {
	values := map[string]lua.LValue{}
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
		return values, fmt.Errorf("sys.%s: unknown field %s; use %s", s.Name, bad, Form(s))
	}
	return values, nil
}
