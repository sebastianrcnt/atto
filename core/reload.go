package core

import (
	"fmt"
	"strings"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/hooks"
)

// Reloaded is the outcome of Reload: what is loaded now and what changed.
type Reloaded struct {
	Loaded  Loaded
	Changes []Change
	// PromptChanged: the system prompt was rebuilt, so the next request
	// cannot reuse the cached prefix.
	PromptChanged bool
	// The configuration read, for the front end to take over.
	Settings config.Settings
	Models   config.ModelsFile
	Hooks    *hooks.Runner
	HookSrc  []config.HookSource
}

// Reload reads settings.json, models.json, the hooks, the AGENTS files,
// the skills and the extensions again for ag's session (id and transcript are what its hooks
// are told), keeping the conversation. The system prompt is rebuilt only if
// its text changed. The model in use stays even when models.json no longer
// has it (with a warning) until the user switches; if it is still there,
// its updated configuration applies. prev is the report to diff against.
//
// Nothing changes when a file cannot be read or parsed. Call it while no
// request is in flight (see agent.AtBoundary).
func Reload(ag *agent.Agent, id, transcript string, prev Loaded) (Reloaded, error) {
	settings, models, err := Load()
	if err != nil {
		return Reloaded{}, err
	}
	hk, src, err := LoadHooks(ag.Cwd)
	if err != nil {
		return Reloaded{}, err
	}
	r := Reloaded{Settings: settings, Models: models, Hooks: hk, HookSrc: src}
	if m := MCPOf(ag); m != nil {
		m.Reload() // before the prompt, which names the servers
	}
	r.PromptChanged = ag.Reload()
	if m := ExtensionsOf(ag); m != nil {
		m.Reload()
	}
	hk.SetSession(id, transcript)
	SetHooks(ag, hk)
	ag.SetCompaction(settings.Compaction)

	var warnings []string
	if m, _ := ag.Current(); m.Model.ID != "" {
		if ref, ok := models.Find(m.ProviderName, m.Model.ID); ok {
			ag.SetModel(ref)
		} else {
			warnings = append(warnings, fmt.Sprintf("%s is no longer configured; atto keeps using it until you switch (/model)", m))
		}
	}
	r.Loaded = Collect(ag, src, prev.Model.Origin, prev.Effort.Origin)
	r.Loaded.Warnings = append(warnings, r.Loaded.Warnings...)
	r.Changes = Diff(prev, r.Loaded)
	return r, nil
}

// PromptNote says what the reload did to the system prompt.
func (r Reloaded) PromptNote() string {
	if r.PromptChanged {
		return "system prompt changed; the next request re-reads the prompt"
	}
	return "system prompt unchanged; the prompt cache is kept"
}

// ForModel is the reload's report for the model, as event text.
func (r Reloaded) ForModel() string {
	var b strings.Builder
	b.WriteString("Reload applied")
	if len(r.Changes) == 0 {
		b.WriteString(": nothing changed")
	} else {
		var cs []string
		for _, c := range r.Changes {
			cs = append(cs, c.String())
		}
		b.WriteString(". " + strings.Join(cs, "; "))
	}
	b.WriteString(". ")
	if r.PromptChanged {
		b.WriteString("The system prompt was rebuilt with the current AGENTS files and skills.")
	} else {
		b.WriteString("The system prompt is unchanged.")
	}
	for _, w := range r.Loaded.Warnings {
		b.WriteString(" Warning: " + w + ".")
	}
	return b.String()
}
