package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/sebastianrcnt/atto/app"
	"github.com/sebastianrcnt/atto/daemon"
)

const daemonUsage = `usage:
  atto daemon [status]        list session workers
  atto daemon kill SESSION    close a worker and end its work
  atto daemon stop [-force]   stop (-force: end running sessions too)

The daemon starts on demand and exits after its last worker retires.
"daemon": false or ATTO_NO_DAEMON=1 runs sessions in the terminal process.`

// RunDaemon implements "atto daemon".
func RunDaemon(args []string, out io.Writer) error {
	fs := newFlags("daemon")
	force := fs.Bool("force", false, "stop: end running sessions too")
	words, err := parseInterleaved(fs, args)
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
	case sub == "kill" && len(words) == 2:
		return daemon.Kill(words[1])
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
	ws, err := daemon.Status()
	if err != nil {
		return err
	}
	if len(ws) == 0 {
		fmt.Fprintln(out, "no session worker is running in the daemon")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tNAME\tDIRECTORY\tCLIENTS\tSTATE\tVERSION\tSTARTED")
	for _, w := range ws {
		state := w.State
		if state == "" {
			state = "idle"
			if w.Busy {
				state = "working"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s ago\n", w.Session, orDash(w.Name), w.Cwd, w.Clients, state, w.Version, age(w.Started))
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

// RunAgents implements "atto agents": the agent center by itself, then
// what was picked, in a fresh process (on Windows a child: stopping the
// center's terminal cancels its pending console read, so the child gets
// every key): a running
// session attached, a saved one opened, or a new one started, in the
// daemon.
func RunAgents(args []string, out io.Writer) error {
	if len(args) > 0 {
		return errors.New("usage: atto agents   (every atto session, running or saved; enter opens it)")
	}
	pick, err := app.RunAgents()
	if err != nil {
		return err
	}
	argv := []string{"atto"}
	switch {
	case pick.Session != "":
		argv = append(argv, "resume", pick.Session)
	case pick.New:
	default:
		return nil
	}
	if pick.Cwd != "" {
		if err := os.Chdir(pick.Cwd); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return execSelf(exe, argv)
}
