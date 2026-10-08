package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/sebastianrcnt/atto/config"
)

// parseInterleaved parses flags that may come before, between or after
// positional words, so "delete <id> -y" works as well as "delete -y <id>".
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// requireUserApproval keeps project trust decisions in the user's terminal.
func requireUserApproval(action, command string) error {
	if config.InAgent() || os.Getenv("ATTO_SESSION_ID") != "" {
		return fmt.Errorf("atto: %s by the user, not from an agent's shell (ATTO_AGENT or ATTO_SESSION_ID is set). Ask the user to run: %s", action, command)
	}
	return nil
}
