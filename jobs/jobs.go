// Package jobs runs background commands for a session. Each job runs under
// a small supervisor process (atto re-executed as `atto _supervise <dir>`)
// that outlives the shell call that started it, captures output to a file,
// records the exit status, and drops an event in the session inbox when the
// job ends — so the agent hears about it without polling (an improvement
// on codex, where the model must poll).
//
// State is plain files under ~/.atto/jobs/<session>/<id>/: job.json and
// output.log. Any process (the TUI, the daemon, `atto job` run by the
// model) can read it.
package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/shell"
)

const (
	// MaxRunning caps concurrent jobs per session (codex: 64 processes).
	MaxRunning = 64
	// maxLog caps output.log; beyond it output is dropped with a note.
	maxLog = 16 << 20
	// eventTail is how many output lines an exit event carries.
	eventTail = 20
)

type Status string

const (
	Starting Status = "starting"
	Running  Status = "running"
	Exited   Status = "exited"
	Killed   Status = "killed"
	Failed   Status = "failed" // could not start
	Lost     Status = "lost"   // supervisor vanished (crash, reboot)
)

// Monitor makes a job re-run its command every Every until a condition
// holds, then report it.
type Monitor struct {
	Every     time.Duration `json:"every"`
	Until     string        `json:"until,omitempty"`     // regexp on output
	OnChange  bool          `json:"onChange,omitempty"`  // output differs from the first run
	UntilExit *int          `json:"untilExit,omitempty"` // command exits with this code
	Timeout   time.Duration `json:"timeout,omitempty"`   // give up after this long
}

type Job struct {
	Type          string     `json:"kind,omitempty"` // agent turns have an explicit lifecycle kind
	ID            int        `json:"id"`
	Session       string     `json:"session"`
	Name          string     `json:"name"`
	Command       string     `json:"command"`
	Cwd           string     `json:"cwd"`
	Status        Status     `json:"status"`
	ExitCode      *int       `json:"exitCode,omitempty"`
	Error         string     `json:"error,omitempty"`
	SupervisorPID int        `json:"supervisorPid,omitempty"`
	PID           int        `json:"pid,omitempty"`
	Started       time.Time  `json:"started"`
	Ended         *time.Time `json:"ended,omitempty"`
	Monitor       *Monitor   `json:"monitor,omitempty"`
	Notify        *Notify    `json:"notify,omitempty"`
	// Args, if set, is run directly instead of Command through the shell;
	// Command then only describes the job.
	Args []string `json:"args,omitempty"`
	// Quiet: a clean exit posts no event, for a command that tells the
	// session how it went itself (an agent turn).
	Quiet bool `json:"quiet,omitempty"`
}

func (j Job) Kind() string {
	if j.Type != "" {
		return j.Type
	}
	if j.Monitor != nil {
		return "monitor"
	}
	return "job"
}

// KindLabel is Kind for listings: it also shows that a job notifies.
func (j Job) KindLabel() string {
	if j.Notify != nil {
		return j.Kind() + "+notify"
	}
	return j.Kind()
}

func (j Job) Active() bool { return j.Status == Starting || j.Status == Running }

// Label is the job's name, or its command's first line.
func (j Job) Label() string {
	if j.Name != "" {
		return j.Name
	}
	c := strings.TrimSpace(j.Command)
	if i := strings.IndexByte(c, '\n'); i >= 0 {
		c = c[:i] + " …"
	}
	return c
}

// Runtime is how long the job ran (or has been running).
func (j Job) Runtime() time.Duration {
	end := time.Now()
	if j.Ended != nil {
		end = *j.Ended
	}
	return end.Sub(j.Started).Round(time.Second)
}

func Root(session string) string {
	if !fsutil.ValidID(session) {
		session = ".invalid-session"
	}
	return filepath.Join(config.Dir(), "jobs", session)
}
func dirOf(session string, id int) string {
	return filepath.Join(Root(session), strconv.Itoa(id))
}
func OutputPath(session string, id int) string {
	return filepath.Join(dirOf(session, id), "output.log")
}

var saveMu sync.Mutex

func save(dir string, j Job) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	data, _ := json.MarshalIndent(j, "", "  ")
	return fsutil.WriteAtomic(filepath.Join(dir, "job.json"), data, 0o600)
}

func load(dir string) (Job, error) {
	var j Job
	data, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		return j, err
	}
	return j, json.Unmarshal(data, &j)
}

// startGrace is how long a job may wait for its supervisor to start.
const startGrace = time.Minute

// Get loads a job, marking it lost if its supervisor is gone.
func Get(session string, id int) (Job, error) {
	if !fsutil.ValidID(session) || id <= 0 {
		return Job{}, fmt.Errorf("invalid session or job id")
	}
	j, err := load(dirOf(session, id))
	if errors.Is(err, os.ErrNotExist) {
		return j, fmt.Errorf("no job %d", id)
	}
	if err == nil && j.Status == Starting && j.SupervisorPID == 0 && time.Since(j.Started) > startGrace {
		// Its supervisor never came up (or died before saying so).
		now := time.Now()
		j.Status, j.Ended = Lost, &now
		_ = save(dirOf(session, id), j)
	}
	if err == nil && j.Active() && j.SupervisorPID > 0 && !shell.Alive(j.SupervisorPID) {
		// Re-read: the supervisor may have written its final state just now.
		if j2, err2 := load(dirOf(session, id)); err2 == nil && j2.Active() {
			now := time.Now()
			j2.Status, j2.Ended = Lost, &now
			_ = save(dirOf(session, id), j2)
			j = j2
		} else if err2 == nil {
			j = j2
		}
	}
	return j, err
}

// List returns a session's jobs, oldest first.
func List(session string) []Job {
	ents, _ := os.ReadDir(Root(session))
	var out []Job
	for _, e := range ents {
		id, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		if j, err := Get(session, id); err == nil {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out
}

// ActiveCount counts active jobs.
func ActiveCount(session string) int {
	n := 0
	for _, j := range List(session) {
		if j.Active() {
			n++
		}
	}
	return n
}

// newDir reserves the next job ID by creating its directory.
func newDir(session string) (int, string, error) {
	if err := fsutil.PrivateDirs(config.Dir(), Root(session)); err != nil {
		return 0, "", err
	}
	next := 1
	for _, j := range List(session) {
		next = max(next, j.ID+1)
	}
	for ; ; next++ {
		dir := dirOf(session, next)
		if err := os.Mkdir(dir, 0o700); err == nil {
			return next, dir, nil
		} else if !os.IsExist(err) {
			return 0, "", err
		}
	}
}

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
func StartAgentArgs(session, cwd, name string, args []string) (Job, error) {
	return startArgs(session, cwd, name, args, true, "agent")
}

func startArgs(session, cwd, name string, args []string, quiet bool, kind string) (Job, error) {
	if len(args) == 0 {
		return Job{}, fmt.Errorf("empty command")
	}
	id, dir, err := reserve(session)
	if err != nil {
		return Job{}, err
	}
	j := Job{Type: kind, ID: id, Session: session, Name: name, Command: strings.Join(args, " "), Cwd: cwd, Status: Starting, Started: time.Now(), Args: args, Quiet: quiet}
	return launch(dir, j, nil)
}

// reserve checks that session may start another job and reserves its ID.
func reserve(session string) (int, string, error) {
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
	if err := save(dir, Job{ID: id, Session: session, Status: Starting, Started: time.Now()}); err != nil {
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
		_ = save(dir, j)
		return j, err
	}
	sup := exec.Command(exe, "_supervise", dir)
	sup.Dir = j.Cwd
	sup.Env = withoutView(env)
	shell.Detach(sup)
	if err := sup.Start(); err != nil {
		j.Status, j.Error = Failed, err.Error()
		_ = save(dir, j)
		return j, err
	}
	// Reap it when it ends, or it stays a zombie that looks alive while
	// this process runs (the TUI starts jobs for hours).
	go func() { _ = sup.Wait() }()
	// Wait briefly for the supervisor to report the child's PID.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cur, err := load(dir); err == nil && cur.Status != Starting {
			return cur, nil
		}
	}
	return load(dir)
}

// Kill stops an active job and its whole process tree.
func Kill(session string, id int) (Job, error) {
	j, err := Get(session, id)
	if err != nil || !j.Active() {
		return j, err
	}
	// A starting job has no supervisor PID yet: wait for it, and if it
	// never comes, record the job killed so a late supervisor doesn't run it.
	for deadline := time.Now().Add(3 * time.Second); j.SupervisorPID == 0 && j.Active() && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if j, err = load(dirOf(session, id)); err != nil {
			return j, err
		}
	}
	if !j.Active() {
		return j, nil
	}
	if j.SupervisorPID == 0 {
		now := time.Now()
		j.Status, j.Ended = Killed, &now
		return j, save(dirOf(session, id), j)
	}
	if err := shell.Terminate(j.SupervisorPID); err != nil && shell.Alive(j.SupervisorPID) {
		return j, err
	}
	// The supervisor records "killed" on Unix; on Windows it is terminated
	// outright, so record it here if it did not.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cur, err := load(dirOf(session, id)); err == nil && !cur.Active() {
			return cur, nil
		}
		if !shell.Alive(j.SupervisorPID) {
			break
		}
	}
	cur, err := load(dirOf(session, id))
	if err == nil && cur.Active() {
		now := time.Now()
		cur.Status, cur.Ended = Killed, &now
		err = save(dirOf(session, id), cur)
	}
	return cur, err
}

// KillAll stops every active job of a session.
func KillAll(session string) int { return KillAllExcept(session, nil) }

// KillAllExcept is KillAll sparing the jobs keep says to.
func KillAllExcept(session string, keep func(Job) bool) int {
	n := 0
	for _, j := range List(session) {
		if j.Active() && (keep == nil || !keep(j)) {
			if _, err := Kill(session, j.ID); err == nil {
				n++
			}
		}
	}
	return n
}

// Wait blocks until the job ends, timeout passes, or the user sends input
// (events.Wake). It returns the job and why it returned.
func Wait(session string, id int, timeout time.Duration) (Job, string, error) {
	start := time.Now()
	for {
		j, err := Get(session, id)
		if err != nil || !j.Active() {
			return j, "done", err
		}
		if timeout > 0 && time.Since(start) >= timeout {
			return j, "timeout", nil
		}
		if events.WokenSince(session, start) {
			return j, "woken", nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Tail returns the last n lines of a job's output.
func Tail(session string, id, n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	data, err := readTail(OutputPath(session, id), 256<<10)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(data, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// Head returns the first n lines of a job's output.
func Head(session string, id, n int) (string, error) {
	f, err := os.Open(OutputPath(session, id))
	if err != nil {
		return "", err
	}
	defer f.Close()
	var b strings.Builder
	r := bufio.NewReader(f)
	for range n {
		line, err := r.ReadString('\n')
		b.WriteString(line)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func readTail(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	off := max(0, st.Size()-limit)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(f)
	s := string(data)
	if off > 0 {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return s, err
}

// cappedFile writes to a file until maxLog, then drops the rest.
type cappedFile struct {
	mu      sync.Mutex
	f       *os.File
	n       int64
	noticed bool
}

func (c *cappedFile) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := maxLog - c.n
	if room > 0 {
		w, err := c.f.Write(p[:min(int64(len(p)), room)])
		c.n += int64(w)
		if err != nil {
			return len(p), err
		}
	}
	if int64(len(p)) > room && !c.noticed {
		c.noticed = true
		if _, err := c.f.WriteString(fmt.Sprintf("\n[atto: output beyond %d MiB dropped]\n", maxLog>>20)); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// Supervise runs in the detached supervisor process: it executes the job,
// records the result and posts the exit event.
func Supervise(dir string) error {
	j, err := load(dir)
	if err != nil {
		return err
	}
	if !j.Active() { // killed before it started
		return nil
	}
	logf, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	if err := fsutil.PrivateFile(logf); err != nil {
		return err
	}
	out := &cappedFile{f: logf}
	j.SupervisorPID = os.Getpid()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	killed := false
	var killMu sync.Mutex
	go func() {
		if _, ok := <-sigs; ok {
			killMu.Lock()
			killed = true
			killMu.Unlock()
			cancel()
		}
	}()

	var code int
	var detail string
	if j.Monitor != nil {
		code, detail = superviseMonitor(ctx, dir, &j, out)
	} else if j.Notify != nil {
		nt := newNotifier(out, j, func(e events.Event) { _ = events.Push(j.Session, e) })
		code, detail = run(ctx, dir, &j, j.Command, nt, true)
		nt.Close()
	} else {
		code, detail = run(ctx, dir, &j, j.Command, out, true)
	}

	killMu.Lock()
	wasKilled := killed
	killMu.Unlock()
	return finish(dir, j, code, detail, wasKilled)
}

// finish records how a job ended and, unless it was killed (by atto job
// kill, which needs no telling), posts the exit event. code -2 means the
// command could not start.
func finish(dir string, j Job, code int, detail string, killed bool) error {
	now := time.Now()
	j.Ended = &now
	switch {
	case killed:
		j.Status = Killed
	case code == -2:
		j.Status = Failed
		j.Error = detail
	default:
		j.Status = Exited
		j.ExitCode = &code
	}
	if err := save(dir, j); err != nil {
		return err
	}
	if !killed && !(j.Quiet && j.Status == Exited && code == 0) {
		postEvent(j, detail)
	}
	return nil
}

// run executes command once, streaming output to out. It returns the exit
// code (-2 if it could not start, -1 if killed) and an error detail.
func run(ctx context.Context, dir string, j *Job, command string, out io.Writer, record bool) (int, string) {
	cmd := shell.Command(ctx, command)
	if len(j.Args) > 0 {
		cmd = exec.CommandContext(ctx, j.Args[0], j.Args[1:]...)
	}
	cmd.Dir = j.Cwd
	cmd.Env = append(os.Environ(), "TERM=dumb", "PAGER=cat", "GIT_PAGER=cat", "NO_COLOR=1")
	cmd.Stdout, cmd.Stderr = out, out
	tree := shell.NewTree(cmd)
	defer tree.Close()
	if err := cmd.Start(); err != nil {
		if record {
			j.Status, j.Error = Failed, err.Error()
			_ = save(dir, *j)
		}
		return -2, err.Error()
	}
	tree.Started()
	if record {
		j.Status, j.PID = Running, cmd.Process.Pid
		_ = save(dir, *j)
	}
	go func() {
		<-ctx.Done()
		tree.Kill()
	}()
	err := cmd.Wait()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, ""
	case errors.As(err, &ee):
		return ee.ExitCode(), ""
	}
	return -1, err.Error()
}

func superviseMonitor(ctx context.Context, dir string, j *Job, out io.Writer) (int, string) {
	m := j.Monitor
	var re *regexp.Regexp
	if m.Until != "" {
		re = regexp.MustCompile(m.Until)
	}
	j.Status, j.PID = Running, os.Getpid()
	_ = save(dir, *j)
	every := max(m.Every, time.Second)
	var baseline *string
	start := time.Now()
	for n := 1; ; n++ {
		var buf strings.Builder
		budget := max(every, time.Minute)
		if m.Timeout > 0 {
			budget = min(budget, max(0, m.Timeout-time.Since(start)))
		}
		runCtx, cancel := context.WithTimeout(ctx, budget)
		code, _ := run(runCtx, dir, j, j.Command, &buf, false)
		cancel()
		output := buf.String()
		fmt.Fprintf(out, "── check %d · %s · exit %d ──\n%s\n", n, time.Now().Format("15:04:05"), code, strings.TrimRight(output, "\n"))
		if ctx.Err() != nil {
			return -1, ""
		}
		if m.Timeout > 0 && time.Since(start) >= m.Timeout {
			return 1, fmt.Sprintf("timed out after %s without the condition holding", m.Timeout) + "\n" + lastLines(output, eventTail)
		}
		var why string
		switch {
		case re != nil && re.MatchString(output):
			why = fmt.Sprintf("output matched /%s/", m.Until)
		case m.UntilExit != nil && code == *m.UntilExit:
			why = fmt.Sprintf("command exited with %d", code)
		case m.OnChange && baseline != nil && output != *baseline:
			why = "output changed"
		}
		if baseline == nil {
			baseline = &output
		}
		if why != "" {
			return 0, why + "\n" + lastLines(output, eventTail)
		}
		if m.Timeout > 0 && time.Since(start) >= m.Timeout {
			return 1, fmt.Sprintf("timed out after %s without the condition holding", m.Timeout) + "\n" + lastLines(output, eventTail)
		}
		wait := every
		if m.Timeout > 0 {
			wait = min(wait, max(0, m.Timeout-time.Since(start)))
		}
		select {
		case <-ctx.Done():
			return -1, ""
		case <-time.After(wait):
		}
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// postEvent tells the session how the job ended.
func postEvent(j Job, detail string) {
	var text, title string
	switch {
	case j.Monitor != nil:
		what := "condition met"
		if j.ExitCode != nil && *j.ExitCode != 0 {
			what = "gave up"
		}
		text = fmt.Sprintf("Monitor %d (%q) %s after %s: %s\nFull log: atto job output %d", j.ID, j.Label(), what, j.Runtime(), detail, j.ID)
		title = fmt.Sprintf("◎ monitor %d %s: %s", j.ID, what, j.Label())
	case j.Status == Failed:
		text = fmt.Sprintf("Background job %d (%q) failed to start: %s", j.ID, j.Label(), detail)
		title = fmt.Sprintf("● job %d failed to start: %s", j.ID, j.Label())
	default:
		tail, _ := Tail(j.Session, j.ID, eventTail)
		text = fmt.Sprintf("Background job %d (%q) exited with code %d after %s.", j.ID, j.Label(), *j.ExitCode, j.Runtime())
		if strings.TrimSpace(tail) != "" {
			text += "\nLast output:\n" + tail
		}
		text += fmt.Sprintf("\nFull output: atto job output %d", j.ID)
		title = fmt.Sprintf("● job %d exited (%d) after %s: %s", j.ID, *j.ExitCode, j.Runtime(), j.Label())
	}
	_ = events.Push(j.Session, events.Event{Source: j.Kind(), Text: text, Title: title})
}
