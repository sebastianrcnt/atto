package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

func TestOldApprovalFile(t *testing.T) {
	root := env(t)
	s := Server{Name: "one", Path: Path(ScopeProject, root), Scope: ScopeProject, Config: ServerConfig{Command: "one"}}
	old := struct {
		Approved map[string]string `json:"approved"`
		Denied   map[string]string `json:"denied,omitempty"`
		All      []string          `json:"all,omitempty"`
	}{map[string]string{approvalKey(s.Path, s.Name): s.Config.Hash()}, map[string]string{"other": "denied"}, []string{s.Path}}
	data, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(config.MCPApprovalsPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if ApprovalOf(s) != Approved {
		t.Fatal("old approval lost")
	}
	s.Config.Command = "changed"
	if ApprovalOf(s) != Approved {
		t.Fatal("legacy all approval lost")
	}
	if err := editApprovals(func(*approvals) {}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(config.MCPApprovalsPath()); err != nil || string(got) != string(data) {
		t.Fatalf("on-disk format changed: %s %v", got, err)
	}
}

func TestConcurrentApprovals(t *testing.T) {
	root := env(t)
	var wg sync.WaitGroup
	servers := make([]Server, 24)
	for i := range servers {
		servers[i] = Server{Name: fmt.Sprint(i), Path: Path(ScopeProject, root), Scope: ScopeProject, Config: ServerConfig{Command: "one"}}
		s := servers[i]
		wg.Go(func() {
			if err := Approve(s); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, s := range servers {
		if ApprovalOf(s) != Approved {
			t.Errorf("lost approval %s", s.Name)
		}
	}
	if _, err := fsutil.ReadJSON[approvals](config.MCPApprovalsPath()); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for path, perm := range map[string]os.FileMode{config.MCPApprovalsPath(): 0o600, filepath.Dir(config.MCPApprovalsPath()): 0o700} {
			st, err := os.Stat(path)
			if err != nil || st.Mode().Perm() != perm {
				t.Fatalf("mode: %s %v %v", path, st, err)
			}
		}
	}
}
