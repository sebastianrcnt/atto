package extensions

import (
	"fmt"
	"github.com/sebastianrcnt/atto/config"
	"os"
	"strings"
	"time"
)

// log appends to the extensions log, which atto.log writes to too.
func (m *Manager) log(name, msg string) {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	path := config.ExtensionLogPath()
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	for line := range strings.SplitSeq(strings.TrimRight(msg, "\n"), "\n") {
		fmt.Fprintf(f, "%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), name, line)
	}
}
