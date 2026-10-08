package agentstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sebastianrcnt/atto/fsutil"
)

// RequestInterrupt asks one turn to stop without terminating its process
// tree. The turn number prevents an old request from interrupting its successor.
func RequestInterrupt(parent, name string, turn int) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if turn <= 0 {
		return fmt.Errorf("invalid turn %d", turn)
	}
	path := turnPath(parent, name) + ".interrupt"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(path, []byte(strconv.Itoa(turn)), 0o644)
}

// Interrupted reports whether this turn has a user interrupt request.
func Interrupted(parent, name string, turn int) bool {
	if ValidName(name) != nil || turn <= 0 {
		return false
	}
	data, err := os.ReadFile(turnPath(parent, name) + ".interrupt")
	return err == nil && strings.TrimSpace(string(data)) == strconv.Itoa(turn)
}
