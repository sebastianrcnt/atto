package machine

import (
	"fmt"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

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
