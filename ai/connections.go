package ai

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// Model requests share their own pool: connections opened on an old
// network must not survive several retries after the network changes.
var modelTransport = newModelTransport()
var modelClient = &http.Client{Transport: modelTransport}

func newModelTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.IdleConnTimeout = 30 * time.Second
	if t.HTTP2 == nil {
		t.HTTP2 = &http.HTTP2Config{}
	}
	t.HTTP2.SendPingTimeout = 15 * time.Second
	t.HTTP2.PingTimeout = 5 * time.Second
	return t
}

// ResetConnections retires idle model connections so the next request
// dials on the current network. Active requests are left alone. It is safe
// to call while other sessions make requests.
func ResetConnections() { modelTransport.CloseIdleConnections() }

// IsConnectionError reports a failed connection or truncated stream, as
// opposed to a provider's HTTP error. A retry should retire the old pool.
func IsConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if _, ok := errors.AsType[*ProviderError](err); ok {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, ErrStreamStalled) {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	// net/http's HTTP/2 error types are internal. Their messages also
	// cover compatible transports without adding an x/net dependency.
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "stream error: stream id ") || strings.Contains(s, "connection error: ") ||
		strings.Contains(s, "http2:") && (strings.Contains(s, "goaway") || strings.Contains(s, "connection")) ||
		strings.Contains(s, "stream ended without finish_reason") || strings.Contains(s, "stream ended without a stop reason")
}
