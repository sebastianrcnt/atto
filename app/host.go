package app

import (
	"context"
	"encoding/json"
	"errors"
	"maps"

	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/server"
)

// Where sessions run. Without the daemon, one runtime in this process
// runs every session the terminal opens, over one connection. In a daemon
// mode each session runs in a worker of its own (daemon.StartWorker): the
// terminal connects to the worker of the session it shows, and switching
// sessions switches connections, leaving the old worker to finish what it
// runs.

// workers reports whether sessions run in the daemon's workers.
func (a *App) workers() bool { return a.conn == nil || a.conn.own == nil }

// openThread opens session id (a new one when id is "") and hands its
// snapshot to then on the UI goroutine, with the connection to it in use;
// old is the connection used before. A session another process writes
// comes as a read-only snapshot.
func (a *App) openThread(id string, params map[string]any, then func(info server.ThreadInfo, old *conn)) {
	if !a.workers() {
		method := "thread/start"
		p := map[string]any{"cwd": a.cwd, "threadId": ""}
		if id != "" {
			method, p = "thread/resume", map[string]any{"threadId": id, "cwd": a.cwd}
		}
		maps.Copy(p, params)
		a.rpc(method, p, func(raw json.RawMessage, err error) {
			if err != nil {
				a.errorNotice(err)
				return
			}
			var info server.ThreadInfo
			if json.Unmarshal(raw, &info) == nil {
				then(info, a.conn)
			}
		})
		return
	}
	go func() {
		cn, info, err := a.dialWorker(id, params)
		a.ui.Do(func() {
			if err != nil {
				a.errorNotice(err)
				return
			}
			old := a.conn
			if cn != nil {
				a.use(cn)
			}
			then(info, old)
		})
	}()
}

// openSync is openThread before the UI runs: it waits.
func (a *App) openSync(id string, params map[string]any) (server.ThreadInfo, error) {
	if !a.workers() {
		method := "thread/start"
		p := map[string]any{"cwd": a.cwd}
		if id != "" {
			method, p = "thread/resume", map[string]any{"threadId": id, "cwd": a.cwd}
		}
		maps.Copy(p, params)
		var info server.ThreadInfo
		err := a.conn.c.Call(context.Background(), method, p, &info)
		return info, err
	}
	cn, info, err := a.dialWorker(id, params)
	if err != nil {
		return info, err
	}
	if cn != nil {
		old := a.conn
		a.use(cn)
		if old != nil {
			old.c.Close()
		}
	}
	return info, nil
}

// dialWorker finds or starts the worker of session id and connects to
// it; for a session another process writes, it reads it read-only (no
// connection).
func (a *App) dialWorker(id string, params map[string]any) (*conn, server.ThreadInfo, error) {
	var args []string
	if deferred, _ := params["deferStart"].(bool); deferred {
		args = append(args, "-defer-start")
	}
	for _, k := range []string{"model", "effort"} {
		if v, _ := params[k].(string); v != "" {
			args = append(args, "-"+k, v)
		}
	}
	cwd := a.cwd
	if dir, _ := params["cwd"].(string); dir != "" {
		cwd = dir
	}
	w, readOnly, err := daemon.StartWorker(id, cwd, args)
	if err != nil {
		return nil, server.ThreadInfo{}, err
	}
	if readOnly != "" {
		info, err := server.ReadOffline(id)
		info.ReadOnly = readOnly
		return nil, info, err
	}
	nc, err := daemon.DialWorker(w)
	if err != nil {
		return nil, server.ThreadInfo{}, err
	}
	cn, err := dialConn(server.NewClient(nc), nil)
	if err != nil {
		return nil, server.ThreadInfo{}, err
	}
	var info server.ThreadInfo
	if err := cn.c.Call(context.Background(), "thread/attach", map[string]any{"threadId": w.Session}, &info); err != nil {
		cn.c.Close()
		return nil, server.ThreadInfo{}, err
	}
	if info.ID == "" {
		cn.c.Close()
		return nil, info, errors.New("the session's runtime sent nothing")
	}
	return cn, info, nil
}

// centerClient is a discovery facade, not an execution worker. Its inventory
// and session mutations use the same protocol as remote frontends.
func centerClient() (*server.Client, func()) {
	s := server.New(Version, "")
	if daemon.Usable() {
		s.Workers = daemon.Routes()
	}
	c := server.Connect(context.Background(), s)
	return c, func() { c.Close(); s.Close() }
}
