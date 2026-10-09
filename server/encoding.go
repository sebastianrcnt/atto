package server

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
)

// encoding/json.Encoder buffers an entire value even on Go's v2 backend.
// MarshalWrite is the genuinely streaming path; v1 options preserve wire shape.
func encodeJSON(w io.Writer, v any) error {
	if ws, ok := w.(*wsConn); ok {
		fragments := &wsJSONWriter{conn: ws}
		if err := jsonv2.MarshalWrite(fragments, v, json.DefaultOptionsV1()); err != nil {
			return err
		}
		return fragments.finish()
	}
	if err := jsonv2.MarshalWrite(w, v, json.DefaultOptionsV1()); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// Small responses retain one-frame compatibility. Large responses use RFC 6455
// continuation frames, bounded to 64 KiB; browsers/JDK see one JSON message.
type wsJSONWriter struct {
	conn    *wsConn
	pending []byte
	started bool
}

func (w *wsJSONWriter) Write(p []byte) (int, error) {
	n := len(p)
	if !w.started && len(w.pending)+n <= 64<<10 {
		w.pending = append(w.pending, p...)
		return n, nil
	}
	if !w.started {
		if err := w.conn.frameFragment(1, w.pending, false); err != nil {
			return 0, err
		}
		w.pending = nil
		w.started = true
	}
	for len(p) > 0 {
		count := min(len(p), 64<<10)
		if err := w.conn.frameFragment(0, p[:count], false); err != nil {
			return 0, err
		}
		p = p[count:]
	}
	return n, nil
}
func (w *wsJSONWriter) finish() error {
	if !w.started {
		return w.conn.frame(1, w.pending)
	}
	return w.conn.frameFragment(0, nil, true)
}
