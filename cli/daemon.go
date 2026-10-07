package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sebastianrcnt/atto/daemon"
)

const daemonUsage = `usage:
  atto daemon [status]       the sessions the daemon's workers run
  atto daemon stop [-force]  stop the daemon (-force: even with sessions running)

The daemon runs each session a client opens in a worker of its own and
exits when its last worker ends; it starts by itself and is never
installed as a service.`

// RunDaemon implements "atto daemon".
func RunDaemon(args []string, out io.Writer) error {
	fs := newFlags("daemon")
	force := fs.Bool("force", false, "stop: end running sessions too")
	words, err := parseWords(fs, args)
	if err != nil {
		return fmt.Errorf("%s", daemonUsage)
	}
	sub := "status"
	if len(words) > 0 {
		sub = words[0]
	}
	switch {
	case sub == "status" && len(words) <= 1:
		return listWorkers(out)
	case sub == "stop" && len(words) == 1:
		err := daemon.Stop(*force)
		if errors.Is(err, daemon.ErrUnavailable) {
			fmt.Fprintln(out, "no daemon is running")
			return nil
		}
		return err
	}
	return fmt.Errorf("%s", daemonUsage)
}

// RunDaemonServe implements the hidden "atto _daemon", which clients
// start.
func RunDaemonServe([]string, io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return daemon.Serve(exe)
}

// listWorkers lists the sessions the daemon's workers run.
func listWorkers(out io.Writer) error {
	ws, err := daemon.Workers()
	if err != nil {
		return err
	}
	if len(ws) == 0 {
		fmt.Fprintln(out, "no session is running in the daemon")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tPID\tDIRECTORY\tSTARTED")
	home, _ := os.UserHomeDir()
	for _, w := range ws {
		dir := w.Cwd
		if home != "" && strings.HasPrefix(dir, home) {
			dir = "~" + dir[len(home):]
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s ago\n", w.Session, w.PID, dir, age(w.Started))
	}
	return tw.Flush()
}

func age(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return d.String()
	case d < time.Hour:
		return d.Round(time.Minute).String()
	}
	return d.Round(time.Hour).String()
}
