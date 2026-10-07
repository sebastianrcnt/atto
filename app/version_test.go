package app

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/server"
)

// A runtime that speaks no revision this terminal does is refused with a
// message that says what to do, rather than half working.
func TestTerminalRefusesOtherProtocolRevision(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	go func() { // an atto of another age
		sc := bufio.NewScanner(b)
		for sc.Scan() {
			var req struct {
				ID int `json:"id"`
			}
			_ = json.Unmarshal(sc.Bytes(), &req)
			resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32000, "message": "this atto speaks protocol 7 to 9", "data": map[string]any{"reason": server.ReasonUnsupportedProtocol}}})
			_, _ = b.Write(append(resp, '\n'))
		}
	}()
	_, err := dialConn(server.NewClient(a), nil)
	if err == nil || !strings.Contains(err.Error(), "another version") || !strings.Contains(err.Error(), "protocol 7 to 9") {
		t.Fatalf("dial: %v", err)
	}
}
