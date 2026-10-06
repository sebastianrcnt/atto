package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/session"
)

// contextDir is a project with an AGENTS file and a skill, in a fresh home
// and ATTO_DIR.
func contextDir(t *testing.T) string {
	t.Helper()
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(config.EnvDir, filepath.Join(home, ".atto"))
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	proj := filepath.Join(home, "proj")
	for path, text := range map[string]string{
		filepath.Join(proj, ".git", "HEAD"):                       "x",
		filepath.Join(proj, "AGENTS.md"):                          "Run go test.",
		filepath.Join(home, ".atto", "skills", "pdf", "SKILL.md"): "---\nname: pdf\ndescription: PDFs\n---\nx",
		filepath.Join(home, ".atto", "settings.json"):             `{"skills":{"disabled":["atto-extensions"]},"defaultEffort":"low"}`,
		filepath.Join(home, ".atto", "models.json"):               `{"providers":{"t":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"m"}]}}}`,
		filepath.Join(proj, ".atto", "settings.json"):             `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"}]}]}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(proj)
	return proj
}

func TestContextCommand(t *testing.T) {
	contextDir(t)
	var out strings.Builder
	if err := RunContext(nil, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, s := range []string{
		"What atto loads for a session in ~/proj:",
		"~/proj/AGENTS.md  12 B",
		"Skills (from ~/.atto/skills)",
		"pdf  ~/.atto/skills/pdf/SKILL.md",
		"Stop  say done · ~/proj/.atto/settings.json",
		"model   m (t/m) · first available",
		"effort  low · from ~/.atto/settings.json",
		"~/proj/.atto/settings.json ", "project settings (hooks)\n",
	} {
		if !strings.Contains(filepath.ToSlash(text), s) {
			t.Errorf("atto context lacks %q:\n%s", s, text)
		}
	}

	out.Reset()
	if err := RunContext([]string{"-json", "-effort", "high"}, &out); err != nil {
		t.Fatal(err)
	}
	var l struct {
		Instructions []struct{ Path string }
		Skills       []struct{ Name string }
		Hooks        []struct{ Event, Command string }
		Effort       struct{ Name, Source string }
	}
	if err := json.Unmarshal([]byte(out.String()), &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Instructions) != 1 || len(l.Skills) != 1 || len(l.Hooks) != 1 || l.Effort.Name != "high" || l.Effort.Source != "flag" {
		t.Fatalf("json %+v", l)
	}
	if err := RunContext([]string{"extra"}, &out); err == nil {
		t.Fatal("arguments are an error")
	}
}

// Inside a session, atto context shows the model and effort that session
// uses, not the configured defaults.
func TestContextShowsSessionModel(t *testing.T) {
	proj := contextDir(t)
	os.WriteFile(filepath.Join(os.Getenv(config.EnvDir), "models.json"), []byte(`{"providers":{"t":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"m"},{"id":"n"}]}}}`), 0o644)
	w := session.New(proj)
	w.Append(session.Entry{Type: session.TypeModel, Provider: "t", Model: "n"})
	w.Append(session.Entry{Type: session.TypeEffort, Effort: "high"})
	w.Close()
	t.Setenv("ATTO_SESSION_ID", w.ID)
	var out strings.Builder
	if err := RunContext(nil, &out); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"model   n (t/n) · from the session", "effort  high · from the session"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("atto context lacks %q:\n%s", s, out.String())
		}
	}
}

// stream-json's init event carries what the run loaded.
func TestStreamJSONInitContext(t *testing.T) {
	bodies := imageModelServer(t, `["text"]`) // sets ATTO_DIR and models.json
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("Be brief."), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	quiet(t)
	os.Stdout = out
	if err := RunPrint(PrintOptions{Prompt: "hi", Format: "stream-json", NoSave: true}); err != nil {
		t.Fatal(err)
	}
	out.Seek(0, 0)
	sc := bufio.NewScanner(out)
	sc.Buffer(nil, 1<<20)
	sc.Scan()
	var init struct {
		Type    string
		Model   string
		Context struct {
			Instructions []struct{ Path string }
			Model        struct {
				ID     string
				Source string
			}
			SystemPrompt struct{ Bytes int } `json:"system_prompt"`
		}
	}
	if err := json.Unmarshal(sc.Bytes(), &init); err != nil {
		t.Fatal(err)
	}
	c := init.Context
	if init.Type != "init" || init.Model != "fake/m" || len(c.Instructions) != 1 || filepath.Base(c.Instructions[0].Path) != "AGENTS.md" ||
		c.Model.ID != "fake/m" || c.Model.Source != "default" || c.SystemPrompt.Bytes == 0 {
		t.Fatalf("init %s", sc.Bytes())
	}
	if len(bodies()) != 1 {
		t.Fatal("one request")
	}
	out.Close()

	// -p -v prints the summary to stderr first.
	errOut, err := os.CreateTemp(t.TempDir(), "err")
	if err != nil {
		t.Fatal(err)
	}
	defer errOut.Close()
	os.Stderr = errOut
	if err := RunPrint(PrintOptions{Prompt: "hi", Verbose: true, NoSave: true}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(errOut.Name())
	if s := string(data); !strings.HasPrefix(s, "◇ Loaded\n  AGENTS.md   ") || !strings.Contains(s, "AGENTS.md (9 B)") || !strings.Contains(s, "\n  Model       m · medium (") {
		t.Fatalf("stderr:\n%s", s)
	}
}

func TestReloadCommandPostsRequest(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "")
	if err := RunReload(nil, &strings.Builder{}); err == nil {
		t.Fatal("no session: an error")
	}
	t.Setenv("ATTO_SESSION_ID", "s1")
	t.Setenv(config.EnvAgent, "1")
	var out strings.Builder
	if err := RunReload(nil, &out); err != nil || !strings.Contains(out.String(), "[atto event]") {
		t.Fatalf("%v %q", err, out.String())
	}
	reload, rest := events.SplitReload(events.Drain("s1"))
	if !reload || len(rest) != 0 {
		t.Fatal("the request is in the session's inbox")
	}
}

// The built-in skills show up in atto context, marked as such, and
// settings.json can turn them off.
func TestContextBuiltinSkills(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Chdir(dir)
	var out strings.Builder
	if err := RunContext(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "atto-extensions") || !strings.Contains(out.String(), "· builtin") {
		t.Fatalf("atto context:\n%s", out.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"skills":{"disabled":["atto-extensions"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := RunContext(nil, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "atto-extensions") {
		t.Fatalf("disabled:\n%s", out.String())
	}
}
