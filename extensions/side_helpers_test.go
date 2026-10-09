package extensions

import (
	"encoding/json"
	"fmt"
	"github.com/sebastianrcnt/atto/config"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sideServer is a fake model server for atto.complete. The user message
// picks what it does: "slow" hangs until the client goes away, "500" fails,
// "wait" answers after 300ms; anything else is answered with "re:<prompt>".
type sideServer struct {
	*httptest.Server
	mu        sync.Mutex
	bodies    []map[string]any
	inflight  atomic.Int32
	maxFlight atomic.Int32
	canceled  atomic.Int32
}

func newSideServer(t *testing.T) *sideServer {
	s := &sideServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		n := s.inflight.Add(1)
		defer s.inflight.Add(-1)
		for {
			m := s.maxFlight.Load()
			if n <= m || s.maxFlight.CompareAndSwap(m, n) {
				break
			}
		}
		msgs, _ := body["messages"].([]any)
		last, _ := msgs[len(msgs)-1].(map[string]any)
		prompt, _ := last["content"].(string)
		switch prompt {
		case "slow":
			select {
			case <-r.Context().Done():
				s.canceled.Add(1)
			case <-time.After(10 * time.Second):
			}
			return
		case "500":
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		case "wait":
			time.Sleep(300 * time.Millisecond)
		case "thinks":
			if body["reasoning_effort"] != nil { // a model that always thinks
				http.Error(w, `{"error":{"message":"Unsupported value: 'none' is not supported with this model.","param":"reasoning.effort"}}`, http.StatusBadRequest)
				return
			}
		}
		reply, _ := json.Marshal("re:" + prompt)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", reply)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *sideServer) requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.bodies...)
}

// sideModels configures s/m at url and d/m at a server that is not there.
func sideModels(t *testing.T, url string) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	write(t, config.ModelsPath(), `{"providers":{
	  "s":{"baseUrl":"`+url+`","models":[{"id":"m","contextWindow":100000}]},
	  "d":{"baseUrl":"`+deadURL+`","models":[{"id":"m","contextWindow":100000}]}}}`)
}
