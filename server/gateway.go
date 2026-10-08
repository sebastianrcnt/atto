package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/sebastianrcnt/atto/images"
)

// A scoped gateway serves one session of a server over HTTP, the way the
// terminal's /remote link does: the web client follows the session the
// terminal shows (Scope.Thread), as "live" (initialize says {live: true,
// threadId}); input is sent as if typed (turn/start and turn/steer are
// input/submit), and when the terminal shows another session clients are
// told (thread/switched, which the terminal publishes with Switched).
// The gateway is a view policy over the same runtime every client uses:
// stopping it stops no session.

// Scope is what a scoped gateway serves.
type Scope struct {
	// Thread is the session to serve now.
	Thread func() string
	// Local runs a command of the terminal typed in the web client
	// (/clear); false when it is not one. It may be nil.
	Local func(text string) bool
}

type scopeKey struct{}

// ScopedHandler is HTTPHandler limited to the session sc names.
func (s *Server) ScopedHandler(token string, sc Scope) http.Handler {
	h := s.HTTPHandler(token)
	// A link shares one client identity, distinct from other scoped links.
	web := &clientConn{id: fmt.Sprintf("r%d", clientSeq.Add(1)), name: "web", interactive: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(context.WithValue(r.Context(), scopeKey{}, &sc), clientKey{}, web)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Switched tells a scoped gateway's clients the terminal shows thread id
// now, after prev.
func (s *Server) Switched(id, prev string) {
	s.publish("thread/switched", map[string]any{"threadId": id, "previousThreadId": prev})
}

// scopedCall serves a request of a scoped gateway's client.
func (s *Server) scopedCall(ctx context.Context, sc *Scope, method string, p threadParams) (any, error, bool) {
	if sc.Thread == nil {
		return nil, invalid("the live session is unavailable"), true
	}
	id := sc.Thread()
	if p.ThreadID != "" && p.ThreadID != id && method != "initialize" && method != "models/list" {
		return nil, invalid("thread %q is no longer the live session (now %q): thread/read it", p.ThreadID, id), true
	}
	p.ThreadID = id
	t, err := s.thread(id)
	if err != nil {
		return nil, err, true
	}
	switch method {
	case "initialized", "ping":
		return nil, nil, true
	case "initialize":
		out, err := s.initialize(ctx, p, map[string]any{"live": true, "threadId": id})
		return out, err, true
	case "thread/start":
		return nil, failure(ReasonUnsupported, "this is atto's live session: send /clear to start a new conversation"), true
	case "thread/list":
		var info ThreadInfo
		err := t.call(func() error { info = t.info(); return nil })
		return map[string]any{"threads": []map[string]any{{"threadId": info.ID, "name": info.Name, "cwd": info.Cwd, "loaded": true, "live": true}}}, err, true
	case "thread/read", "thread/resume", "thread/attach":
		var info ThreadInfo
		err := t.call(func() error { info = t.snapshot(); return nil })
		info.Live = true
		return info, err, true
	case "turn/start", "turn/steer":
		if strings.TrimSpace(p.Input) == "" && len(p.Images) == 0 {
			return nil, invalid("input is required"), true
		}
		if strings.HasPrefix(p.Input, "/") && len(p.Images) == 0 && sc.Local != nil && sc.Local(p.Input) {
			return map[string]any{"status": StatusDone}, nil, true
		}
		var out any
		err := t.call(func() error {
			imgs, err := turnImages(p.Images, t.model())
			if err != nil {
				return err
			}
			r, err := t.submit(clientOf(ctx), images.WithPlaceholders(p.Input, imgs), imgs, "auto")
			out = r
			return err
		})
		return out, err, true
	case "thread/compact":
		var out any
		err := t.call(func() error {
			r, err := t.submit(clientOf(ctx), "/compact", nil, "auto")
			out = map[string]any{"turnId": r.TurnID}
			return err
		})
		return out, err, true
	case "thread/rollback":
		var out any
		err := t.call(func() error {
			var err error
			out, err = t.rollback(clientOf(ctx), p.NumTurns)
			return err
		})
		if err == nil {
			r := out.(struct {
				ThreadInfo
				Input string `json:"input"`
			})
			r.Live = true
			out = r
			s.Switched(id, id) // the web client reads the branch again
		}
		return out, err, true
	}
	return nil, nil, false
}

func scopeOf(ctx context.Context) *Scope {
	sc, _ := ctx.Value(scopeKey{}).(*Scope)
	return sc
}

// event keeps unrelated sessions out of a scoped link, including replay.
func (sc *Scope) event(raw []byte) bool {
	var n Notification
	if json.Unmarshal(raw, &n) != nil || sc.Thread == nil {
		return false
	}
	if n.Method == "events/reset" {
		return true
	}
	return n.ThreadID() == sc.Thread()
}
