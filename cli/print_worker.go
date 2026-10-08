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
func workerFor(o PrintOptions) (daemon.Worker, bool, error) {
	if !daemon.Enabled() || o.Background || o.NoSave || o.Worker != nil || (o.Resume == "" && !o.Continue) {
		return daemon.Worker{}, false, nil
	}
	id := o.Resume
	if id == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return daemon.Worker{}, false, nil
		}
		s, ok := session.Latest(cwd)
		if !ok {
			return daemon.Worker{}, false, nil
		}
		id = s.ID
	} else if path, err := session.Find(id); err == nil {
		if s, err := session.Summarize(path); err == nil {
			id = s.ID
		}
	}
	ws, err := daemon.Workers()
	if err != nil {
		return daemon.Worker{}, false, err
	}
	var found *daemon.Worker
	for _, w := range ws {
		if w.Session == id {
			return w, true, nil
		}
		if strings.HasPrefix(w.Session, id) {
			if found != nil {
				return daemon.Worker{}, false, fmt.Errorf("ambiguous session %q", id)
			}
			copy := w
			found = &copy
		}
	}
	if found != nil {
		return *found, true, nil
	}
	return daemon.Worker{}, false, nil
}

// printViaWorker sends o's prompt to the session's worker and prints what
// the turn answers, until the session is idle again: text (the answer) or
// json (the result object).
func printViaWorker(o PrintOptions, w daemon.Worker, out, errOut io.Writer) error {
	if o.Format == "stream-json" {
		return errors.New("this session runs in the atto daemon: use -output-format text or json, or atto connect")
	}
	if o.Goal != "" || len(o.Images) > 0 || o.Model != "" || o.Effort != "" || o.MaxSteps > 0 {
		return errors.New("this session runs in the atto daemon: -goal, -image, -m, -effort and -max-steps are not taken there; use atto connect")
	}
	nc, err := daemon.DialWorker(w)
	if err != nil {
		return err
	}
	c := server.NewClient(nc)
	defer c.Close()
	return printOnClient(o, w.Session, c, out, errOut)
}

func printOnClient(o PrintOptions, id string, c *server.Client, out, errOut io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{server.ProtocolVersion}, "clientInfo": map[string]string{"name": "atto-print"}}, nil); err != nil {
		return err
	}
	var info server.ThreadInfo
	if err := c.Call(ctx, "thread/attach", map[string]any{"threadId": id}, &info); err != nil {
		return err
	}
	began := time.Now()
	var sub struct {
		Status  string `json:"status"`
		InputID string `json:"inputId"`
		TurnID  string `json:"turnId"`
	}
	if err := c.Call(ctx, "input/submit", map[string]any{"threadId": id, "input": o.Prompt, "intent": "auto"}, &sub); err != nil {
		return err
	}
	res := printResult{Type: "result", SessionID: id, Model: info.Model}
	var answer strings.Builder
	var failed string
	busy := sub.Status == server.StatusStarted || sub.Status == server.StatusSteered || sub.Status == server.StatusQueued
	completed := false
	for busy {
		select {
		case <-ctx.Done():
			cancelCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = c.Call(cancelCtx, "turn/interrupt", map[string]any{"threadId": id, "mode": "cancel"}, nil)
			cancel()
			return ctx.Err()
		case n, ok := <-c.Events():
			if !ok {
				return errors.New("the session's runtime went away")
			}
			if n.ThreadID() != id || n.EventID <= info.EventID {
				continue
			}
			var p struct {
				Item   *server.Item  `json:"item"`
				TurnID string        `json:"turnId"`
				Step   *server.Usage `json:"step"`
				Status string        `json:"status"`
				Error  string        `json:"error"`
			}
			_ = json.Unmarshal(n.Params, &p)
			switch n.Method {
			case "item/completed":
				if p.Item != nil && p.Item.Type == server.ItemAgent {
					answer.Reset()
					answer.WriteString(p.Item.Text)
				}
			case "thread/usage":
				if p.Step != nil {
					res.NumSteps++
					res.Usage.InputTokens += p.Step.InputTokens
					res.Usage.CachedInputTokens += p.Step.CachedInputTokens
					res.Usage.OutputTokens += p.Step.OutputTokens
				}
			case "turn/completed":
				if sub.TurnID != "" && sub.TurnID != p.TurnID {
					continue
				}
				completed = true
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
				// Attach and submit may leave idle notifications in the queue.
				// Only a completion can make this invocation finish.
				if completed {
					busy = u.Thread.Busy || (pend != nil && len(pend.Queued) > 0 && !pend.Paused)
				}
			}
		}
	}
	if sub.Status == server.StatusDone && sub.InputID == "" && !strings.HasPrefix(o.Prompt, "/") {
		failed = "the session did not start a turn; use atto connect to check its model and state"
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
