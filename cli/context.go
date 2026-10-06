package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/session"
)

const contextUsage = `usage: atto context [-json] [-m provider/id] [-effort level]

Shows what a session started in this directory loads: the AGENTS.md files
and skills in its system prompt, the hooks, the extensions (found, not
run: "ready" would load), the settings and models files, and the model
and effort it would use, with where each came from. Run inside a session
(ATTO_SESSION_ID), the model and effort are the ones that session uses.`

// RunContext implements "atto context".
func RunContext(args []string, out io.Writer) error {
	fs := newFlags("context")
	asJSON := fs.Bool("json", false, "")
	model := fs.String("m", "", "")
	effort := fs.String("effort", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fmt.Errorf("%s", contextUsage)
	}
	settings, models, err := core.Load()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var saved core.Saved // the running session's choices outrank the defaults
	if id := os.Getenv("ATTO_SESSION_ID"); id != "" {
		if path, err := session.Find(id); err == nil {
			saved, _ = core.Read(path)
		}
	}
	ref, modelFrom, err := core.PickModelFrom(models, settings, *model, saved.Model)
	if err != nil && !errors.Is(err, core.ErrNoModels) {
		return err
	}
	level, effortFrom := core.EffortFrom(settings, *effort, saved.Effort)
	ag := agent.New(ref, level, cwd)
	_, src, err := core.LoadHooks(cwd)
	if err != nil {
		return err
	}
	l := core.Collect(ag, src, modelFrom, effortFrom)
	l.Extensions = extensions.Inspect(cwd) // found, not run
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(l)
	}
	_, err = io.WriteString(out, l.Text())
	return err
}

// RunReload implements "atto reload": run by the agent (or anyone with the
// session's ID), it asks the front end running the session to read
// AGENTS.md, skills, hooks, settings.json and models.json again. The
// reload happens after the current command, between steps, and its result
// arrives as an [atto event].
func RunReload(args []string, out io.Writer) error {
	fs := newFlags("reload")
	session := sessionFlag(fs)
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fmt.Errorf("usage: atto reload [-session id]\n\nReloads AGENTS.md, skills, hooks, extensions, settings.json and models.json in the running session.")
	}
	if err := requireSession(*session); err != nil {
		return err
	}
	if err := events.RequestReload(*session); err != nil {
		return err
	}
	fmt.Fprintln(out, "Reload requested. atto applies it once this command has finished and reports what changed in an [atto event].")
	return nil
}
