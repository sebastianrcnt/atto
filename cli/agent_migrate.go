package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/sebastianrcnt/atto/agentmigrate"
	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/update"
)

// askMigrate is how the first agent command that finds the old layout asks
// whether to migrate. Tests replace it.
var askMigrate = func(out io.Writer, why string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	fmt.Fprintf(out, "Agent data has the old layout (%s).\nMigrating takes a backup of ~/.atto first and converts everything in one pass; every machine that shares this data must then run this atto or a newer one.\nMigrate now? [y/N] ", why)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")
}

// ensureAgentLayout makes sure the agent data has the layout this atto
// reads. The old layout is converted after asking on a terminal; anywhere
// else (a model's shell, a script) the command refuses and names the command
// to run. Data newer than this atto always refuses.
func ensureAgentLayout(out io.Writer) error {
	layout, why, err := agentstate.Detect()
	if err != nil {
		return err
	}
	if layout != agentstate.LayoutLegacy {
		return agentstate.Ready()
	}
	if config.InAgentCommand() || os.Getenv("ATTO_SESSION_ID") != "" || !askMigrate(out, why) {
		return fmt.Errorf("%w (%s): run `atto agent migrate` from your own terminal (it backs ~/.atto up first)", agentstate.ErrNeedsMigration, why)
	}
	return runAgentMigrate(out)
}

// runAgentMigrate is atto agent migrate.
func runAgentMigrate(out io.Writer) error {
	if config.InAgentCommand() || os.Getenv("ATTO_SESSION_ID") != "" {
		return errors.New("atto agent migrate cannot run inside an atto model shell (ATTO_AGENT/ATTO_SESSION_ID is set); run it from your own terminal")
	}
	r, err := agentmigrate.Run(agentmigrate.Options{Out: out, Version: update.Current(), Project: externalProject})
	if err != nil {
		return err
	}
	if !r.AlreadyCurrent {
		fmt.Fprintf(out, "Backup: %s\nIf anything looks wrong: atto restore -force %s\nEvery machine that shares ~/.atto must now run this atto or a newer one; old binaries must not run atto agent on this data.\n", r.Backup, r.Backup)
	}
	return nil
}
