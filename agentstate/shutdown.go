package agentstate

import (
	"fmt"
	"os"
	"path/filepath"
)

func closedPath(root string) string { return filepath.Join(Dir(root), ".closed") }

// LockTree serializes new agent work with root-session shutdown.
func LockTree(session string) (func(), error) {
	return lockFile(filepath.Join(Dir(Root(session)), ".tree.lock"))
}

// StartWork holds the tree until a new agent or turn is durably started.
func StartWork(session string) (func(), error) {
	release, err := LockTree(session)
	if err != nil {
		return nil, err
	}
	for s, n := session, 0; n < maxHops; n++ {
		if _, err := os.Stat(closedPath(s)); !os.IsNotExist(err) {
			release()
			return nil, fmt.Errorf("the parent session is closed: reopen it before starting agent work")
		}
		parent, _, ok := ParentOf(s)
		if !ok {
			return release, nil
		}
		s = parent
	}
	release()
	return nil, fmt.Errorf("agent ancestry has a loop")
}

// CloseTree prevents new work below session until it is opened again. Hold the
// returned lock while walking and stopping the descendants.
func CloseTree(session string) (func(), error) {
	release, err := LockTree(session)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(closedPath(session), nil, 0o644); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// OpenTree reopens this session, without reopening any closed ancestor.
func OpenTree(session string) {
	release, err := LockTree(session)
	if err != nil {
		return
	}
	defer release()
	_ = os.Remove(closedPath(session))
}
