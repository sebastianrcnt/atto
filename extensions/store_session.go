package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"

	"github.com/sebastianrcnt/atto/session"
)

// SessionStore serializes standalone writes through the same session writer as
// model entries. Worker hosts queue writes on their lane instead.
func SessionStore(w *session.Writer) func(context.Context, string, string, string, json.RawMessage) (json.RawMessage, error) {
	var mu sync.Mutex
	return func(ctx context.Context, owner, op, key string, value json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var s MemoryStore
		err := session.VisitActive(w.Path, func(e session.Entry) error {
			if e.Type == session.TypeUIStore {
				op := "set"
				if e.StoreDeleted {
					op = "delete"
				}
				_, err := s.Do(e.Ext, op, e.StoreKey, e.StoreValue)
				return err
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		v, err := s.Do(owner, op, key, value)
		if err == nil && (op == "set" || op == "delete") {
			w.Append(session.Entry{Type: session.TypeUIStore, Ext: owner, StoreKey: key, StoreValue: value, StoreDeleted: op == "delete"})
			err = w.Err()
		}
		return v, err
	}
}
