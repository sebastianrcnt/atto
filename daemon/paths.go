package daemon

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sebastianrcnt/atto/config"
)

// RunDir holds the daemon's lock and, normally, its socket.
func RunDir() string { return filepath.Join(config.Dir(), "run") }

// LogPath is where the daemon writes its errors.
func LogPath() string { return filepath.Join(config.Dir(), "logs", "daemon.log") }

// maxSocketPath stays under the smallest sun_path (104 bytes on macOS).
const maxSocketPath = 100

// SocketPath is the daemon's Unix socket: in RunDir, or, when that path
// is too long for a socket (a deep ATTO_DIR), in a private directory under
// the temp dir named after it.
func SocketPath() string {
	p := filepath.Join(RunDir(), "daemon.sock")
	if len(p) <= maxSocketPath {
		return p
	}
	h := sha256.Sum256([]byte(config.Dir()))
	return filepath.Join(os.TempDir(), fmt.Sprintf("atto-%d", os.Getuid()), fmt.Sprintf("%x.sock", h[:8]))
}
