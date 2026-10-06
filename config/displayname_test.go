package config

import (
	"os"
	"testing"
)

func loadTwoProviders(t *testing.T) ModelsFile {
	t.Helper()
	t.Setenv("ATTO_DIR", t.TempDir())
	if err := os.WriteFile(ModelsPath(), []byte(`{"providers":{
		"openai":      {"baseUrl":"http://a/v1","apiKey":"k","models":[{"id":"gpt-6-luna","name":"GPT-6 Luna"},{"id":"gpt-6","name":"GPT-6"}]},
		"opencode-go": {"baseUrl":"http://b/v1","apiKey":"k","models":[{"id":"luna","name":"GPT-6 Luna"},{"id":"kimi"}]}
	}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A name two providers have shows the provider; the others stay as they are.
func TestDisplayNameShowsProviderOnCollision(t *testing.T) {
	m := loadTwoProviders(t)
	for _, c := range []struct{ provider, id, want string }{
		{"openai", "gpt-6-luna", "GPT-6 Luna · openai"},
		{"opencode-go", "luna", "GPT-6 Luna · opencode-go"},
		{"openai", "gpt-6", "GPT-6"},
		{"opencode-go", "kimi", "kimi"}, // no name: the ID
	} {
		r, ok := m.Find(c.provider, c.id)
		if !ok {
			t.Fatalf("no %s/%s", c.provider, c.id)
		}
		if got := m.DisplayName(r); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.provider, c.id, got, c.want)
		}
	}
	// Computed once, and the same through a copy (the App keeps a value).
	cp := m
	r, _ := cp.Find("openai", "gpt-6-luna")
	if got := cp.DisplayName(r); got != "GPT-6 Luna · openai" {
		t.Errorf("copy: %q", got)
	}
}

// Two models of one provider with the same name do not collide, and a
// ModelsFile built by hand works without the cache.
func TestDisplayNameSameProviderAndNoCache(t *testing.T) {
	m := ModelsFile{Providers: map[string]Provider{
		"a": {Models: []Model{{ID: "1", Name: "Same"}, {ID: "2", Name: "Same"}, {ID: "3", Name: "Twice"}}},
		"b": {Models: []Model{{ID: "3", Name: "Twice"}}},
	}}
	if got := m.DisplayName(ModelRef{ProviderName: "a", Model: m.Providers["a"].Models[0]}); got != "Same" {
		t.Errorf("one provider: %q", got)
	}
	if got := m.DisplayName(ModelRef{ProviderName: "b", Model: m.Providers["b"].Models[0]}); got != "Twice · b" {
		t.Errorf("two providers: %q", got)
	}
	if got := (ModelsFile{}).DisplayName(ModelRef{ProviderName: "x", Model: Model{ID: "y"}}); got != "y" {
		t.Errorf("empty list: %q", got)
	}
}
