package server

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/internal/sessionops"
	"github.com/sebastianrcnt/atto/session"
)

// mutateSession acts on an inventory ID, including unloaded and archived sessions.
// Agent teardown is serialized against spawns/turns and keeps closed records.
func (s *Server) mutateSession(ctx context.Context, method string, p threadParams) (any, error) {
	if p.ThreadID == "" {
		return nil, invalid("threadId is required")
	}
	id := p.ThreadID
	if st, err := agentstate.Load(id); err == nil && method != "thread/unarchive" && st.Lifecycle != agentstate.Closed {
		release, err := agentstate.LockTree(id)
		if err != nil {
			return nil, err
		}
		defer release()
		var tree []agentstate.State
		seen := map[string]bool{}
		var walk func(agentstate.State)
		walk = func(st agentstate.State) {
			if seen[st.Session] {
				return
			}
			seen[st.Session] = true
			for _, kid := range agentstate.Children(st.Session) {
				walk(kid)
			}
			tree = append(tree, st)
		}
		walk(st)
		for _, node := range tree {
			if turn := node.Latest(); turn.Status.Active() {
				return nil, failure(ReasonBusy, "agent %s has a running or queued turn: interrupt it before archiving/deleting", node.Path)
			}
			if dirt, err := agentstate.WorktreeDirt(node); err != nil {
				return nil, err
			} else if dirt != "" {
				return nil, failure(ReasonBusy, "agent %s worktree has uncommitted changes: commit them before closing", node.Path)
			}
		}
		// Holding the tree lock prevents new work until the closing records persist.
		for _, node := range tree {
			if err := agentstate.MarkClosing(node.Session); err != nil {
				return nil, err
			}
		}
		defer func() {
			for _, node := range tree {
				if cur, e := agentstate.Load(node.Session); e == nil && cur.Lifecycle == agentstate.Closing {
					cur.Lifecycle = agentstate.Open
					_ = agentstate.Save(cur)
				}
			}
		}()
		for _, node := range tree {
			if err := s.stopInventoryThread(ctx, node.Session, false); err != nil {
				return nil, err
			}
			if node.Worktree != "" {
				if _, err := agentstate.RemoveWorktree(node, false); err != nil {
					return nil, err
				}
			}
			path, err := session.Find(node.Session)
			archive := ""
			if err == nil {
				archive = path
				if !isArchivedPath(path) {
					archive, err = session.Archive(path)
				}
				if err != nil {
					return nil, err
				}
			}
			if err := agentstate.MarkClosed(node.Session, archive); err != nil {
				return nil, err
			}
		}
	}
	if method == "thread/unarchive" {
		path, err := session.Find(id)
		if err != nil {
			return nil, err
		}
		dst, err := session.Unarchive(path)
		return map[string]any{"threadId": id, "path": dst}, err
	}
	if err := s.stopInventoryThread(ctx, id, p.Stop); err != nil {
		return nil, err
	}
	path, err := session.Find(id)
	if err != nil {
		// Closing an agent whose first turn never wrote a transcript is still valid.
		if st, e := agentstate.Load(id); e == nil && st.Lifecycle == agentstate.Closed {
			return map[string]any{"threadId": id}, nil
		}
		return nil, err
	}
	if method == "thread/delete" {
		var notices bytes.Buffer
		err := sessionops.Delete(&notices, path)
		return map[string]any{"threadId": id, "notices": notices.String()}, err
	}
	dst := path
	if !isArchivedPath(path) {
		dst, err = session.Archive(path)
	}
	return map[string]any{"threadId": id, "path": dst}, err
}

func (s *Server) stopInventoryThread(ctx context.Context, id string, stop bool) error {
	if t, err := s.thread(id); err == nil {
		err = t.call(func() error {
			if t.readOnly != "" {
				return failure(ReasonReadOnly, "%s", t.readOnly)
			}
			if t.sess.IsAgent() && (t.turns.Busy || t.mgd != nil && t.mgd.busy()) {
				return failure(ReasonBusy, "agent has a running turn: interrupt it before archiving/deleting")
			}
			if (t.turns.Busy || t.shell != nil) && !stop {
				return failure(ReasonBusy, "live worker: confirm stop it and archive/delete first")
			}
			if t.sess.Leaf() == "" {
				t.sess.Branch("")
			}
			return t.sess.Err()
		})
		if err != nil {
			return err
		}
		s.closeThread(t, closeMode{reason: "archive"})
	}
	if s.Workers != nil && s.Workers.Close != nil {
		return s.Workers.Close(ctx, id, stop)
	}
	return nil
}

// isArchivedPath also recognizes compressed archives.
func isArchivedPath(path string) bool {
	rel, err := filepath.Rel(config.ArchivedDir(), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
