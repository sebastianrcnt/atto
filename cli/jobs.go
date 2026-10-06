package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
)

const jobUsage = `usage:
  atto job start [-name text] [-notify REGEXP [-notify-limit N]] -- '<command>'
                                                run a command in the background (quote it so
                                                your shell passes ; && | through)
  atto job list                                 jobs of this session
  atto job output <id> [-tail N | -head N]      print captured output (default: last 50 lines)
  atto job wait <id> [-timeout 10m]             block until it ends (returns early on user input)
  atto job kill <id>|all                        stop it and everything it started
  atto monitor [-every 30s] [-until REGEXP | -on-change | -until-exit N] [-timeout 1h] [-name text] -- <command>
  atto timer in <duration> <message>            e.g. atto timer in 10m "check CI"
  atto timer at <HH:MM> <message>
  atto timer every <duration> [-count N] [-until HH:MM|duration] <message>
                                                recurring, minimum 1m; e.g. atto timer every 30m "check CI"
  atto timer list | atto timer cancel <id>      cancel also stops a recurring timer
  atto sleep <duration>                         wait; wakes early when an event or user input arrives

-notify makes a job post an [atto event] for each output line matching REGEXP
while it keeps running (e.g. tail -f app.log with -notify 'ERROR|panic').
Matches within about a second are batched into one event, and after
-notify-limit events (default 50) notifications stop; the full output stays
available through atto job output. The exit event still fires.

Recurring timers fire at fixed intervals from their schedule, so they do not
drift. If atto was not running for several intervals, the timer fires once and
reports how many it skipped. Skipped intervals count against -count.

When a job or monitor finishes, or a timer fires, atto tells the agent with
an [atto event] message. The session comes from $ATTO_SESSION_ID (set for
commands atto runs) or -session.`

// sessionFlag adds -session to fs, defaulting to $ATTO_SESSION_ID.
func sessionFlag(fs *flag.FlagSet) *string {
	return fs.String("session", os.Getenv("ATTO_SESSION_ID"), "session ID")
}

func requireSession(s string) error {
	if s == "" {
		return fmt.Errorf("no session: run this inside atto (ATTO_SESSION_ID is set for its commands) or pass -session")
	}
	return nil
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseID(s string) (int, error) {
	id, err := strconv.Atoi(strings.TrimPrefix(s, "#"))
	if err != nil {
		return 0, fmt.Errorf("bad job id %q", s)
	}
	return id, nil
}

// RunJob implements "atto job".
func RunJob(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", jobUsage)
	}
	sub, rest := args[0], args[1:]
	fs := newFlags("job " + sub)
	session := sessionFlag(fs)
	name := fs.String("name", "", "short label")
	tail := fs.Int("tail", 50, "last N lines")
	head := fs.Int("head", 0, "first N lines")
	timeout := fs.Duration("timeout", 0, "give up after this long")
	notify := fs.String("notify", "", "post an event for each output line matching this regexp")
	notifyLimit := fs.Int("notify-limit", jobs.DefaultNotifyLimit, "stop notifying after this many events")
	var parseErr error
	switch sub {
	case "output", "log", "wait", "kill", "stop":
		var pos []string
		pos, parseErr = parseInterleaved(fs, rest)
		if parseErr == nil {
			parseErr = fs.Parse(append([]string{"--"}, pos...))
		}
	default:
		parseErr = fs.Parse(rest)
	}
	if parseErr != nil {
		return fmt.Errorf("%v\n%s", parseErr, jobUsage)
	}
	if err := requireSession(*session); err != nil {
		return err
	}
	cwd, _ := os.Getwd()

	switch sub {
	case "start":
		cmd := strings.Join(fs.Args(), " ")
		var n *jobs.Notify
		if *notify != "" {
			n = &jobs.Notify{Pattern: *notify, Limit: *notifyLimit}
		}
		j, err := jobs.Start(*session, cwd, *name, cmd, nil, n)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "job %d %s (pid %d): %s\n", j.ID, j.Status, j.PID, j.Label())
		if n != nil {
			fmt.Fprintf(out, "Each output line matching /%s/ will arrive as an [atto event] (batched, at most %d events).\n", n.Pattern, max(n.Limit, 1))
		}
		fmt.Fprintf(out, "You will get an [atto event] when it exits. Output: atto job output %d · wait: atto job wait %d · stop: atto job kill %d\n", j.ID, j.ID, j.ID)
	case "list", "ls":
		list := jobs.List(*session)
		if len(list) == 0 {
			fmt.Fprintln(out, "no jobs")
			return nil
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tKIND\tSTATUS\tTIME\tCOMMAND")
		for _, j := range list {
			st := string(j.Status)
			if j.ExitCode != nil {
				st += fmt.Sprintf(" (%d)", *j.ExitCode)
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", j.ID, j.KindLabel(), st, j.Runtime(), j.Label())
		}
		return tw.Flush()
	case "output", "log":
		id, err := parseID(fs.Arg(0))
		if err != nil {
			return err
		}
		var text string
		if *head > 0 {
			text, err = jobs.Head(*session, id, *head)
		} else {
			text, err = jobs.Tail(*session, id, *tail)
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(out, text)
		if j, err := jobs.Get(*session, id); err == nil {
			fmt.Fprintf(out, "[job %d %s · %s · full log: %s]\n", id, j.Status, j.Runtime(), jobs.OutputPath(*session, id))
		}
	case "wait":
		id, err := parseID(fs.Arg(0))
		if err != nil {
			return err
		}
		j, why, err := jobs.Wait(*session, id, *timeout)
		if err != nil {
			return err
		}
		switch why {
		case "timeout":
			fmt.Fprintf(out, "job %d still running after %s\n", id, *timeout)
		case "woken":
			fmt.Fprintf(out, "stopped waiting: the user sent a message. job %d is still running.\n", id)
		default:
			t, _ := jobs.Tail(*session, id, 20)
			code := ""
			if j.ExitCode != nil {
				code = fmt.Sprintf(" with code %d", *j.ExitCode)
			}
			fmt.Fprintf(out, "job %d %s%s after %s\n%s\n", id, j.Status, code, j.Runtime(), t)
		}
	case "kill", "stop":
		if fs.Arg(0) == "all" {
			fmt.Fprintf(out, "stopped %d jobs\n", jobs.KillAll(*session))
			return nil
		}
		id, err := parseID(fs.Arg(0))
		if err != nil {
			return err
		}
		j, err := jobs.Kill(*session, id)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "job %d %s\n", id, j.Status)
	default:
		return fmt.Errorf("unknown subcommand %q\n%s", sub, jobUsage)
	}
	return nil
}

// RunMonitor implements "atto monitor".
func RunMonitor(args []string, out io.Writer) error {
	fs := newFlags("monitor")
	session := sessionFlag(fs)
	name := fs.String("name", "", "short label")
	every := fs.Duration("every", 30*time.Second, "check interval")
	until := fs.String("until", "", "stop when output matches this regexp")
	onChange := fs.Bool("on-change", false, "stop when output differs from the first check")
	untilExit := fs.Int("until-exit", -1, "stop when the command exits with this code")
	timeout := fs.Duration("timeout", 24*time.Hour, "give up after this long")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%v\n%s", err, jobUsage)
	}
	if err := requireSession(*session); err != nil {
		return err
	}
	m := &jobs.Monitor{Every: *every, Until: *until, OnChange: *onChange, Timeout: *timeout}
	if *untilExit >= 0 {
		m.UntilExit = untilExit
	}
	if m.Until == "" && !m.OnChange && m.UntilExit == nil {
		return fmt.Errorf("monitor needs a condition: -until REGEXP, -on-change or -until-exit N")
	}
	cwd, _ := os.Getwd()
	j, err := jobs.Start(*session, cwd, *name, strings.Join(fs.Args(), " "), m, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "monitor %d running every %s: %s\nYou will get an [atto event] when the condition holds. Stop: atto job kill %d\n", j.ID, m.Every, j.Label(), j.ID)
	return nil
}

// RunTimer implements "atto timer".
func RunTimer(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", jobUsage)
	}
	fs := newFlags("timer")
	session := sessionFlag(fs)
	count := fs.Int("count", 0, "stop after this many firings (every)")
	until := fs.String("until", "", "stop after HH:MM or this long from now (every)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := requireSession(*session); err != nil {
		return err
	}
	switch args[0] {
	case "every":
		// Flags usually come after the duration, which the first parse
		// stops at; parse the rest again into the same variables.
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: atto timer every <duration> [-count N] [-until HH:MM|duration] <message>")
		}
		every, err := time.ParseDuration(fs.Arg(0))
		if err != nil {
			return fmt.Errorf("bad interval %q: use a duration like 30m", fs.Arg(0))
		}
		fs2 := newFlags("timer every")
		fs2.IntVar(count, "count", *count, "")
		fs2.StringVar(until, "until", *until, "")
		if err := fs2.Parse(fs.Args()[1:]); err != nil {
			return err
		}
		if fs2.NArg() < 1 {
			return fmt.Errorf("usage: atto timer every <duration> [-count N] [-until HH:MM|duration] <message>")
		}
		now := time.Now()
		var stop time.Time
		if *until != "" {
			if stop, err = events.ParseWhen(*until, now); err != nil {
				return fmt.Errorf("-until: %w", err)
			}
		}
		t, err := events.AddRecurringTimer(*session, now, every, *count, stop, strings.Join(fs2.Args(), " "))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "timer %s set: first at %s, %s\n", t.ID, t.Due.Format("15:04:05"), t.Schedule())
	case "in", "at":
		if fs.NArg() < 2 {
			return fmt.Errorf("usage: atto timer %s <when> <message>", args[0])
		}
		due, err := events.ParseWhen(fs.Arg(0), time.Now())
		if err != nil {
			return err
		}
		t, err := events.AddTimer(*session, due, strings.Join(fs.Args()[1:], " "))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "timer %s set for %s (in %s)\n", t.ID, due.Format("15:04:05"), time.Until(due).Round(time.Second))
	case "list", "ls":
		ts := events.Timers(*session)
		if len(ts) == 0 {
			fmt.Fprintln(out, "no timers")
		}
		for _, t := range ts {
			sched := ""
			if t.Recurring() {
				sched = " [" + t.Schedule() + "]"
			}
			fmt.Fprintf(out, "%s  %s (in %s)%s  %s\n", t.ID, t.Due.Format("15:04:05"), time.Until(t.Due).Round(time.Second), sched, t.Message)
		}
	case "cancel", "rm":
		if err := events.CancelTimer(*session, fs.Arg(0)); err != nil {
			return err
		}
		fmt.Fprintf(out, "timer %s canceled\n", fs.Arg(0))
	default:
		return fmt.Errorf("unknown subcommand %q\n%s", args[0], jobUsage)
	}
	return nil
}

// RunSleep implements "atto sleep": it returns early when an event is
// pending or the user sends input, so the agent never oversleeps.
func RunSleep(args []string, out io.Writer) error {
	fs := newFlags("sleep")
	session := sessionFlag(fs)
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return fmt.Errorf("usage: atto sleep <duration>  (e.g. 5m)")
	}
	d, err := time.ParseDuration(fs.Arg(0))
	if err != nil {
		return err
	}
	start := time.Now()
	for time.Since(start) < d {
		if *session != "" {
			if events.Pending(*session) {
				fmt.Fprintf(out, "woke after %s: an event arrived (it will be delivered next)\n", time.Since(start).Round(time.Second))
				return nil
			}
			if events.WokenSince(*session, start) {
				fmt.Fprintf(out, "woke after %s: the user sent a message\n", time.Since(start).Round(time.Second))
				return nil
			}
		}
		time.Sleep(min(250*time.Millisecond, d-time.Since(start)))
	}
	fmt.Fprintf(out, "slept %s\n", d)
	return nil
}

// RunSupervise is the hidden entry point of a job supervisor process.
func RunSupervise(args []string, _ io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: atto _supervise <job dir>")
	}
	return jobs.Supervise(args[0])
}

// RunShellHost is the hidden entry point of a shell host: atto runs each
// command of its shell tool under one, so the command can move to the
// background (jobs.StartHost).
func RunShellHost(args []string, _ io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: atto _shell (started by atto, protocol on stdin)")
	}
	return jobs.ServeHost(os.Stdin, os.Stdout, os.Stderr)
}
