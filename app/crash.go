package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/update"
)

// writeCrash keeps a panic of the render loop in ~/.atto/logs, where it
// survives the terminal it happened on.
func writeCrash(v any, stack []byte) {
	dir := filepath.Join(config.Dir(), "logs")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	path := filepath.Join(dir, "crash-"+time.Now().Format("20060102-150405")+".log")
	if os.WriteFile(path, fmt.Appendf(nil, "atto %s panicked: %v\n\n%s", update.Describe(), v, stack), 0o600) == nil {
		fmt.Fprintf(os.Stderr, "atto: crashed; details in %s\n", path)
	}
}
