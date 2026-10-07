package machine

import (
	"regexp"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

func (m *Machine) installText() {
	text := m.L.NewTable()
	for _, helper := range []struct {
		name    string
		handler lua.LGFunction
	}{
		{"split", textSplit}, {"lines", textLines}, {"trim", textTrim}, {"match_all", textMatchAll},
	} {
		text.RawSetString(helper.name, m.L.NewFunction(helper.handler))
	}
	m.L.SetGlobal("text", text)
}

func stringArray(L *lua.LState, items []string) *lua.LTable {
	t := L.NewTable()
	for _, s := range items {
		t.Append(lua.LString(s))
	}
	return t
}

func textSplit(L *lua.LState) int {
	L.Push(stringArray(L, strings.Split(L.CheckString(1), L.CheckString(2))))
	return 1
}

func textLines(L *lua.LState) int {
	s := L.CheckString(1)
	var lines []string
	if s != "" {
		lines = strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		for i := range lines {
			lines[i] = strings.TrimSuffix(lines[i], "\r")
		}
	}
	L.Push(stringArray(L, lines))
	return 1
}

func textTrim(L *lua.LState) int {
	L.Push(lua.LString(strings.TrimSpace(L.CheckString(1))))
	return 1
}

func textMatchAll(L *lua.LState) int {
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
			t.Append(stringArray(L, match[1:]))
		}
	}
	L.Push(t)
	return 1
}
