package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func TestResumeUsageMatchesFullFileScan(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	models := config.ModelsFile{Providers: map[string]config.Provider{
		"p": {Models: []config.Model{{ID: "m", Cost: &ai.ModelCost{}}}},
	}}
	cwd := t.TempDir()
	w := session.New(cwd)
	w.Append(session.Entry{Type: session.TypeModel, Provider: "p", Model: "m"})
	root := w.Leaf()
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "abandoned"}, Usage: &provider.Usage{PromptTokens: 20, CachedTokens: 5, CompletionTokens: 2, CacheWriteTokens: 3, Cost: .01}})
	w.Branch(root)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "active"}, Usage: &provider.Usage{PromptTokens: 30, CachedTokens: 8, CompletionTokens: 4, CacheWriteTokens: 7, Cost: .02}})
	w.Close()
	_, entries, err := session.Load(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(models)
	writeTestFile(t, config.ModelsPath(), string(raw))
	a := startApp(t, cwd, Options{Session: w.ID})
	var old, got usageStats
	old.fromEntries(entries, models)
	a.ui.Do(func() { got = a.usage })
	if !reflect.DeepEqual(old, got) {
		t.Fatalf("usage differs: old %+v, new %+v", old, got)
	}
}

// fromEntries rebuilds totals from a resumed session.
func (u *usageStats) fromEntries(entries []session.Entry, models config.ModelsFile) {
	*u = usageStats{}
	var model config.ModelRef
	for _, e := range entries {
		if e.Type == session.TypeModel {
			model, _ = models.Find(e.Provider, e.Model)
		}
		if e.Usage != nil {
			u.add(*e.Usage)
			u.lastCost = model.Model.Cost
		}
	}
}
