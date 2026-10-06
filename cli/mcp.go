package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/mcp"
)

// ErrSilent makes the command exit non-zero without printing a message:
// it already said what it had to.
var ErrSilent = errors.New("")

const mcpUsage = `usage: atto mcp <command>

  list [-json]                       the configured servers: scope, status, tools
  tools [server [tool]]              the tools of a server (or of every server), one
                                     line each; with a tool, its full JSON schema
  call <server> <tool> ['<json>'|-]  call a tool with a JSON object as arguments
                                     (from the command line or stdin); prints the
                                     result, exits non-zero if the tool reported an error
  add <name> [-scope user|project|local] [-e KEY=VALUE]... -- <command> [args...]
  add <name> [-scope ...] [-type http|sse] [-H 'Name: value']... -url <url>
                                     add a server (default scope: local)
  remove <name> [-scope ...]         remove a server
  approve <name>                     let the project's .mcp.json server <name> run, as its
                                     entry is now. Not from an agent's shell.

Servers are configured in Claude Code's .mcp.json format:
  user     ~/.atto/mcp.json
  project  <project>/.mcp.json         shared; each server needs approval once
  local    ~/.atto/projects/<project>/mcp.json   private to you, outside the repo
The later wins by name (local over project over user). Strings may use
${VAR} and ${VAR:-default}. Remote servers that need OAuth are not supported.

Inside atto, servers start on first use and stay until the session ends;
run from a normal terminal, a server runs for the one command. A running
session picks up changes to the files with /reload (or atto reload).`

// RunMCP implements "atto mcp".
func RunMCP(args []string, out io.Writer) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "list":
		return mcpList(args, out)
	case "tools":
		return mcpTools(args, out)
	case "call":
		return mcpCall(args, out)
	case "add":
		return mcpAdd(args, out)
	case "remove", "rm":
		return mcpRemove(args, out)
	case "approve":
		return mcpApprove(args, out)
	case "help", "-h", "-help", "--help":
		fmt.Fprintln(out, mcpUsage)
		return nil
	}
	return fmt.Errorf("atto mcp: unknown command %q\n\n%s", cmd, mcpUsage)
}

// mcpEnv is the project the command runs in.
func mcpEnv() (mcp.Options, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return mcp.Options{}, err
	}
	return mcp.Options{Cwd: cwd, Root: agent.ProjectRoot(cwd)}, nil
}

// mcpBackend returns what runs the command's calls (see mcp.Connect) and
// the context that ends them: Ctrl-C and SIGTERM stop a call. The caller
// closes the backend (a Manager stops its servers) and cancels.
func mcpBackend(session string) (mcp.Backend, context.Context, context.CancelFunc, error) {
	o, err := mcpEnv()
	if err != nil {
		return nil, nil, nil, err
	}
	b, _, note := mcp.Connect(session, o)
	if note != "" {
		fmt.Fprintln(os.Stderr, "atto mcp: "+note)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return b, ctx, cancel, nil
}

func mcpList(args []string, out io.Writer) error {
	fs := newFlags("mcp list")
	asJSON := fs.Bool("json", false, "")
	session := sessionFlag(fs)
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fmt.Errorf("usage: atto mcp list [-json]")
	}
	b, ctx, cancel, err := mcpBackend(*session)
	if err != nil {
		return err
	}
	defer cancel()
	defer b.Close()
	infos, err := b.Servers(ctx)
	if err != nil {
		return err
	}
	if m, ok := b.(*mcp.Manager); ok {
		for _, is := range m.Issues() {
			fmt.Fprintln(os.Stderr, "atto mcp: "+is)
		}
	}
	if *asJSON {
		if infos == nil {
			infos = []mcp.Info{}
		}
		return encodeJSON(out, infos)
	}
	if len(infos) == 0 {
		fmt.Fprintf(out, "No MCP servers. Add one with atto mcp add, or write %s (Claude Code's .mcp.json format).\n", core.ShortPath(config.MCPPath()))
		return nil
	}
	w := 0
	for _, in := range infos {
		w = max(w, len(in.Name))
	}
	for _, in := range infos {
		status := in.Status
		switch in.Status {
		case mcp.Running:
			status = fmt.Sprintf("running, %s", plural(in.Tools, "tool"))
		case mcp.Failed:
			status = "failed: " + oneLine(in.Error, 300)
		case mcp.NeedsApproval:
			status = "needs approval: atto mcp approve " + in.Name
		case mcp.DeniedStatus:
			status = "denied: atto mcp approve " + in.Name + " to allow"
		}
		fmt.Fprintf(out, "%-*s  %-7s %-5s %s · %s\n", w, in.Name, in.Scope, in.Transport, status, oneLine(in.Target, 80))
	}
	return nil
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func encodeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// firstLine is the first non-empty line of a description.
func firstLine(s string) string {
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func mcpTools(args []string, out io.Writer) error {
	fs := newFlags("mcp tools")
	asJSON := fs.Bool("json", false, "")
	session := sessionFlag(fs)
	if err := fs.Parse(args); err != nil || fs.NArg() > 2 {
		return fmt.Errorf("usage: atto mcp tools [-json] [server [tool]]")
	}
	b, ctx, cancel, err := mcpBackend(*session)
	if err != nil {
		return err
	}
	defer cancel()
	defer b.Close()

	var tools []mcp.ToolInfo
	var problems []mcp.ServerError
	if fs.NArg() == 0 {
		if tools, problems, err = b.AllTools(ctx); err != nil {
			return err
		}
	} else if tools, err = b.Tools(ctx, fs.Arg(0)); err != nil {
		return err
	}

	if fs.NArg() == 2 { // the full definition
		for _, t := range tools {
			if t.Name == fs.Arg(1) {
				return encodeJSON(out, struct {
					Server      string          `json:"server"`
					Name        string          `json:"name"`
					Description string          `json:"description,omitempty"`
					InputSchema json.RawMessage `json:"inputSchema"`
				}{t.Server, t.Name, t.Description, t.InputSchema})
			}
		}
		var names []string
		for _, t := range tools {
			names = append(names, t.Name)
		}
		return fmt.Errorf("server %s has no tool %q (tools: %s)", fs.Arg(0), fs.Arg(1), strings.Join(names, ", "))
	}

	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "atto mcp: %s\n", p.Error)
	}
	if *asJSON {
		if tools == nil {
			tools = []mcp.ToolInfo{}
		}
		return encodeJSON(out, tools)
	}
	if len(tools) == 0 {
		if len(problems) == 0 {
			fmt.Fprintln(out, "No tools.")
		}
		return nil
	}
	sort.SliceStable(tools, func(i, j int) bool { return tools[i].Server < tools[j].Server })
	sw, tw := 0, 0
	for _, t := range tools {
		sw, tw = max(sw, len(t.Server)), max(tw, len(t.Name))
	}
	for _, t := range tools {
		line := fmt.Sprintf("%-*s  %-*s  %s", sw, t.Server, tw, t.Name, oneLine(firstLine(t.Description), 100))
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}
	return nil
}

func mcpCall(args []string, out io.Writer) error {
	const usage = "usage: atto mcp call <server> <tool> ['<json object>' | -]"
	fs := newFlags("mcp call")
	session := sessionFlag(fs)
	if err := fs.Parse(args); err != nil || fs.NArg() < 2 || fs.NArg() > 3 {
		return fmt.Errorf("%s", usage)
	}
	var raw json.RawMessage
	if fs.NArg() == 3 {
		text := fs.Arg(2)
		if text == "-" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			text = string(data)
		}
		if strings.TrimSpace(text) != "" {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal([]byte(text), &obj); err != nil || obj == nil {
				return fmt.Errorf("the arguments must be a JSON object, e.g. '{\"name\": \"value\"}'")
			}
			raw = json.RawMessage(text)
		}
	}
	b, ctx, cancel, err := mcpBackend(*session)
	if err != nil {
		return err
	}
	defer cancel()
	defer b.Close()
	res, err := b.Call(ctx, fs.Arg(0), fs.Arg(1), raw)
	if err != nil {
		return err
	}
	text := res.Text
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if _, err := io.WriteString(out, text); err != nil {
		return err
	}
	if res.IsError {
		return ErrSilent
	}
	return nil
}

// scopeFlag adds -scope to fs.
func scopeFlag(fs *flag.FlagSet, def string) *string {
	return fs.String("scope", def, "")
}

func checkScope(s string) error {
	switch s {
	case mcp.ScopeUser, mcp.ScopeProject, mcp.ScopeLocal:
		return nil
	}
	return fmt.Errorf("unknown scope %q (user, project or local)", s)
}

// repeated is a flag that may be given several times.
type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ", ") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

func mcpAdd(args []string, out io.Writer) error {
	const usage = "usage: atto mcp add <name> [-scope user|project|local] [-e KEY=VALUE]... -- <command> [args...]\n       atto mcp add <name> [-scope ...] [-type http|sse] [-H 'Name: value']... -url <url>"
	var command []string
	for i, a := range args {
		if a == "--" {
			args, command = args[:i], args[i+1:]
			break
		}
	}
	fs := newFlags("mcp add")
	scope := scopeFlag(fs, mcp.ScopeLocal)
	url := fs.String("url", "", "")
	typ := fs.String("type", "", "")
	var env, headers repeated
	fs.Var(&env, "e", "")
	fs.Var(&headers, "H", "")
	var positional []string
	for { // flags may come before or after the name
		if err := fs.Parse(args); err != nil {
			return fmt.Errorf("%s", usage)
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	if len(positional) != 1 {
		return fmt.Errorf("%s", usage)
	}
	name := positional[0]
	if err := checkScope(*scope); err != nil {
		return err
	}
	if strings.ContainsAny(name, " \t\n#") || name == "" {
		return fmt.Errorf("bad server name %q", name)
	}
	var c mcp.ServerConfig
	switch {
	case *url != "" && len(command) > 0:
		return fmt.Errorf("give a command after -- or a -url, not both")
	case *url != "":
		if len(env) > 0 {
			return fmt.Errorf("-e is for stdio servers; a remote one takes -H")
		}
		c.Type, c.URL = *typ, *url
		if c.Type == "" {
			c.Type = mcp.TransportHTTP
		}
		for _, h := range headers {
			k, v, ok := strings.Cut(h, ":")
			if k = strings.TrimSpace(k); !ok || k == "" {
				return fmt.Errorf("bad header %q (want 'Name: value')", h)
			}
			if c.Headers == nil {
				c.Headers = map[string]string{}
			}
			c.Headers[k] = strings.TrimSpace(v)
		}
	case len(command) > 0:
		if len(headers) > 0 || *typ != "" && *typ != mcp.TransportStdio {
			return fmt.Errorf("-H and -type are for remote servers (-url)")
		}
		c.Command, c.Args = command[0], command[1:]
		if len(env) > 0 {
			c.Env = map[string]string{}
		}
		for _, e := range env {
			k, v, ok := strings.Cut(e, "=")
			if !ok || k == "" {
				return fmt.Errorf("bad -e %q (want KEY=VALUE)", e)
			}
			c.Env[k] = v
		}
	default:
		return fmt.Errorf("%s", usage)
	}
	o, err := mcpEnv()
	if err != nil {
		return err
	}
	servers, _ := mcp.Load(o.Root)
	for _, s := range servers {
		if s.Name == name && s.Scope == *scope {
			return fmt.Errorf("%s already exists in %s; remove it first (atto mcp remove %s -scope %s)", name, core.ShortPath(s.Path), name, *scope)
		}
	}
	if err := mcp.Put(*scope, o.Root, name, c); err != nil {
		return err
	}
	path := mcp.Path(*scope, o.Root)
	fmt.Fprintf(out, "Added %s to %s.", name, core.ShortPath(path))
	if *scope == mcp.ScopeProject {
		fmt.Fprintf(out, " It runs once approved: atto mcp approve %s.", name)
	}
	fmt.Fprintln(out, " A running session picks it up after /reload (or atto reload).")
	return nil
}

func mcpRemove(args []string, out io.Writer) error {
	const usage = "usage: atto mcp remove <name> [-scope user|project|local]"
	fs := newFlags("mcp remove")
	scope := scopeFlag(fs, "")
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return fmt.Errorf("%s", usage)
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	if len(positional) != 1 {
		return fmt.Errorf("%s", usage)
	}
	name := positional[0]
	o, err := mcpEnv()
	if err != nil {
		return err
	}
	if *scope == "" { // the one scope that has it
		var found []string
		for _, sc := range mcp.Scopes {
			if has, _ := fileHas(mcp.Path(sc, o.Root), name); has {
				found = append(found, sc)
			}
		}
		switch len(found) {
		case 0:
			return fmt.Errorf("no MCP server %q in any scope's file (see atto mcp list)", name)
		case 1:
			*scope = found[0]
		default:
			return fmt.Errorf("%s is defined in several scopes (%s): say which with -scope", name, strings.Join(found, ", "))
		}
	}
	if err := checkScope(*scope); err != nil {
		return err
	}
	ok, err := mcp.Remove(*scope, o.Root, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no MCP server %q in %s", name, core.ShortPath(mcp.Path(*scope, o.Root)))
	}
	fmt.Fprintf(out, "Removed %s from %s. A running session drops it after /reload (or atto reload).\n", name, core.ShortPath(mcp.Path(*scope, o.Root)))
	return nil
}

// fileHas reports whether the server file at path defines name.
func fileHas(path, name string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var f struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&f); err != nil {
		return false, err
	}
	_, ok := f.Servers[name]
	return ok, nil
}

func mcpApprove(args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: atto mcp approve <name>")
	}
	name := args[0]
	if config.InAgent() {
		// Approval is the user's check on commands a repository brings.
		return fmt.Errorf("atto: project MCP servers are approved by the user, not from an agent's shell (%s is set). Ask the user to run: atto mcp approve %s", config.EnvAgent, name)
	}
	o, err := mcpEnv()
	if err != nil {
		return err
	}
	m := mcp.New(o)
	defer m.Close()
	if err := m.Approve(name); err != nil {
		return err
	}
	s, _ := m.Server(name)
	fmt.Fprintf(out, "Approved %s (%s: %s). A running session can start it now (after /reload, if the file changed since the session began).\n", name, core.ShortPath(s.Path), oneLine(s.Config.Target(), 80))
	return nil
}
