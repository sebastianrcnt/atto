// Package machine is an agent's computer: a Lua VM of its own, with only
// what the agent is given. Nothing reaches the host unless a device puts
// it there; the standard libraries that would (os, io, load, require) are
// not opened.
//
// Like a shell, commands print what they find (ls, cat, grep…); the fs
// table gives the same as values for programs (fs.read, fs.list…). Paths
// are inside the machine's root, which is "/" to the agent, as in a
// chroot.
package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// Limits of one run.
const (
	RunTimeout = 30 * time.Second
	MaxOutput  = 16 << 10 // bytes of output the agent gets back
)

// Machine is one agent's Lua VM and its working directory.
type Machine struct {
	L    *lua.LState
	root string // host path the agent sees as "/"
	cwd  string // host path, inside root
	out  *output
}

// New makes a machine whose files are those under root, read only.
func New(root string) (*Machine, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, err
	}
	m := &Machine{root: abs, cwd: abs, out: &output{}}
	m.L = lua.NewState(lua.Options{SkipOpenLibs: true})
	for _, lib := range []struct {
		name string
		open lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		m.L.Push(m.L.NewFunction(lib.open))
		m.L.Push(lua.LString(lib.name))
		m.L.Call(1, 0)
	}
	// What the base library has that reaches outside, or loads code the
	// agent did not write in the run.
	for _, name := range []string{"dofile", "loadfile", "load", "loadstring", "require", "module", "setfenv", "getfenv", "_printregs", "collectgarbage"} {
		m.L.SetGlobal(name, lua.LNil)
	}
	m.L.SetGlobal("print", m.L.NewFunction(m.print))
	m.installFS()
	return m, nil
}

// Close frees the VM.
func (m *Machine) Close() { m.L.Close() }

// Run runs code and returns what it printed (and what it returned), cut
// to MaxOutput. A Lua error comes back in the output, after what was
// printed before it, and as err.
func (m *Machine) Run(ctx context.Context, code string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()
	m.out = &output{}
	m.L.SetContext(ctx)
	defer m.L.RemoveContext()

	fn, err := m.L.LoadString(code)
	if err != nil {
		return m.out.String(), fmt.Errorf("syntax: %v", err)
	}
	top := m.L.GetTop()
	m.L.Push(fn)
	err = m.L.PCall(0, lua.MultRet, nil)
	if err == nil {
		// Like a REPL: what the chunk returns is shown.
		for i := top + 1; i <= m.L.GetTop(); i++ {
			m.out.line(show(m.L.Get(i)))
		}
	}
	m.L.SetTop(top)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("stopped after %s", RunTimeout)
		}
		if le, ok := errors.AsType[*lua.ApiError](err); ok {
			err = errors.New(le.Object.String())
		}
	}
	return m.out.String(), err
}

// Pwd is the working directory as the agent sees it.
func (m *Machine) Pwd() string { return m.virtual(m.cwd) }

func (m *Machine) print(L *lua.LState) int {
	var parts []string
	for i := 1; i <= L.GetTop(); i++ {
		parts = append(parts, show(L.Get(i)))
	}
	m.out.line(strings.Join(parts, "\t"))
	return 0
}

// resolve turns a path the agent gave into a host path inside root.
func (m *Machine) resolve(p string) (string, error) {
	if p == "" {
		p = "."
	}
	var host string
	if strings.HasPrefix(p, "/") {
		host = filepath.Join(m.root, filepath.FromSlash(p))
	} else {
		host = filepath.Join(m.cwd, filepath.FromSlash(p))
	}
	if !m.inside(host) {
		return "", fmt.Errorf("%s: outside the machine", p)
	}
	// A link must not lead out either.
	if real, err := filepath.EvalSymlinks(host); err == nil && !m.inside(real) {
		return "", fmt.Errorf("%s: outside the machine", p)
	}
	return host, nil
}

func (m *Machine) inside(host string) bool {
	rel, err := filepath.Rel(m.root, host)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// virtual is the path the agent sees for a host path.
func (m *Machine) virtual(host string) string {
	rel, err := filepath.Rel(m.root, host)
	if err != nil || rel == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(rel)
}

// show is how a value prints.
func show(v lua.LValue) string {
	if t, ok := v.(*lua.LTable); ok {
		var b strings.Builder
		b.WriteString("{")
		n := 0
		t.ForEach(func(k, val lua.LValue) {
			if n > 0 {
				b.WriteString(", ")
			}
			if n == 50 {
				b.WriteString("…")
				return
			}
			if n < 50 {
				if _, isNum := k.(lua.LNumber); !isNum {
					b.WriteString(k.String() + " = ")
				}
				if s, ok := val.(lua.LString); ok {
					b.WriteString(fmt.Sprintf("%q", string(s)))
				} else {
					b.WriteString(val.String())
				}
			}
			n++
		})
		b.WriteString("}")
		return b.String()
	}
	return v.String()
}

// output collects what a run prints, up to MaxOutput.
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

// statFile is os.Stat of a resolved path, with the agent's path in errors.
func statFile(host, p string) (os.FileInfo, error) {
	fi, err := os.Stat(host)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: no such file or directory", p)
	}
	return fi, err
}
