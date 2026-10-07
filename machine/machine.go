// Package machine is an agent's pure Lua 5.1 computer. World access is
// installed only through the kernel's checked Go-function boundary.
package machine

import (
	"time"

	"atto2/kernel"
	lua "github.com/yuin/gopher-lua"
)

const (
	RunTimeout = 30 * time.Second
	MaxOutput  = 16 << 10
)

type Result struct {
	Output string
	Pure   bool
}
type Machine struct {
	L         *lua.LState
	Kernel    *kernel.Kernel
	out       *output
	ids       map[lua.LValue]int
	jsonKinds map[*lua.LTable]bool // true: decoded JSON array; false: object
	null      *lua.LUserData
}

func New(k *kernel.Kernel) *Machine {
	m := &Machine{
		L:         lua.NewState(lua.Options{SkipOpenLibs: true}),
		Kernel:    k,
		out:       &output{},
		ids:       map[lua.LValue]int{},
		jsonKinds: map[*lua.LTable]bool{},
	}
	m.openLibraries()
	m.removeWorldAccess()
	m.installDisplay()
	m.installIteration()
	m.wrapFormat()
	m.installText()
	m.installJSON()
	if k != nil {
		k.Bind(m.L)
	}
	return m
}

func (m *Machine) Close() { m.L.Close() }
