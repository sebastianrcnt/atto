package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/sebastianrcnt/atto/app"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/daemon"
)

const attachUsage = `usage:
  atto attach [ID|session]   show a running atto on this terminal (default:
                             the most recent one no terminal shows)
  atto attach -l             list the running attos

Interactive atto runs in a pane of the atto daemon, as a shell in tmux:
closing the terminal, a dropped SSH connection or /detach leaves it
running, and atto attach brings it back from any terminal of this user.`

const connectUsage = `usage:
  atto connect [session]     open a session in this terminal as a client of
                             its daemon worker (default: the latest session
                             here, else a new one)

Each session the daemon runs lives in a worker of its own; any number of
terminals can show it, each with its own editor. Leaving (/quit, closing
the terminal) leaves the session running; /close ends it.`

const daemonUsage = `usage:
  atto daemon [status]       the daemon's panes and sessions (as atto attach -l)
  atto daemon kill ID        end a pane's atto, as closing its terminal would
  atto daemon stop [-force]  stop the daemon (-force: even with panes running)

The daemon starts by itself when interactive atto needs it and exits when
its last pane ends; it is never installed as a service. "daemon": false in
settings.json (or ATTO_NO_DAEMON=1) runs atto directly in the terminal.`

// RunAttach implements "atto attach".
func RunAttach(args []string, out io.Writer) error {
	fs := newFlags("attach")
	list := fs.Bool("l", false, "list the running attos")
	words, err := parseInterleaved(fs, args)
	if err != nil || len(words) > 1 {
		return fmt.Errorf("%s", attachUsage)
	}
	if *list {
		return listPanes(out)
	}
	target := ""
	if len(words) == 1 {
		target = words[0]
	}
	code, note, err := daemon.Run(daemon.Hello{Op: "attach", Target: target})
	if errors.Is(err, daemon.ErrUnavailable) {
		return errors.New("no atto is running in the daemon")
	}
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	if code != 0 {
		return ExitCode(code)
	}
	return nil
}

// RunConnect implements "atto connect": the TUI as an independent client
// of a session's daemon worker, outside any pane.
func RunConnect(args []string, out io.Writer) error {
	if !daemon.Enabled() {
		return fmt.Errorf("session workers are unavailable here; use atto resume for an in-process session")
	}
	fs := newFlags("connect")
	words, err := parseInterleaved(fs, args)
	if err != nil || len(words) > 1 {
		return fmt.Errorf("%s", connectUsage)
	}
	opts := app.Options{Continue: true}
	if len(words) == 1 {
		opts.Session, opts.Continue = words[0], false
	}
	return app.Run(opts)
}

// RunDaemon implements "atto daemon".
func RunDaemon(args []string, out io.Writer) error {
	fs := newFlags("daemon")
	force := fs.Bool("force", false, "stop: end running panes too")
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
		return listPanes(out)
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

func listPanes(out io.Writer) error {
	panes, err := daemon.List()
	if err != nil {
		return err
	}
	if len(panes) == 0 {
		fmt.Fprintln(out, "no atto pane is running in the daemon")
		return listWorkers(out)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSESSION\tNAME\tDIRECTORY\tSHOWN\tSTARTED")
	for _, p := range panes {
		dir := core.ShortPath(p.Cwd)
		shown := "detached"
		if p.Clients > 0 {
			shown = strconv.Itoa(p.Clients) + " terminal"
			if p.Clients > 1 {
				shown += "s"
			}
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s ago\n", p.ID, orDash(p.Session), orDash(p.Name), dir, shown, age(p.Started))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return listWorkers(out)
}

// listWorkers lists the sessions the daemon's workers run.
func listWorkers(out io.Writer) error {
	ws, err := daemon.Workers()
	if err != nil || len(ws) == 0 {
		return err
	}
	fmt.Fprintln(out, "\nsessions (atto connect <session> opens one here):")
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tPID\tDIRECTORY\tCLIENTS\tSTATE\tVERSION\tSTARTED")
	for _, w := range ws {
		state := "idle"
		if w.Busy {
			state = "working"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t%s\t%s\t%s ago\n", w.Session, w.PID, w.Cwd, w.Clients, state, w.Version, age(w.Started))
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
// what was picked, in a fresh process (the center's terminal reader may
// still be blocked on stdin and would take the first keys): a running
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
	case pick.Pane > 0:
		argv = append(argv, "attach", strconv.Itoa(pick.Pane))
	case pick.Session != "":
		argv = append(argv, "-session", pick.Session)
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
	return syscall.Exec(exe, argv, os.Environ())
}
