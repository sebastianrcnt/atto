package core

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/extensions"
)

func TestExtensionsInLoadedAndReload(t *testing.T) {
	atto, repo, cwd := project(t)
	user := filepath.Join(atto, "extensions")
	writeFile(t, filepath.Join(user, "guard.ts"), `export default (atto: any) => {
  atto.on("tool_call", () => {});
  atto.on("user_prompt", () => {});
  atto.registerCommand("check", { description: "Check", handler() {} });
}`)
	writeFile(t, filepath.Join(user, "broken.ts"), "export default () => {\n  throw new Error('nope')\n}")
	writeFile(t, filepath.Join(repo, ".atto", "extensions", "proj.js"), "export default () => {}")
	ag, _ := open(t, cwd)
	m := LoadExtensions(ag, nil)
	t.Cleanup(m.Close)
	if ExtensionsOf(ag) != m {
		t.Fatal("the agent runs the extensions")
	}
	l := Collect(ag, nil, FromDefault, FromDefault)

	var row string
	for _, r := range l.Summary() {
		if r.Label == "Extensions" {
			row = r.Text
		}
	}
	if row != "3: guard, autorename, diff; 1 failed; 1 needs approval" {
		t.Fatalf("summary %q", row)
	}
	var details []string
	for _, s := range l.Details() {
		if s.Title == "Extensions" {
			for _, r := range s.Rows {
				details = append(details, r.Label+" "+r.Text)
			}
		}
	}
	got := strings.Join(details, "\n")
	for _, want := range []string{
		"guard loaded · user · ", "commands /check · on tool_call, user_prompt",
		"broken failed: Error: nope", "broken.ts:2",
		"proj needs approval: /extensions approve proj (or: atto extensions approve proj)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("details lack %q:\n%s", want, got)
		}
	}
	if len(l.Warnings) != 2 || !strings.Contains(l.Warnings[0], "extension broken failed") {
		t.Errorf("warnings %q", l.Warnings)
	}
	if !slices.ContainsFunc(l.Context, func(p Part) bool { return p.Name == "user_prompt extensions" && strings.Contains(p.Detail, "guard") }) {
		t.Errorf("context %+v", l.Context)
	}

	// Fixed, approved and edited: the reload says so, and the agent hears
	// of failures in its report.
	writeFile(t, filepath.Join(user, "broken.ts"), "export default () => {}")
	if _, err := extensions.Approve(cwd, "proj"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(user, "guard.ts"), "export default () => { syntax error")
	r, err := Reload(ag, "s1", "", l)
	if err != nil {
		t.Fatal(err)
	}
	var changes []string
	for _, c := range r.Changes {
		if c.What == "extension" { // the hooks differ only because l was collected without them
			changes = append(changes, c.String())
		}
	}
	want := []string{"changed extension broken", "changed extension guard", "changed extension proj"}
	if !slices.Equal(changes, want) {
		t.Fatalf("changes %q, want %q", changes, want)
	}
	if rep := r.ForModel(); !strings.Contains(rep, "Warning: extension guard failed: "+filepath.Join(user, "guard.ts")+":1:") {
		t.Fatalf("the model learns where its extension broke: %s", rep)
	}
	if in := r.Loaded.Extensions; len(in) != 5 || in[0].Name != "broken" || in[0].Status != extensions.Loaded || in[2].Status != extensions.Loaded {
		t.Fatalf("%+v", in)
	}
}
