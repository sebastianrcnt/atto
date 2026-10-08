package server

import "context"

// beginForTest supplies an execution body to test cancellation and idle-memory
// ownership independently of model calls. The run still uses the runtime lane.
func beginForTest(s *Server, t *thread, fn func(context.Context, func(any)) error) (string, error) {
	var id string
	err := t.call(func() error {
		if t.turns.Busy || t.closing {
			return failure(ReasonBusy, "a turn is already running")
		}
		t.start("turn", "Thinking", fn)
		id = t.turnID
		return nil
	})
	return id, err
}
