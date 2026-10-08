// Package mcp lets atto's agent use MCP servers without giving the model
// any tools of its own: atto has one tool, the shell, and the servers are
// reached through "atto mcp" subcommands that the model runs in it. The
// tool schema and the system prompt therefore stay the same whatever is
// configured (only one short line naming the servers is added), and the
// prompt cache survives.
//
// Servers are configured in Claude Code's .mcp.json format at three scopes
// (see Load) and run inside the atto session that uses them, started on
// first use and kept until the session ends, so a stateful server is not
// restarted per call. The shell's "atto mcp" talks to the session over a
// local socket (see Manager.Serve and Dial); from a normal terminal it
// starts the server for the one call instead (see Connect).
//
// Project servers (<project>/.mcp.json) run only once the user approved
// them, as project extensions do. Remote servers that need OAuth are not
// supported; headers (with ${VAR} expansion for tokens) are.
package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sebastianrcnt/atto/approval"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

// Scopes, from lowest to highest precedence.
const (
	ScopeUser    = "user"    // ~/.atto/mcp.json
	ScopeProject = "project" // <root>/.mcp.json; needs approval
	ScopeLocal   = "local"   // ~/.atto/projects/<project>/mcp.json; private to the user, outside the repository
)

// Scopes lists the scopes in precedence order, lowest first.
var Scopes = []string{ScopeUser, ScopeProject, ScopeLocal}

// Transports.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http" // streamable HTTP
	TransportSSE   = "sse"
)

// ServerConfig is one entry of "mcpServers", as Claude Code writes it:
// {command, args, env} for a stdio server, {type: "http", url, headers}
// for a remote one.
type ServerConfig struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Transport is the transport the entry asks for: its type, else stdio for
// a command and http for a url.
func (c ServerConfig) Transport() string {
	switch t := strings.ToLower(c.Type); {
	case t != "":
		return t
	case c.URL != "" && c.Command == "":
		return TransportHTTP
	}
	return TransportStdio
}

// Validate reports what is wrong with the entry.
func (c ServerConfig) Validate() error {
	switch c.Transport() {
	case TransportStdio:
		if strings.TrimSpace(c.Command) == "" {
			return errors.New("a stdio server needs a command")
		}
		if c.URL != "" {
			return errors.New("a stdio server has no url (set \"type\": \"http\" for a remote one)")
		}
	case TransportHTTP, TransportSSE:
		if strings.TrimSpace(c.URL) == "" {
			return errors.New("a remote server needs a url")
		}
		if c.Command != "" {
			return errors.New("a remote server has no command")
		}
	default:
		return fmt.Errorf("unknown type %q (stdio, http or sse)", c.Type)
	}
	return nil
}

// Hash identifies the entry as written, before ${VAR} expansion, so what
// the user approved is what the file says and not what the environment
// holds today.
func (c ServerConfig) Hash() string {
	data, _ := json.Marshal(c) // maps are marshaled in key order
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// Target is what the server runs or connects to, for display.
func (c ServerConfig) Target() string {
	if c.Transport() == TransportStdio {
		return strings.Join(append([]string{c.Command}, c.Args...), " ")
	}
	return c.URL
}

// Server is a configured server and where it came from.
type Server struct {
	Name   string
	Scope  string
	Path   string // the file that defines it
	Config ServerConfig
}

// File is the format of an MCP server file.
type File struct {
	Servers map[string]ServerConfig `json:"mcpServers"`
}

// Ignored returns the path of a <root>/.atto/mcp.json if the repository has
// one: it is not read, since a repository could use it to start commands
// without approval. Local servers go to the user's own ~/.atto instead.
func Ignored(root string) string {
	p := config.RepoMCPPath(root)
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

// Path returns the file for scope in a project at root.
func Path(scope, root string) string {
	switch scope {
	case ScopeProject:
		return config.ProjectMCPPath(root)
	case ScopeLocal:
		return config.LocalMCPPath(root)
	}
	return config.MCPPath()
}

// Load reads the three scopes for a project at root and merges them by
// name, the later scope winning (local over project over user). Servers
// come back sorted by name. A file that cannot be read or parsed is
// reported in issues and contributes nothing; so is an entry that is
// not valid JSON for a server.
func Load(root string) (servers []Server, issues []string) {
	byName := map[string]Server{}
	seenPath := map[string]bool{}
	for _, scope := range Scopes {
		path := Path(scope, root)
		if abs := absPath(path); seenPath[abs] {
			continue // user and project root can be the same place
		} else {
			seenPath[abs] = true
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			issues = append(issues, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		raw, err := parseFile(data)
		if err != nil {
			issues = append(issues, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		for name, msg := range raw {
			var c ServerConfig
			if err := json.Unmarshal(msg, &c); err != nil {
				issues = append(issues, fmt.Sprintf("%s: server %q: %v", path, name, err))
				continue
			}
			byName[name] = Server{Name: name, Scope: scope, Path: path, Config: c}
		}
	}
	for _, s := range byName {
		servers = append(servers, s)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers, issues
}

// parseFile reads an MCP server file's entries, leaving them raw.
func parseFile(data []byte) (map[string]json.RawMessage, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var f struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Servers, nil
}

func absPath(p string) string { return approval.Path(p) }

// Put writes server name into the file of scope, keeping the file's other
// servers and fields as they are.
func Put(scope, root, name string, c ServerConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return edit(scope, root, func(m map[string]json.RawMessage) error {
		data, err := json.MarshalIndent(c, "    ", "  ")
		if err != nil {
			return err
		}
		m[name] = data
		return nil
	})
}

// Remove deletes server name from the file of scope; it reports false when
// the file has no such server.
func Remove(scope, root, name string) (bool, error) {
	found := false
	err := edit(scope, root, func(m map[string]json.RawMessage) error {
		if _, ok := m[name]; ok {
			found = true
			delete(m, name)
		}
		return nil
	})
	return found, err
}

// edit applies change to the "mcpServers" of the file of scope and writes
// it back, unless nothing was there and nothing is left.
func edit(scope, root string, change func(map[string]json.RawMessage) error) error {
	path := Path(scope, root)
	top := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil && len(strings.TrimSpace(string(data))) > 0:
		if err := json.Unmarshal(data, &top); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return err
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := top["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("%s: mcpServers: %w", path, err)
		}
	}
	if err := change(servers); err != nil {
		return err
	}
	raw, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	top["mcpServers"] = raw
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// The file may hold tokens in headers or env: keep it to the user.
	return fsutil.WriteAtomic(path, append(out, '\n'), 0o600)
}

// A reference is ${VAR} or ${VAR:-default}.
var reference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// Expand replaces ${VAR} with the value of the environment variable, and
// ${VAR:-default} with the default when it is unset or empty, as Claude
// Code does. Variables that are unset and have no default are returned in
// missing (and expand to nothing).
func Expand(s string, lookup func(string) (string, bool)) (out string, missing []string) {
	out = reference.ReplaceAllStringFunc(s, func(ref string) string {
		m := reference.FindStringSubmatch(ref)
		v, ok := lookup(m[1])
		if ok && v != "" {
			return v
		}
		if m[2] != "" {
			return m[3]
		}
		if !ok {
			missing = append(missing, m[1])
		}
		return v
	})
	return out, missing
}

// Expanded returns c with ${VAR} references expanded, and the variables
// that were unset without a default.
func (c ServerConfig) Expanded(lookup func(string) (string, bool)) (ServerConfig, []string) {
	var missing []string
	ex := func(s string) string {
		out, m := Expand(s, lookup)
		missing = append(missing, m...)
		return out
	}
	out := ServerConfig{Type: c.Type, Command: ex(c.Command), URL: ex(c.URL)}
	for _, a := range c.Args {
		out.Args = append(out.Args, ex(a))
	}
	if c.Env != nil {
		out.Env = map[string]string{}
		for k, v := range c.Env {
			out.Env[k] = ex(v)
		}
	}
	if c.Headers != nil {
		out.Headers = map[string]string{}
		for k, v := range c.Headers {
			out.Headers[k] = ex(v)
		}
	}
	sort.Strings(missing)
	var uniq []string
	for _, m := range missing {
		if len(uniq) == 0 || uniq[len(uniq)-1] != m {
			uniq = append(uniq, m)
		}
	}
	return out, uniq
}
