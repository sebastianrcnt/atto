package approval

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestDecisions(t *testing.T) {
	var d Decisions
	if d.Of("one", "hash") != Pending || d.Of("one", "") != Pending {
		t.Fatal("empty decisions approved content")
	}
	d.Set("one", "hash", true)
	if d.Of("one", "hash") != Approved || d.Of("one", "changed") != Pending {
		t.Fatal("approval is not content-specific")
	}
	d.Set("one", "hash", false)
	if d.Of("one", "hash") != Denied || len(d.Approved) != 0 {
		t.Fatal("denial did not replace approval")
	}
	d.Forget("one")
	if d.Of("one", "hash") != Pending {
		t.Fatal("decision survived revocation")
	}
	d.Set("project#one", "a", true)
	d.Set("project#two", "b", false)
	d.Set("project2#one", "c", true)
	d.ForgetPrefix("project#")
	if d.Of("project#one", "a") != Pending || d.Of("project#two", "b") != Pending || d.Of("project2#one", "c") != Approved {
		t.Fatal("prefix revocation crossed a project boundary")
	}
}

func TestLegacyDecisionsJSON(t *testing.T) {
	for _, text := range []string{`{"approved":{"one":"hash"}}`, `{"approved":{"one":"hash"},"denied":{"two":"hash2"}}`} {
		var d Decisions
		if err := json.Unmarshal([]byte(text), &d); err != nil {
			t.Fatal(err)
		}
		if d.Of("one", "hash") != Approved {
			t.Fatal("old approval was lost")
		}
		d.Init()
		data, err := json.Marshal(d)
		if err != nil || string(data) != text {
			t.Fatalf("JSON changed: %s: %v", data, err)
		}
	}
}

func TestPathNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project", "file")
	if Path(filepath.Dir(path)+string(filepath.Separator)+"."+string(filepath.Separator)+filepath.Base(path)) != path {
		t.Fatal("path not cleaned")
	}
	if !filepath.IsAbs(Path("relative")) {
		t.Fatal("relative path not made absolute")
	}
}
