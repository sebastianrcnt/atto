package jobs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/shell"
)

// Start launches command in the background and returns once it runs.
// mon and notify are optional; they are mutually exclusive.
func Start(session, cwd, name, command string, mon *Monitor, notify *Notify) (Job, error) {
	return start(session, cwd, name, command, mon, notify, nil)
}

// StartEnv is Start for a plain job whose command runs with env (the
// complete environment) rather than the caller's: the agent's shell tool
// starts jobs this way, with the variables it gives its commands.
func StartEnv(session, cwd, name, command string, env []string) (Job, error) {
	return start(session, cwd, name, command, nil, nil, env)
}

// StartArgs is Start for a plain job that runs args directly, with no
// shell in between, under the label name. With quiet, a clean exit posts
// no event: the command reports itself.
func StartArgs(session, cwd, name string, args []string, quiet bool) (Job, error) {
	return startArgs(session, cwd, name, args, quiet, "")
}

// StartAgentArgs starts an agent turn, which outlives an intermediate turn.
// With silent the job never posts an exit event (see Job.Silent).
func StartAgentArgs(session, cwd, name string, args []string, silent bool) (Job, error) {
	return startArgs(session, cwd, name, args, true, "agent", silent)
}

func startArgs(session, cwd, name string, args []string, quiet bool, kind string, silent ...bool) (Job, error) {
	if len(args) == 0 {
		return Job{}, fmt.Errorf("empty command")
	}
	id, dir, err := reserveKind(session, kind)
	if err != nil {
		return Job{}, err
	}
	j := Job{Type: kind, ID: id, Session: session, Name: name, Command: strings.Join(args, " "), Cwd: cwd, Status: Starting, Started: time.Now(), Args: args, Quiet: quiet, Silent: len(silent) > 0 && silent[0]}
	return launch(dir, j, nil)
}

// reserve checks that session may start another job and reserves its ID.
func reserve(session string) (int, string, error) { return reserveKind(session, "") }

// reserveKind is reserve for a job of kind ("agent" for a turn): the
// placeholder already has it, so a session that cleans up its jobs, as a
// turn's end does, spares an agent's turn that is just being started.
func reserveKind(session, kind string) (int, string, error) {
	if !fsutil.ValidID(session) {
		return 0, "", fmt.Errorf("no session: run inside atto (ATTO_SESSION_ID) or pass --session")
	}
	release, err := lockReservations(session)
	if err != nil {
		return 0, "", err
	}
	defer release()
	if ActiveCount(session) >= MaxRunning {
		return 0, "", fmt.Errorf("%d jobs already running; stop some with `atto job kill <id>`", MaxRunning)
	}
	id, dir, err := newDir(session)
	if err != nil {
		return 0, "", err
	}
	// Count the reservation before another caller checks the limit.
	if err := save(dir, Job{Type: kind, ID: id, Session: session, Status: Starting, Started: time.Now()}); err != nil {
		_ = os.RemoveAll(dir)
		return 0, "", err
	}
	return id, dir, nil
}

func start(session, cwd, name, command string, mon *Monitor, notify *Notify, env []string) (Job, error) {
	if strings.TrimSpace(command) == "" {
		return Job{}, fmt.Errorf("empty command")
	}
	if mon != nil && mon.Until != "" {
		if _, err := regexp.Compile(mon.Until); err != nil {
			return Job{}, fmt.Errorf("--until: %w", err)
		}
	}
	if notify != nil {
		if mon != nil {
			return Job{}, fmt.Errorf("--notify does not apply to monitors")
		}
		if _, err := regexp.Compile(notify.Pattern); err != nil {
			return Job{}, fmt.Errorf("--notify: %w", err)
		}
		if notify.Limit < 0 {
			return Job{}, fmt.Errorf("--notify-limit must be positive")
		}
	}
	id, dir, err := reserve(session)
	if err != nil {
		return Job{}, err
	}
	j := Job{ID: id, Session: session, Name: name, Command: command, Cwd: cwd, Status: Starting, Started: time.Now(), Monitor: mon, Notify: notify}
	return launch(dir, j, env)
}

// withoutView is env (nil: the caller's) without config.EnvView: a job
// outlives the command that started it, so atto view in it has no result
// to attach images to, and says so.
func withoutView(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		return strings.HasPrefix(kv, config.EnvView+"=")
	})
}

// launch saves the new job j in dir and starts its supervisor.
func launch(dir string, j Job, env []string) (Job, error) {
	if err := save(dir, j); err != nil {
		return j, err
	}
	exe, err := os.Executable()
	if err != nil {
		j.Status, j.Error = Failed, err.Error()
		return j, errors.Join(err, save(dir, j))
	}
	sup := exec.Command(exe, "_supervise", dir)
	sup.Dir = j.Cwd
	sup.Env = withoutView(env)
	shell.Detach(sup)
	if err := sup.Start(); err != nil {
		j.Status, j.Error = Failed, err.Error()
		return j, errors.Join(err, save(dir, j))
	}
	// Reap it when it ends, or it stays a zombie that looks alive while
	// this process runs (the TUI starts jobs for hours).
	done := make(chan error, 1)
	go func() { done <- sup.Wait() }()
	// Wait briefly for the supervisor to report the child's PID.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		select {
		case err := <-done:
			cur, loadErr := load(dir)
			if err != nil {
				err = fmt.Errorf("job %d supervisor failed (see atto job output %d): %w", j.ID, j.ID, err)
			}
			return cur, errors.Join(err, loadErr)
		default:
		}
		if cur, err := load(dir); err == nil && cur.Status != Starting {
			return cur, nil
		}
	}
	return load(dir)
}
