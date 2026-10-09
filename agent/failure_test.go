package agent

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// The same 5xx message twice in a row ends the turn after two requests, and
// the error says why.
func TestIdentical5xxStopsAfterSecondAttempt(t *testing.T) {
	noWait(t)
	srv, count := scriptedServer(t, status(503, visionDown), status(503, visionDown), okReply)
	a := newTestAgent(srv.URL)
	retries := 0
	err := a.Run(context.Background(), "go", func(ev any) {
		if _, ok := ev.(StreamRetry); ok {
			retries++
		}
	})
	if err == nil || count() != 2 || retries != 1 {
		t.Fatalf("err %v after %d requests, %d retries", err, count(), retries)
	}
	if msg := err.Error(); !strings.Contains(msg, "strata-vision") || !strings.Contains(msg, "same error twice") || !strings.Contains(msg, "did not try again") {
		t.Fatalf("error %q", msg)
	}
	// Still a passing failure to a goal: it may retry the turn later.
	if ai.IsPermanent(err) {
		t.Fatalf("%q should stay transient", err)
	}
}

// 5xx answers that differ, say nothing, or say the server is busy are
// retried as before.
func TestOther5xxKeepAllAttempts(t *testing.T) {
	noWait(t)
	var differing []func(http.ResponseWriter)
	for i := range streamRetries + 1 {
		differing = append(differing, status(500, fmt.Sprintf("boom %d", i)))
	}
	empty := func(w http.ResponseWriter) { w.WriteHeader(503) }
	for name, replies := range map[string][]func(http.ResponseWriter){
		"differing":  differing,
		"overloaded": {status(503, "overloaded")},
		"no message": {empty},
		"generic":    {status(500, "Internal Server Error")},
		"mixed":      {status(500, "boom"), status(502, "boom")},
	} {
		t.Run(name, func(t *testing.T) {
			srv, count := scriptedServer(t, replies...)
			a := newTestAgent(srv.URL)
			err := a.Run(context.Background(), "go", func(any) {})
			if name == "mixed" {
				// A different status in between: the third answer repeats the second.
				if err == nil || count() != 3 {
					t.Fatalf("err %v after %d requests", err, count())
				}
				return
			}
			if err == nil || count() != streamRetries+1 {
				t.Fatalf("err %v after %d requests", err, count())
			}
			if strings.Contains(err.Error(), "same error twice") {
				t.Fatalf("error %q", err)
			}
		})
	}
}

// A 4xx that says the request is wrong is not sent again; those that pass
// are.
func TestClientErrorsAreNotRetried(t *testing.T) {
	noWait(t)
	for _, code := range []int{400, 401, 403, 404, 410, 413, 422} {
		srv, count := scriptedServer(t, status(code, "bad request"), okReply)
		a := newTestAgent(srv.URL)
		if err := a.Run(context.Background(), "go", func(any) {}); err == nil || count() != 1 {
			t.Fatalf("%d: err %v after %d requests", code, err, count())
		}
	}
	for _, code := range []int{408, 409, 425, 429} {
		srv, count := scriptedServer(t, status(code, "try later"), okReply)
		a := newTestAgent(srv.URL)
		if err := a.Run(context.Background(), "go", func(any) {}); err != nil || count() != 2 {
			t.Fatalf("%d: err %v after %d requests", code, err, count())
		}
	}
}

// When the request carried an image and the failure is about images, the
// error says the server could not process it; the image stays in the
// conversation.
func TestImageFailureHint(t *testing.T) {
	noWait(t)
	im := testPNG(t)
	const hint = "the server could not process the image"
	for _, c := range []struct {
		name   string
		images bool
		reply  string
		hint   bool
	}{
		{"image and vision error", true, visionDown, true},
		{"image, unrelated error", true, "model crashed", false},
		{"no image, vision error", false, visionDown, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, count := scriptedServer(t, status(503, c.reply))
			a := imageAgent(srv.URL, "text", "image")
			var rec []session.Entry
			a.Record = func(e session.Entry) { rec = append(rec, e) }
			input := "look"
			if c.images {
				a.messages = append(a.messages, provider.Message{Role: "user", Content: "earlier", Images: []provider.Image{im}})
			}
			err := a.Run(context.Background(), input, func(any) {})
			if err == nil || count() != 2 {
				t.Fatalf("err %v after %d requests", err, count())
			}
			if got := strings.Contains(err.Error(), hint); got != c.hint {
				t.Fatalf("hint %v, want %v: %q", got, c.hint, err)
			}
			if c.hint {
				if e := errorEntries(rec); len(e) != 1 || !strings.Contains(e[0].Error, hint) || e[0].HTTPStatus != 503 {
					t.Fatalf("entry %+v", e)
				}
				if n := len(a.messages[0].Images); n != 1 {
					t.Fatalf("the image was dropped: %+v", a.messages[0])
				}
			}
		})
	}
}
