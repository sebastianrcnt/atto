package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sebastianrcnt/atto/agent"
)

// externalProject is the canonical project key agents started from a shell
// are found by: the git root (else the directory), with symlinks resolved
// and, on Windows, case folded. It is recorded with the agent before any
// worktree is entered, and inherited by everything below it.
func externalProject(cwd string) string {
	root := agent.ProjectRoot(cwd)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
	}
	return root
}

// externalProjectHere is externalProject of the working directory.
func externalProjectHere() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return externalProject(cwd)
}
