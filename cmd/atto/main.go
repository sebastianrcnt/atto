// Command atto is a terminal coding harness.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/app"
	"github.com/sebastianrcnt/atto/cli"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/mcp"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/update"
	"golang.org/x/term"
)

const usage = `atto — a terminal coding harness

usage:
  atto [flags]                      interactive session
  atto [flags] "prompt"             interactive session, starting with this message
  atto -p [flags] "prompt"          standalone session, not an agent in a tree;
                                    run one prompt and print the result
  cat file | atto -p "explain"      stdin is appended to the prompt
  atto -p -image shot.png "why?"    attach an image (repeatable); an image on
                                    stdin is attached too
  atto models [refresh]             list available models
  atto auth set <provider>          store an API key
  atto login [provider]             sign in (ChatGPT, …); /login inside atto
  atto logout <provider>            remove stored credentials
  atto resume [id]                  resume a session (no id: pick one)
  atto attach [ID] | attach -l      return to an atto running in the daemon
                                    (closed terminal, SSH drop, /detach)
  atto connect [session]            attach an independent TUI to a session worker
  atto agents                       every atto the daemon runs, with goals and
                                    agents; enter attaches (← in atto too)
  atto daemon [status|kill|stop]    the daemon interactive atto runs in
  atto sessions [list|show|rename|archive|unarchive|delete]
                                    manage saved sessions (atto sessions -h)
  atto history grep|show ...        search a session transcript
  atto job|monitor|timer|sleep ...  background jobs and wake-ups (atto job for details)
  atto goal [status|set|complete|...]  the session goal (or /goal, -goal)
  atto view <image>...              from the agent's shell: show the model an image file
  atto agent start|steer|next|wait|report|list|stop ...
                                    agents: background child sessions (atto agent -h)
  atto context [-json]              what a session here loads: AGENTS.md, skills,
                                    hooks, settings, model
  atto reload                       from the agent's shell: reload AGENTS.md, skills,
                                    hooks, extensions and settings in the running session
  atto mcp list|tools|call|add|remove|approve
                                    MCP servers, used by the agent through its shell (atto mcp -h)
  atto trust [list|approve all|approve <kind> <name>|revoke ...]
                                    project code approvals (hooks, MCP servers, extensions)
  atto extensions [list|approve <name>|types|docs]
                                    JavaScript/TypeScript extensions (docs: atto extensions docs)
  atto update [-check]              install the latest release
  atto channel [stable|edge]        show or switch the release channel this build follows
  atto serve [-listen addr]         JSON-RPC over HTTP + SSE, with a web client
  atto app-server                   JSON-RPC over stdio (JSON lines)

flags:
`

// nestedRefused are the commands an atto agent may not run from its shell:
// starting another agent (which would recurse and spend tokens unseen) or
// changing credentials. "" is atto itself (interactive or -p). Commands
// that work on the agent's own session (history, job, goal, reload...) or
// only read (context, models) are allowed.
var nestedRefused = map[string]bool{"": true, "attach": true, "connect": true, "_session-server": true, "agents": true, "daemon": true, "_daemon": true, "serve": true, "app-server": true, "resume": true, "login": true, "logout": true, "auth": true, "update": true, "channel": true, "_continue": true}

func refuseNested(cmd string) {
	if !config.InAgent() || !nestedRefused[cmd] {
		return
	}
	what := "start another atto agent"
	switch cmd {
	case "login", "logout", "auth":
		what = "change atto's credentials"
	case "update", "channel":
		what = "replace the atto binary"
	}
	fmt.Fprintf(os.Stderr, "atto: commands run by an atto agent can't %s (%s is set). Do the work in this session instead; for background work use atto job.\n", what, config.EnvAgent)
	os.Exit(2)
}

func subcommandNames() []string {
	var names []string
	for k := range subcommands() {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// subcommands maps each subcommand to its implementation.
func subcommands() map[string]func([]string, io.Writer) error {
	return map[string]func([]string, io.Writer) error{
		"history":     cli.RunHistory,
		"sessions":    cli.RunSessions,
		"auth":        cli.RunAuth,
		"models":      cli.RunModels,
		"job":         cli.RunJob,
		"monitor":     cli.RunMonitor,
		"timer":       cli.RunTimer,
		"sleep":       cli.RunSleep,
		"goal":        cli.RunGoal,
		"view":        cli.RunView,
		"agent":       cli.RunAgent,
		"_agent-turn": cli.RunAgentTurn,
		"context":     cli.RunContext,
		"reload":      cli.RunReload,
		"extensions":  cli.RunExtensions,
		"trust":       cli.RunTrust,
		"mcp":         cli.RunMCP,
		"update":      cli.RunUpdate,
		"channel":     cli.RunChannel,
		"_supervise":  cli.RunSupervise,
		"_shell":      cli.RunShellHost,
		"_continue":   cli.RunContinue,
		"attach":      cli.RunAttach,
		"connect":     cli.RunConnect,
		"_session-server": func(args []string, out io.Writer) error {
			provider.UserAgent = "github.com/sebastianrcnt/atto/" + update.Current()
			return daemon.RunWorker(update.Current(), args)
		},
		"agents":  cli.RunAgents,
		"daemon":  cli.RunDaemon,
		"_daemon": cli.RunDaemonServe,
		"login":   cli.RunLogin,
		"logout":  cli.RunLogout,
		"serve": func(args []string, out io.Writer) error {
			provider.UserAgent = "github.com/sebastianrcnt/atto/" + update.Current()
			var routes *server.WorkerRoutes
			if daemon.Usable() {
				routes = daemon.Routes()
			}
			return server.RunHTTPWith(update.Current(), args, out, routes)
		},
		"app-server": func(args []string, out io.Writer) error {
			provider.UserAgent = "github.com/sebastianrcnt/atto/" + update.Current()
			var routes *server.WorkerRoutes
			if daemon.Usable() {
				routes = daemon.Routes()
			}
			return server.RunStdioWith(update.Current(), args, routes)
		},
	}
}

// unknownCommand reports whether a lone argument is a mistyped subcommand
// rather than a prompt, and what to print for it. A prompt is more than one
// word or contains a space; one bare word is far likelier a typo.
func unknownCommand(positional []string, known []string) (msg string, ok bool) {
	if len(positional) != 1 || strings.ContainsAny(positional[0], " \t\n") {
		return "", false
	}
	w := positional[0]
	if sug := closestCommand(w, known); sug != "" {
		return fmt.Sprintf("atto: unknown command %q. Did you mean %q?", w, sug), true
	}
	return fmt.Sprintf("atto: unknown command %q (see atto -h)", w), true
}

// commandAliases are common synonyms too far from the real name for edit
// distance to catch ("upgrade" is 3 edits from "update").
var commandAliases = map[string]string{"upgrade": "update"}

// closestCommand returns the known name within edit distance 2 of w, or "".
// Hidden names (leading underscore) are never suggested.
func closestCommand(w string, known []string) string {
	if to := commandAliases[w]; to != "" && slices.Contains(known, to) {
		return to
	}
	best, bd := "", 3
	for _, k := range known {
		if strings.HasPrefix(k, "_") {
			continue
		}
		if d := editDistance(w, k); d < bd || (d == bd && k < best) {
			best, bd = k, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// stringList is a flag that may be repeated (-image a.png -image b.png).
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ", ") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// parseInterleaved parses flags before and after the prompt words, as in
// atto -p "fix it" -m x, and returns the words.
func parseInterleaved(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		_ = fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return positional
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// initialPrompt joins positional words into the first message, so
// atto fix the build and atto "fix the build" start the same session.
func initialPrompt(positional []string) string { return strings.Join(positional, " ") }

// resumeArgs rewrites "atto resume [id] [flags]" into the flags it means:
// -session <id>, or -resume for the picker. Other flags pass through.
func resumeArgs(args []string) []string {
	out := []string{args[0]}
	var id string
	for i, a := range args[2:] {
		// A bare word right after -m or -effort is that flag's value.
		valueOf := i > 0 && (args[i+1] == "-m" || args[i+1] == "-effort")
		if id == "" && !strings.HasPrefix(a, "-") && !valueOf {
			id = a
			continue
		}
		out = append(out, a)
	}
	if id != "" {
		return append(out, "-session", id)
	}
	return append(out, "-resume")
}

func main() {
	daemon.ConsumePaneToken()
	update.Cleanup()
	mcp.Version = update.Current()
	// This binary serves `atto _shell`, so the agent's commands can run
	// under shell hosts and move to the background.
	agent.ShellHost = true
	if len(os.Args) > 1 && os.Args[1] == "resume" {
		// Not a subcommand function: it starts the TUI, like -resume / -session.
		refuseNested("resume")
		os.Args = resumeArgs(os.Args)
	}
	if len(os.Args) > 1 {
		refuseNested(os.Args[1])
		sub := subcommands()[os.Args[1]]
		if sub != nil {
			if err := sub(os.Args[2:], os.Stdout); err != nil {
				if code, ok := errors.AsType[cli.ExitCode](err); ok {
					os.Exit(int(code))
				}
				if !errors.Is(err, cli.ErrSilent) {
					fmt.Fprintln(os.Stderr, err)
				}
				os.Exit(1)
			}
			return
		}
	}

	fs := flag.NewFlagSet("atto", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		fs.PrintDefaults()
	}
	showVersion := fs.Bool("version", false, "print version and exit")
	print := fs.Bool("p", false, "standalone session, not an agent in a tree: run the prompt non-interactively and exit")
	model := fs.String("m", "", "model to use, as provider/id (see: atto models)")
	effort := fs.String("effort", "", "reasoning effort for this run")
	cont := fs.Bool("c", false, "continue the most recent session in this directory")
	sessionID := fs.String("session", "", "continue the session with this ID")
	resume := fs.Bool("resume", false, "pick a saved session to resume (interactive)")
	inline := fs.Bool("inline", false, "render inline in the main screen instead of fullscreen")
	format := fs.String("output-format", "text", "print mode output: text, json or stream-json")
	partial := fs.Bool("include-partial", false, "stream-json: also emit text and reasoning deltas")
	verbose := fs.Bool("v", false, "print mode: show tool activity on stderr")
	maxSteps := fs.Int("max-steps", 0, "print mode: stop after this many model calls")
	noSave := fs.Bool("no-save", false, "print mode: do not save the run as a session")
	goalObj := fs.String("goal", "", "print mode: keep working until this objective is done")
	var imagePaths stringList
	fs.Var(&imagePaths, "image", "print mode: attach the image file at `path` (PNG, JPEG, GIF or WebP) to the prompt; repeatable.\nAn image piped to stdin is attached as well. The model must accept images")

	positional := parseInterleaved(fs, os.Args[1:])

	if *showVersion {
		fmt.Println("atto", update.Describe())
		return
	}
	provider.UserAgent = "github.com/sebastianrcnt/atto/" + update.Current()
	refuseNested("")

	var err error
	if *print {
		switch *format {
		case "text", "json", "stream-json":
		default:
			fmt.Fprintf(os.Stderr, "atto: unknown --output-format %q\n", *format)
			os.Exit(2)
		}
		var prompt string
		var imgs []provider.Image
		if *goalObj == "" || len(positional) > 0 || len(imagePaths) > 0 {
			prompt, imgs, err = cli.ReadPromptInput(positional, imagePaths) // a goal needs no prompt
		}
		if err == nil {
			err = cli.RunPrint(cli.PrintOptions{
				Prompt: prompt, Images: imgs, Model: *model, Effort: *effort, Format: *format, Partial: *partial,
				Verbose: *verbose, MaxSteps: *maxSteps, Continue: *cont, Resume: *sessionID, NoSave: *noSave,
				Goal: *goalObj,
			})
		}
	} else {
		if msg, ok := unknownCommand(positional, subcommandNames()); ok {
			fmt.Fprintln(os.Stderr, msg)
			os.Exit(2)
		}
		if useDaemon() {
			code, note, derr := daemon.Run(daemon.Hello{Op: "new", Args: os.Args[1:], Cwd: cwd(), Env: os.Environ()})
			if derr == nil {
				if note != "" {
					fmt.Fprintln(os.Stderr, note)
				}
				os.Exit(code)
			}
			fmt.Fprintf(os.Stderr, "atto: running without the daemon: %v\n", derr)
			// Sessions then run in this process too, not in its workers.
			os.Setenv("ATTO_NO_DAEMON", "1")
		}
		err = app.Run(app.Options{Prompt: initialPrompt(positional), Inline: *inline, Continue: *cont, Resume: *resume, Model: *model, Session: *sessionID, Effort: *effort})
	}
	if errors.Is(err, cli.ErrPrintFailed) {
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "atto:", err)
		os.Exit(1)
	}
}

// useDaemon reports whether interactive atto should run in a pane of the
// daemon (package daemon): on a terminal, not already in a pane, unless
// turned off.
func useDaemon() bool {
	if runtime.GOOS == "windows" || os.Getenv(daemon.EnvPane) != "" || os.Getenv("ATTO_NO_DAEMON") != "" {
		return false
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	s, err := config.LoadSettings()
	return err != nil || s.DaemonOn()
}

func cwd() string {
	d, _ := os.Getwd()
	return d
}
