package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
)

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// project is a git project with AGENTS files, skills, hooks and a model:
// <tmp>/home is the home directory, <tmp>/atto ATTO_DIR, <tmp>/repo the
// project and <tmp>/repo/sub the working directory.
func project(t *testing.T) (atto, repo, cwd string) {
	t.Helper()
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(tmp, "home")
	atto, repo = filepath.Join(tmp, "atto"), filepath.Join(tmp, "repo")
	cwd = filepath.Join(repo, "sub")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(config.EnvDir, atto)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	writeFile(t, filepath.Join(repo, ".git", "HEAD"), "x")
	writeFile(t, filepath.Join(atto, "AGENTS.md"), "global rules")
	writeFile(t, filepath.Join(repo, "AGENTS.override.md"), "override rules")
	writeFile(t, filepath.Join(repo, "AGENTS.md"), "shadowed rules")
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), strings.Repeat("x", 40*1024)) // over the cap
	writeFile(t, filepath.Join(atto, "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: Work with PDFs\n---\nsteps")
	writeFile(t, filepath.Join(atto, "skills", "nodesc", "SKILL.md"), "---\nname: nodesc\n---\nx")
	writeFile(t, filepath.Join(repo, ".atto", "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: Another\n---\nx")
	writeFile(t, filepath.Join(repo, ".atto", "skills", "hidden", "SKILL.md"), "---\nname: hidden\ndescription: By command\ndisable-model-invocation: true\n---\nx")
	writeFile(t, filepath.Join(atto, "settings.json"), `{"skills":{"disabled":["atto-extensions"]},"defaultProvider":"a","defaultModel":"two","hooks":{
		"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"./check.sh --strict"}]}],
		"Stop":[{"hooks":[{"type":"http","url":"http://127.0.0.1:9/stop"}]}]}}`)
	writeFile(t, filepath.Join(atto, "models.json"), `{"providers":{"a":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"one"},{"id":"two","input":["text","image"]}]}}}`)
	return atto, repo, cwd
}

// open builds an agent for cwd the way the front ends do.
func open(t *testing.T, cwd string) (*agent.Agent, Loaded) {
	t.Helper()
	settings, models, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	m, mf, err := PickModelFrom(models, settings, "", "")
	if err != nil {
		t.Fatal(err)
	}
	e, ef := EffortFrom(settings, "high", "")
	ag, _, src, err := NewAgentSources(cwd, m, e)
	if err != nil {
		t.Fatal(err)
	}
	ag.SetStart(time.Date(2026, 1, 2, 3, 0, 0, 0, time.Local))
	return ag, Collect(ag, src, mf, ef)
}

func TestCollect(t *testing.T) {
	atto, repo, cwd := project(t)
	_, l := open(t, cwd)

	type in struct {
		path      string
		truncated bool
		skipped   string
	}
	var got []in
	for _, x := range l.Instructions {
		got = append(got, in{x.Path, x.Truncated, x.Skipped})
	}
	want := []in{
		{filepath.Join(atto, "AGENTS.md"), false, ""},
		{filepath.Join(repo, "AGENTS.override.md"), false, ""},
		{filepath.Join(cwd, "AGENTS.md"), true, ""},
		{filepath.Join(repo, "AGENTS.md"), false, "shadowed by AGENTS.override.md"},
	}
	if len(got) != len(want) {
		t.Fatalf("instructions %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("instruction %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if x := l.Instructions[2]; x.Bytes != 40*1024 || x.Kept >= x.Bytes || x.Kept == 0 {
		t.Errorf("truncated file sizes: %+v", x)
	}

	if len(l.Skills) != 2 || l.Skills[0].Name != "pdf" || l.Skills[0].Dir != filepath.Join(atto, "skills") ||
		l.Skills[1].Name != "hidden" || !l.Skills[1].Hidden || l.Skills[1].Dir != filepath.Join(repo, ".atto", "skills") {
		t.Errorf("skills %+v", l.Skills)
	}
	var skipped []string
	for _, is := range l.SkillIssues {
		if is.Skipped {
			skipped = append(skipped, is.Reason)
		}
	}
	if len(skipped) != 2 || !strings.Contains(strings.Join(skipped, "|"), "description is required") ||
		!strings.Contains(strings.Join(skipped, "|"), `skill "pdf" ignored`) {
		t.Errorf("skipped skills %q", skipped)
	}
	if len(l.SkillDirs) != 2 {
		t.Errorf("skill dirs %q", l.SkillDirs)
	}

	if len(l.Hooks) != 2 || l.Hooks[0] != (Hook{File: config.SettingsPath(), Event: "PreToolUse", Matcher: "Bash", Type: "command", Command: "./check.sh --strict"}) ||
		l.Hooks[1].Event != "Stop" || l.Hooks[1].Type != "http" || l.Hooks[1].Command != "http://127.0.0.1:9/stop" {
		t.Errorf("hooks %+v", l.Hooks)
	}

	if l.Model.ID != "a/two" || l.Model.Origin != FromSettings || l.Model.Path != config.SettingsPath() {
		t.Errorf("model %+v", l.Model)
	}
	if l.Effort.Name != "high" || l.Effort.Origin != FromFlag {
		t.Errorf("effort %+v", l.Effort)
	}
	exists := map[string]bool{}
	for _, f := range l.Config {
		exists[f.Role] = f.Exists
	}
	if !exists["settings"] || !exists["models"] || exists["credentials"] || exists["project settings (hooks)"] {
		t.Errorf("config %+v", l.Config)
	}
	if l.Prompt.Bytes == 0 || len(l.Prompt.Parts) != 5 {
		t.Errorf("prompt %+v", l.Prompt)
	}
	var ctx []string
	for _, p := range l.Context {
		ctx = append(ctx, p.Name+": "+p.Detail)
	}
	if c := strings.Join(ctx, "\n"); !strings.Contains(c, "images: sent to the model") || !strings.Contains(c, "Stop hooks") {
		t.Errorf("context %s", c)
	}

	// It is what stream-json's init event and the server send.
	data, err := json.Marshal(l)
	if err != nil || !strings.Contains(string(data), `"instructions":[{"path":`) || !strings.Contains(string(data), `"source":"settings"`) {
		t.Fatalf("json %s %v", data, err)
	}
}

func TestLoadedSummaryAndDetails(t *testing.T) {
	_, _, cwd := project(t)
	_, l := open(t, cwd)
	rows := map[string]string{}
	for _, r := range l.Summary() {
		rows[r.Label] = r.Text
	}
	checks := map[string][]string{
		"AGENTS.md": {"~/../atto/AGENTS.md (12 B)", "(40.0 KB, truncated at 32 KiB)", "; 1 skipped"},
		"Skills":    {"2: pdf, hidden", "; 2 skipped"},
		"Hooks":     {"2: PreToolUse(Bash), Stop · from "},
		"Model":     {"two · high (model from ", "settings.json, effort from -effort)"},
		"Prompt":    {"system prompt: base, environment, 3 AGENTS files, 1 skill"},
	}
	for label, subs := range checks {
		for _, s := range subs {
			if after, ok := strings.CutPrefix(s, "~/../"); ok { // the ATTO_DIR is outside the home here
				s = after
			}
			if !strings.Contains(filepath.ToSlash(rows[label]), s) {
				t.Errorf("%s: %q lacks %q", label, rows[label], s)
			}
		}
	}

	text := l.Text()
	for _, s := range []string{
		"skipped: shadowed by AGENTS.override.md",
		"truncated at 32 KiB (",
		"not in the prompt; run with /skill:hidden",
		"skipped  ",
		"PreToolUse [Bash]  ./check.sh --strict",
		"POST http://127.0.0.1:9/stop",
		"effort  high · from -effort",
		"credentials · not found",
		"session date 2026-01-02",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("details lack %q:\n%s", s, text)
		}
	}
}

func TestOrigins(t *testing.T) {
	settings, models := setup(t)
	if _, o, _ := PickModelFrom(models, settings, "b/three", "a/one"); o != FromFlag {
		t.Error("flag", o)
	}
	if m, o, _ := PickModelFrom(models, settings, "", "a/one"); o != FromSession || m.Model.ID != "one" {
		t.Error("session", o)
	}
	if _, o, _ := PickModelFrom(models, settings, "", "gone/model"); o != FromDefault {
		t.Error("a session model that is gone falls through", o)
	}
	settings.DefaultProvider, settings.DefaultModel, settings.DefaultEffort = "b", "three", "low"
	if _, o, _ := PickModelFrom(models, settings, "", ""); o != FromSettings {
		t.Error("settings", o)
	}
	for _, c := range []struct {
		flag, saved, want string
		o                 Origin
	}{{"high", "low", "high", FromFlag}, {"", "max", "max", FromSession}, {"", "", "low", FromSettings}} {
		if e, o := EffortFrom(settings, c.flag, c.saved); e != c.want || o != c.o {
			t.Errorf("effort %+v: %s %s", c, e, o)
		}
	}
	settings.DefaultEffort = ""
	if e, o := EffortFrom(settings, "", ""); e != DefaultEffort || o != FromDefault {
		t.Error("default effort", e, o)
	}
	if got := (Choice{Origin: FromDefault}).From("-m"); got != "first available" {
		t.Error(got)
	}
}

func TestReload(t *testing.T) {
	atto, repo, cwd := project(t)
	ag, l := open(t, cwd)
	prompt := ag.SystemPrompt()

	// Nothing changed: the prompt stays as it is.
	r, err := Reload(ag, "s1", "", l)
	if err != nil {
		t.Fatal(err)
	}
	if r.PromptChanged || len(r.Changes) != 0 || ag.SystemPrompt() != prompt || !strings.Contains(r.ForModel(), "nothing changed") {
		t.Fatalf("unchanged reload: %+v %v", r.Changes, r.PromptChanged)
	}
	if !strings.Contains(r.PromptNote(), "unchanged") {
		t.Error(r.PromptNote())
	}

	// An edited AGENTS file, a new skill, a removed hook and a model that
	// is gone from models.json.
	writeFile(t, filepath.Join(repo, "AGENTS.override.md"), "new override rules")
	writeFile(t, filepath.Join(atto, "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Ship it\n---\nx")
	writeFile(t, filepath.Join(atto, "settings.json"), `{"skills":{"disabled":["atto-extensions"]},"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://127.0.0.1:9/stop"}]}]}}`)
	writeFile(t, filepath.Join(atto, "models.json"), `{"providers":{"a":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"one"}]}}}`)
	r, err = Reload(ag, "s1", "", r.Loaded)
	if err != nil {
		t.Fatal(err)
	}
	if !r.PromptChanged || ag.SystemPrompt() == prompt || !strings.Contains(ag.SystemPrompt(), "new override rules") ||
		!strings.Contains(ag.SystemPrompt(), "<name>deploy</name>") {
		t.Fatalf("the prompt was not rebuilt:\n%s", ag.SystemPrompt())
	}
	var changes []string
	for _, c := range r.Changes {
		changes = append(changes, c.String())
	}
	got := strings.Join(changes, "\n")
	for _, s := range []string{
		"changed AGENTS file " + ShortPath(filepath.Join(repo, "AGENTS.override.md")),
		"added skill deploy",
		"removed hook PreToolUse [Bash]: ./check.sh --strict",
		"changed config " + ShortPath(config.SettingsPath()),
		"changed config " + ShortPath(config.ModelsPath()),
	} {
		if !strings.Contains(got, s) {
			t.Errorf("changes lack %q:\n%s", s, got)
		}
	}
	if m, _ := ag.Current(); m.Model.ID != "two" {
		t.Errorf("the model in use must stay: %s", m)
	}
	if len(r.Loaded.Warnings) != 1 || !strings.Contains(r.Loaded.Warnings[0], "a/two is no longer configured") ||
		!strings.Contains(r.ForModel(), "Warning: a/two") {
		t.Errorf("warnings %q", r.Loaded.Warnings)
	}
	if r.Hooks == nil || ag.Hooks == nil || len(r.Loaded.Hooks) != 1 {
		t.Errorf("hooks not swapped: %+v", r.Loaded.Hooks)
	}
	if !strings.Contains(r.PromptNote(), "the next request re-reads the prompt") {
		t.Error(r.PromptNote())
	}

	// No hooks left: the agent runs none (not a typed nil).
	writeFile(t, filepath.Join(atto, "settings.json"), `{}`)
	if r, err = Reload(ag, "s1", "", r.Loaded); err != nil || ag.Hooks != nil || r.Hooks != nil {
		t.Fatalf("hooks removed: %v %v", ag.Hooks, err)
	}

	// A broken file changes nothing.
	prompt = ag.SystemPrompt()
	writeFile(t, filepath.Join(repo, "AGENTS.override.md"), "rules that must not load")
	writeFile(t, filepath.Join(atto, "settings.json"), `{broken`)
	if _, err := Reload(ag, "s1", "", r.Loaded); err == nil || ag.SystemPrompt() != prompt {
		t.Fatalf("a parse error must leave everything: %v", err)
	}
}

func TestLoadedAgentJSON(t *testing.T) {
	atto, _, cwd := project(t)
	writeFile(t, filepath.Join(atto, "settings.json"), `{"agents":{"enabled":true}}`)
	writeFile(t, filepath.Join(atto, "agents", "Bad.md"), "invalid role name")
	_, loaded := open(t, cwd)
	raw, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["agents"]) != "true" || len(fields["agent_presets"]) == 0 {
		t.Fatalf("agent context fields: %s", raw)
	}
	// Keep the old subagent context fields for external context -json consumers.
	for old, current := range map[string]string{
		"subagents": "agents", "subagent_presets": "agent_presets", "subagent_preset_warnings": "agent_preset_warnings",
	} {
		if len(fields[current]) == 0 || string(fields[old]) != string(fields[current]) {
			t.Errorf("context aliases %s/%s: %s", old, current, raw)
		}
	}
}
