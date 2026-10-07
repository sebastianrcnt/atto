package kernel

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const OutputLimit = 64 << 10

type capped struct {
	b       strings.Builder
	dropped int
}

func (b *capped) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, OutputLimit-b.b.Len())
	b.b.Write(p[:keep])
	b.dropped += n - keep
	return n, nil
}
func (b *capped) String() string {
	if b.dropped > 0 {
		return b.b.String() + fmt.Sprintf("\n[%d bytes cut]\n", b.dropped)
	}
	return b.b.String()
}

func bash(ctx context.Context, dir string, a Args) (any, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("sys.bash: read-only sandbox is only implemented on macOS (Linux TODO)")
	}
	seconds := 60.0
	if v, ok := a["timeout"]; ok {
		seconds = v.(float64)
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > float64(math.MaxInt64)/float64(time.Second) {
		return nil, fmt.Errorf("sys.bash: field timeout expected positive finite seconds; use sys.bash{cmd = string, timeout = number (optional)}")
	}
	tmp, err := os.MkdirTemp("", "atto2-bash-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	tmp, err = filepath.EvalSymlinks(tmp)
	if err != nil {
		return nil, err
	}
	// Deny by default: no network, writes, IPC services, or signalling other agents.
	// The temp exception is canonical, so symlinks cannot turn it into project writes.
	profile := fmt.Sprintf(`(version 1)
(deny default)
(allow file-read* process-exec process-fork sysctl-read)
(allow signal (target self))
(allow file-write* (literal "/dev/null") (subpath %q))`, tmp)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds*float64(time.Second)))
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, "/bin/bash", "-c", a["cmd"].(string))
	cmd.Dir = dir
	// Do not inherit credentials or shell startup configuration.
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + tmp, "TMPDIR=" + tmp, "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0"}
	prepareProcess(cmd)
	defer cleanupProcess(cmd)
	var stdout, stderr capped
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			return nil, fmt.Errorf("sys.bash: %w", err)
		}
	}
	if ctx.Err() != nil {
		code = 124
		fmt.Fprintf(&stderr, "\n[timeout after %g seconds]\n", seconds)
	}
	return map[string]any{"stdout": stdout.String(), "stderr": stderr.String(), "code": code}, nil
}
