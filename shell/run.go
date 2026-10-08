package shell

import (
	"os/exec"
	"time"
)

// Run runs a context-backed command in its own process tree, so cancellation
// kills its descendants too. Closing the tree releases platform resources.
func Run(cmd *exec.Cmd) error {
	tree := NewTree(cmd)
	defer tree.Close()
	return tree.Run()
}

// Run starts and waits for this tree's command. The caller owns Close and any
// extra cleanup after success, such as killing a status command's leftovers.
func (t *Tree) Run() error {
	if t.cmd.WaitDelay == 0 {
		t.cmd.WaitDelay = 2 * time.Second // escaped children must not hold output pipes forever
	}
	if err := t.cmd.Start(); err != nil {
		return err
	}
	t.Started()
	return t.cmd.Wait()
}
