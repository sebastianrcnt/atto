//go:build !noext

package extensions

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/dop251/goja"

	"github.com/sebastianrcnt/atto/shell"
)

// defaultExecTimeout bounds atto.exec when no timeout is given.
const defaultExecTimeout = 60 * time.Second

// jsExec is atto.exec(command, {cwd, timeout}): it runs command with the
// agent's shell (bash, or PowerShell on Windows, on a console of its
// own) and resolves to {stdout, stderr, code}. A command still running at
// the timeout (ms) is killed with everything it started; code is then -1
// and killed true.
func (e *ext) jsExec(command string, opts *goja.Object) goja.Value {
	e.readOnlyRender()
	dir := e.m.cwd
	if d := optString(opts, "cwd"); d != "" {
		dir = e.resolve(d)
	}
	timeout := defaultExecTimeout
	if ms := optNumber(opts, "timeout"); ms > 0 {
		timeout = time.Duration(ms * float64(time.Millisecond))
	}
	id, _ := e.m.session()
	name := e.spec.Name
	return e.async(func() (func() goja.Value, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := shell.Command(ctx, command)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "ATTO_SESSION_ID="+id, "ATTO_EXTENSION="+name)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := shell.Run(cmd)
		code, killed := 0, false
		var ee *exec.ExitError
		switch {
		case ctx.Err() != nil:
			code, killed = -1, true
		case errors.As(err, &ee):
			code = ee.ExitCode()
		case err != nil:
			return nil, err
		}
		return func() goja.Value {
			o := e.vm.NewObject()
			_ = o.Set("stdout", stdout.String())
			_ = o.Set("stderr", stderr.String())
			_ = o.Set("code", code)
			_ = o.Set("killed", killed)
			return o
		}, nil
	})
}

// resolve makes p absolute against the session's directory.
func (e *ext) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.m.cwd, p)
}
