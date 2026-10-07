//go:build !darwin && !linux

package kernel

import "os/exec"

func prepareProcess(cmd *exec.Cmd) {}

func cleanupProcess(cmd *exec.Cmd) {}
