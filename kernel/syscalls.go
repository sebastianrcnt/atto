package kernel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Project registers the step-one world interface and grants it to this agent.
func Project(dir string) (*Kernel, error) {
	return WithGrant(dir, "bash", "now", "exit")
}

// WithGrant registers devices, checking the working directory only for bash.
func WithGrant(dir string, names ...string) (*Kernel, error) {
	abs := dir
	if slices.Contains(names, "bash") {
		var err error
		abs, err = projectDir(dir)
		if err != nil {
			return nil, err
		}
	}
	k := New()
	for _, s := range []Syscall{
		{Name: "bash", Fields: []Field{{"cmd", "string", true}, {"timeout", "number", false}}, Description: "{stdout, stderr, code}; /bin/bash -c in the project, OS read-only except /dev/null and a private temp directory; timeout seconds, default 60; 64 KiB per stream", Call: func(ctx context.Context, a Args) (any, error) { return bash(ctx, abs, a) }},
		{Name: "now", Description: "Unix seconds", Call: func(context.Context, Args) (any, error) { return time.Now().Unix(), nil }},
		{Name: "exit", Fields: []Field{{"report", "string", true}}, Description: "ends your life with this report; execution stops", Call: func(_ context.Context, a Args) (any, error) {
			k.Exited = true
			k.Report = a["report"].(string)
			return k.Report, nil
		}},
	} {
		if err := k.Register(s); err != nil {
			return nil, err
		}
	}
	if err := k.Grant(names...); err != nil {
		return nil, err
	}
	return k, nil
}

func projectDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s: not a directory", dir)
	}
	return abs, nil
}
