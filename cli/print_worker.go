package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
)

// atto -p on a session a daemon worker runs (an interactive session left
// running, or one a terminal shows): the worker owns the session's writer,
// so the prompt goes to it as a client would send it, and the answer comes
// back from its notifications. This is the first step of print mode as a
// protocol client (docs/tui-as-client.md, phase H); other runs still run
// their own agent (RunPrint).

// workerFor is the worker of the session -session or -c names, if a
// daemon worker runs it.
func workerFor(o PrintOptions) (daemon.Worker, bool) {
	if o.Background || o.NoSave || o.Subagent != nil || (o.Resume == "" && !o.Continue) {
		return daemon.Worker{}, false
	}
	id := o.Resume
	if id == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return daemon.Worker{}, false
		}
		s, ok := session.Latest(cwd)
		if !ok {
			return daemon.Worker{}, false
		}
		id = s.ID
	} else if path, err := session.Find(id); err == nil {
		if s, err := session.Summarize(path); err == nil {
			id = s.ID
		}
	}
	ws, _ := daemon.Workers()
	for _, w := range ws {
		if w.Session == id {
			return w, true
		}
	}
	return daemon.Worker{}, false
}

// printViaWorker sends o's prompt to the session's worker and prints what
// the turn answers, until the session is idle again: text (the answer) or
// json (the result object).
func printViaWorker(o PrintOptions, w daemon.Worker, out, errOut io.Writer) error {
	if o.Format == "stream-json" {
		return errors.New("this session runs in the atto daemon: use -output-format text or json, or a client of the session")
	}
	if o.Goal != "" || len(o.Images) > 0 || o.Model != "" || o.Effort != "" || o.MaxSteps > 0 {
		return errors.New("this session runs in the atto daemon: -goal, -image, -m, -effort and -max-steps are not taken there; use a client of the session")
	}
	nc, err := daemon.DialWorker(w)
	if err != nil {
		return err
	}
	c := server.NewClient(nc)
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{server.ProtocolVersion}, "clientInfo": map[string]string{"name": "atto-print"}}, nil); err != nil {
		return err
	}
	var info server.ThreadInfo
	if err := c.Call(ctx, "thread/attach", map[string]any{"threadId": w.Session}, &info); err != nil {
		return err
	}
	began := time.Now()
	var sub struct {
		Status string `json:"status"`
	}
	if err := c.Call(ctx, "input/submit", map[string]any{"threadId": w.Session, "input": o.Prompt, "intent": "auto"}, &sub); err != nil {
		return err
	}
	res := printResult{Type: "result", SessionID: w.Session, Model: info.Model}
	var answer strings.Builder
	var failed string
	busy := true
	for busy {
		select {
		case <-ctx.Done():
			_ = c.Call(context.Background(), "turn/interrupt", map[string]any{"threadId": w.Session, "mode": "cancel"}, nil)
			return ctx.Err()
		case n, ok := <-c.Events():
			if !ok {
				return errors.New("the session's runtime went away")
			}
			if n.ThreadID() != w.Session {
				continue
			}
			var p struct {
				Item   *server.Item `json:"item"`
				Status string       `json:"status"`
				Error  string       `json:"error"`
			}
			_ = json.Unmarshal(n.Params, &p)
			switch n.Method {
			case "item/completed":
				if p.Item != nil && p.Item.Type == server.ItemAgent {
					answer.Reset()
					answer.WriteString(p.Item.Text)
				}
			case "turn/completed":
				if p.Status != "completed" {
					failed = p.Error
					if failed == "" {
						failed = p.Status
					}
				}
			case "thread/updated":
				var u struct {
					Thread server.ThreadInfo `json:"thread"`
				}
				_ = json.Unmarshal(n.Params, &u)
				pend := u.Thread.Pending
				busy = u.Thread.Busy || (pend != nil && len(pend.Queued) > 0)
			}
		}
	}
	res.DurationMs = time.Since(began).Milliseconds()
	res.Result = strings.TrimSpace(answer.String())
	res.Subtype = "success"
	if failed != "" {
		res.Subtype, res.IsError, res.Error = "error", true, failed
	}
	if o.Format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	} else {
		fmt.Fprintln(out, res.Result)
		if failed != "" {
			fmt.Fprintln(errOut, "atto:", failed)
		}
	}
	if failed != "" {
		return ErrPrintFailed
	}
	return nil
}
