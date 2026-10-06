package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelsJSONCompaction(t *testing.T) {
	for _, key := range []string{"ATTO_AGENT", "ATTO_SUBAGENT", "ATTO_SESSION_ID"} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	for name, data := range map[string]string{
		"models.json":   `{"providers":{"local":{"baseUrl":"http://localhost/v1","models":[{"id":"luna","contextWindow":1050000,"maxTokens":128000,"cost":{"input":0.1,"tiers":[{"inputTokensAbove":272000,"input":0.2}]}}]}}}`,
		"settings.json": `{"compaction":{"limits":{"local/luna":400000}}}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	settings, models, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	m, err := PickModel(models, settings, "local/luna")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Model.Cost.ContextPriceBoundary(); got != 272000 {
		t.Fatal(got)
	}
	a, _, err := NewAgent(dir, m, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, cap := a.CompactionLimit(); got != 360000 || cap != 400000 {
		t.Fatal(got, cap)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"compaction":{"limits":{"local/luna":0}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Reload(a, "test", "", Loaded{}); err != nil {
		t.Fatal(err)
	}
	if got, cap := a.CompactionLimit(); got != 922000 || cap != 0 {
		t.Fatal(got, cap)
	}
}
