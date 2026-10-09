package extensions

import (
	_ "embed"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// Statuses of an extension.
const (
	Loaded        = "loaded"
	Failed        = "failed"
	NeedsApproval = "needs approval"
	Disabled      = "disabled" // by settings.json
)

// DefaultTimeout bounds what atto waits for (see config.ExtensionSettings).
const DefaultTimeout = 5 * time.Second

// Types is atto.d.ts, the API's TypeScript declarations.
//
//go:embed atto.d.ts
var Types string

// TypesFile is the name Types is written under next to extensions.
const TypesFile = "atto.d.ts"

// Info describes an extension, for the Loaded block.
type Info struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Source   string   `json:"source"` // User, Project or Builtin
	Status   string   `json:"status"`
	Error    string   `json:"error,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Events   []string `json:"events,omitempty"`
	Hash     string   `json:"hash,omitempty"` // of the bundled code
	// Completes counts the atto.complete requests the extension made, per
	// model, since it loaded.
	Completes []CompleteStat `json:"completes,omitempty"`
}

// Command is a slash command an extension registered.
type Command struct {
	Name        string
	Description string
	Ext         string
}

// Options configures a Manager.
type Options struct {
	Cwd string
	// Agent, if set, gives atto.session its model.
	Agent *agent.Agent
	// Host is the front end; nil is a Headless host that drops everything.
	Host Host
}

// Ready is the status Inspect gives an extension that would load.
const Ready = "ready"

// settings reads the timeout and the disabled names from settings.json.
func settings() (time.Duration, []string) {
	s, _ := config.LoadSettings() // a broken file was reported by whoever loaded it first
	if s.Extensions == nil {
		return DefaultTimeout, nil
	}
	to := DefaultTimeout
	if s.Extensions.Timeout > 0 {
		to = time.Duration(s.Extensions.Timeout) * time.Second
	}
	return to, s.Extensions.Disabled
}

// sessionText reads the session's file: its latest name, and the last
// limit user and assistant messages of the active branch that carry text.
func (m *Manager) sessionText(limit int) (name string, msgs []provider.Message) {
	id, _ := m.session()
	path, err := session.Find(id)
	if err != nil {
		return "", nil // not written yet
	}
	_, entries, err := session.Load(path)
	if err != nil {
		return "", nil
	}
	for _, e := range session.Active(entries) {
		switch {
		case e.Type == session.TypeName:
			name = e.Name
		case e.Type == session.TypeMessage && e.Message != nil && (e.Message.Role == "user" || e.Message.Role == "assistant") && strings.TrimSpace(e.Message.Content) != "":
			msgs = append(msgs, provider.Message{Role: e.Message.Role, Content: e.Message.Content})
		}
	}
	if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	return name, msgs
}
