package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

// mainServer answers each request with the next reply: reasoning and text.
// It keeps the request bodies.
type mainServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

type reply struct{ reasoning, text string }

func newMainServer(t *testing.T, replies ...reply) *mainServer {
	s := &mainServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(raw))
		i := min(len(s.bodies), len(replies)) - 1
		s.mu.Unlock()
		rep := replies[i]
		if rep.reasoning != "" {
			j, _ := json.Marshal(rep.reasoning)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":%s}}]}\n\n", j)
		}
		j, _ := json.Marshal(rep.text)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", j)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *mainServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

// extApp is a terminal on a new session of a home with extension src,
// its model t/m served by srv (extra adds providers to models.json).
func extApp(t *testing.T, src string, srv *mainServer, extra string) *App {
	t.Helper()
	cwd, _ := testEnv(t)
	writeTestFile(t, config.ModelsPath(), `{"providers":{"t":{"baseUrl":"`+srv.URL+`","models":[{"id":"m","contextWindow":100000}]}`+extra+`}}`)
	writeTestFile(t, filepath.Join(config.Dir(), "settings.json"), `{"defaultProvider":"t","defaultModel":"m"}`)
	if src != "" {
		writeTestFile(t, filepath.Join(config.ExtensionsDir(), "demo.ts"), src)
	}
	return startApp(t, cwd)
}
