package extensions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/mcp"
)

func TestOldExtensionApprovalFile(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	s := Spec{Name: "one", Path: filepath.Join(t.TempDir(), "one.ts"), Source: Project}
	old := struct {
		Approved map[string]string `json:"approved"`
		Denied   map[string]string `json:"denied,omitempty"`
	}{map[string]string{s.Path: "hash"}, map[string]string{"other": "denied"}}
	data, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(config.ExtensionApprovalsPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if ApprovalOf(s, "hash") != mcp.Approved {
		t.Fatal("old approval lost")
	}
	if err := editApprovals(func(*approvals) {}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(config.ExtensionApprovalsPath()); err != nil || string(got) != string(data) {
		t.Fatalf("format changed: %s %v", got, err)
	}
}

func TestConcurrentExtensionApprovals(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	root := t.TempDir()
	var wg sync.WaitGroup
	var specs []Spec
	for i := range 24 {
		s := Spec{Name: fmt.Sprint(i), Path: filepath.Join(root, fmt.Sprint(i)+".ts"), Source: Project}
		specs = append(specs, s)
		wg.Go(func() {
			if err := SetApproval(s, "hash", true); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, s := range specs {
		if ApprovalOf(s, "hash") != mcp.Approved {
			t.Errorf("lost extension %s", s.Name)
		}
	}
}
