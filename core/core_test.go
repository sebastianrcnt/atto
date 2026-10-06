package core

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
)

func setup(t *testing.T) (config.Settings, config.ModelsFile) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvDir, dir)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{
		"a":{"baseUrl":"http://a/v1","models":[{"id":"one","efforts":["low","high"]},{"id":"two"}]},
		"b":{"baseUrl":"http://b/v1","models":[{"id":"three"}]}}}`), 0o644)
	settings, models, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return settings, models
}

func TestPickModel(t *testing.T) {
	settings, models := setup(t)
	if m, err := PickModel(models, settings, "b/three"); err != nil || m.Model.ID != "three" {
		t.Fatal(m, err)
	}
	if _, err := PickModel(models, settings, "nope"); err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatal("unknown ids are errors", err)
	}
	settings.DefaultProvider, settings.DefaultModel = "a", "two"
	if m, _ := PickModel(models, settings, ""); m.Model.ID != "two" {
		t.Fatal("settings default", m.Model.ID)
	}
	settings.DefaultModel = "gone"
	if m, err := PickModel(models, settings, ""); err != nil || m.Model.ID == "" {
		t.Fatal("falls back to the first model", m, err)
	}
	if _, err := PickModel(config.ModelsFile{}, settings, ""); err == nil {
		t.Fatal("no models is an error")
	}
}

func TestEffort(t *testing.T) {
	settings, models := setup(t)
	if Effort(settings, "") != DefaultEffort || Effort(settings, "high") != "high" {
		t.Fatal("given, else default")
	}
	settings.DefaultEffort = "low"
	if Effort(settings, "") != "low" {
		t.Fatal("settings default")
	}
	one, _ := models.Find("", "a/one")
	if CheckEffort(one, "low") != nil || CheckEffort(one, "max") == nil {
		t.Fatal("levels are checked")
	}
}

func TestOpenRestoresLastSettings(t *testing.T) {
	setup(t)
	w := session.New(t.TempDir())
	w.Append(session.Entry{Type: session.TypeModel, Provider: "a", Model: "one"})
	w.Append(session.Entry{Type: session.TypeEffort, Effort: "low"})
	w.Append(session.Entry{Type: session.TypeName, Name: "first"})
	w.Append(session.Entry{Type: session.TypeModel, Provider: "b", Model: "three"})
	w.Append(session.Entry{Type: session.TypeName, Name: "second"})
	path := w.Path
	w.Close()

	saved, file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if saved.Model != "b/three" || saved.Effort != "low" || saved.Name != "second" {
		t.Fatalf("the last values win: %+v", saved)
	}
	if file.ID != saved.Header.ID {
		t.Fatal("the file reopens the same session")
	}
}

func TestEnvAndLeave(t *testing.T) {
	setup(t)
	env := Env("s1")
	if !slices.Contains(env, "ATTO_SESSION_ID=s1") || !slices.Contains(env, config.EnvAgent+"=1") {
		t.Fatal(env)
	}
	g, _ := goal.New("x")
	goal.Save("s1", g)
	if Leave("s1") != 0 {
		t.Fatal("no jobs to stop")
	}
	if g, _ := goal.Load("s1"); g != nil {
		t.Fatal("leaving removes the goal file")
	}
}
