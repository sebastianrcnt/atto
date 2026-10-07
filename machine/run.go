package machine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

var namedArgs = regexp.MustCompile(`\bsys\.([a-zA-Z_][a-zA-Z_0-9]*)\s*\(\s*([a-zA-Z_][a-zA-Z_0-9]*)\s*=\s*([^\)\n]+)\)`)

func (m *Machine) Run(ctx context.Context, code string) (result Result, err error) {
	result.Pure = true
	if m.Kernel != nil && m.Kernel.Exited {
		return result, fmt.Errorf("agent exited")
	}
	// Syscalls have their own timeout; the execution context limits pure computation.
	// Use a generous run bound so a bash call can use its default 60-second timeout.
	timeout := m.runTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	before := 0
	if m.Kernel != nil {
		before = len(m.Kernel.Log)
		m.Kernel.BeginRun(ctx, cancel)
	}
	defer func() {
		result.Output = m.out.String()
		if m.Kernel != nil {
			result.Pure = len(m.Kernel.Log) == before
			m.Kernel.EndRun(result.Pure)
		}
	}()
	m.out = &output{}
	m.L.SetContext(ctx)
	defer m.L.RemoveContext()
	fn, err := m.load(code)
	if err != nil {
		return result, err
	}
	err = m.execute(fn)
	err = m.runError(ctx, timeout, err)
	return result, err
}

func (m *Machine) runTimeout() time.Duration {
	if m.Kernel != nil {
		return 5 * time.Minute
	}
	return RunTimeout
}

func (m *Machine) load(code string) (*lua.LFunction, error) {
	fn, err := m.L.LoadString(code)
	if err != nil {
		if args := namedArgs.FindStringSubmatch(code); args != nil {
			//lint:ignore ST1005 Lua is a proper noun; preserve the syntax hint.
			return nil, fmt.Errorf("Lua has no named arguments; use sys.%s{%s = %s}", args[1], args[2], strings.TrimSpace(args[3]))
		}
		return nil, fmt.Errorf("syntax: %v", err)
	}
	return fn, nil
}

func (m *Machine) execute(fn *lua.LFunction) error {
	top := m.L.GetTop()
	m.L.Push(fn)
	err := m.L.PCall(0, lua.MultRet, nil)
	if m.Kernel != nil && m.Kernel.Exited {
		err = nil
	} else if err == nil {
		for i := top + 1; i <= m.L.GetTop(); i++ {
			m.out.line(m.show(m.L.Get(i), 0))
		}
	}
	m.L.SetTop(top)
	return err
}

func (m *Machine) runError(ctx context.Context, timeout time.Duration, err error) error {
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("stopped after %s", timeout)
		} else if le, ok := errors.AsType[*lua.ApiError](err); ok {
			err = errors.New(m.show(le.Object, 0))
		}
	}
	return err
}
