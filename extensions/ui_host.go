package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/sebastianrcnt/atto/ui"
)

// PortableHost queues UI work on the session lane. Implementations must not
// wait for that lane from an extension runtime (it may be waiting for boot).
type PortableHost interface {
	UIWork(func(*ui.Registry) error, func(error))
	Store(context.Context, string, string, string, json.RawMessage) (json.RawMessage, error)
}

func UIWork(h Host, work func(*ui.Registry) error, done func(error)) {
	if p, ok := h.(PortableHost); ok {
		p.UIWork(work, done)
		return
	}
	done(fmt.Errorf("portable UI unavailable on this host"))
}

// MemoryStore is the standalone/noninteractive fallback; workers use the
// session writer instead. It enforces the same JSON and byte limits.
type MemoryStore struct {
	mu     sync.Mutex
	values map[string]map[string]json.RawMessage
}

func (s *MemoryStore) Do(owner, op, key string, value json.RawMessage) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]map[string]json.RawMessage{}
	}
	values := s.values[owner]
	if values == nil {
		values = map[string]json.RawMessage{}
		s.values[owner] = values
	}
	switch op {
	case "get":
		return append(json.RawMessage(nil), values[key]...), nil
	case "keys":
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b, _ := json.Marshal(keys)
		return b, nil
	case "delete":
		delete(values, key)
	case "set":
		if key == "" || len(key) > 128 {
			return nil, fmt.Errorf("store key must be 1..128 bytes")
		}
		if !json.Valid(value) || len(value) > 64<<10 {
			return nil, fmt.Errorf("store value must be JSON, at most 64 KiB")
		}
		total := len(key) + len(value)
		for k, v := range values {
			if k != key {
				total += len(k) + len(v)
			}
		}
		if total > 1<<20 {
			return nil, fmt.Errorf("store exceeds 1 MiB")
		}
		values[key] = append(json.RawMessage(nil), value...)
	default:
		return nil, fmt.Errorf("unknown store operation")
	}
	return nil, nil
}

// UIQueue is a nonblocking FIFO used by standalone hosts. Session workers use
// their existing lane instead. Work must not call back into the queue and wait.
type UIQueue struct {
	mu      sync.Mutex
	work    []func()
	running bool
}

func (q *UIQueue) Post(fn func()) {
	q.mu.Lock()
	q.work = append(q.work, fn)
	if q.running {
		q.mu.Unlock()
		return
	}
	q.running = true
	q.mu.Unlock()
	go func() {
		for {
			q.mu.Lock()
			if len(q.work) == 0 {
				q.running = false
				q.mu.Unlock()
				return
			}
			fn := q.work[0]
			q.work[0] = nil
			q.work = q.work[1:]
			q.mu.Unlock()
			fn()
		}
	}()
}
