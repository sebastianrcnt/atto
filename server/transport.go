package server

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/config"
)

// TokenPath stores the HTTP server's bearer token.
func TokenPath() string { return filepath.Join(config.Dir(), "server-token") }

// NewToken returns a random token of n bytes, hex encoded.
func NewToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// LoadOrCreateToken returns the persistent server token.
func LoadOrCreateToken() (string, error) {
	if b, err := os.ReadFile(TokenPath()); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
		return strings.TrimSpace(string(b)), nil
	}
	tok, err := NewToken(24)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		return "", err
	}
	return tok, os.WriteFile(TokenPath(), []byte(tok+"\n"), 0o600)
}

// IsLoopback reports whether addr only listens on the local machine.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
