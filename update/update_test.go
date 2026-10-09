package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.9", true},
		{"v0.1.10", "v0.1.9", true},
		{"v1.0.0", "v1.0.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v0.1.0", "dev", true},
		{"dev", "v0.1.0", false},
		// The edge ordering: v0.0.2 < v0.0.3-dev.1 < v0.0.3-dev.14 < v0.0.3.
		{"v0.0.3-dev.1", "v0.0.2", true},
		{"v0.0.2", "v0.0.3-dev.1", false},
		{"v0.0.3-dev.14", "v0.0.3-dev.1", true}, // numeric, not lexical
		{"v0.0.3-dev.1", "v0.0.3-dev.14", false},
		{"v0.0.3", "v0.0.3-dev.14", true},
		{"v0.0.3-dev.14", "v0.0.3", false},
		{"v0.0.4-dev.0", "v0.0.3", true},
		// Build metadata is ignored.
		{"v0.0.3-dev.14+abc1234", "v0.0.3-dev.14+def5678", false},
		{"v0.0.3-dev.14+abc1234", "v0.0.3-dev.13+zzz", true},
		{"v0.0.3+abc", "v0.0.3", false},
		// Go pseudo-versions are commits before their release.
		{"v0.2.0", "v0.2.0-0.20261005-abcdef", true},
		{"v0.2.0-0.20261005-abcdef", "v0.2.0", false},
		{"v0.0.3-0.20261005120000-abcdef123456", "v0.0.2", true},
		{"v0.0.3-dev.1", "v0.0.3-0.20261005120000-abcdef123456", true}, // numeric < alphanumeric
		{"v0.0.3-0.20261006-bbbbbb", "v0.0.3-0.20261005-aaaaaa", true},
		// Garbage never wins.
		{"v1.2", "v0.0.1", false},
		{"v1.2.3-", "v0.0.1", false},
		{"v1.x.3", "v0.0.1", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// fakeGitHub serves the release API and downloads for both channels.
func fakeGitHub(t *testing.T, edgeName string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.0.2","name":"v0.0.2"}`)
	})
	mux.HandleFunc("/api/tags/edge", func(w http.ResponseWriter, r *http.Request) {
		if edgeName == "" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"tag_name":"edge","name":%q}`, edgeName)
	})
	for tag, body := range map[string]string{"edge": "edge-binary", "v0.0.2": "stable-binary"} {
		sum := sha256.Sum256([]byte(body))
		mux.HandleFunc("/dl/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), Asset())
		})
		mux.HandleFunc("/dl/"+tag+"/"+Asset(), func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(SetEndpoints(srv.URL+"/api/", srv.URL+"/dl/"))
}

func TestLatestChannels(t *testing.T) {
	fakeGitHub(t, "v0.0.3-dev.14+abc1234")
	ctx := context.Background()

	st, err := Latest(ctx, "stable")
	if err != nil || st != (Release{Tag: "v0.0.2", Version: "v0.0.2"}) {
		t.Fatal(st, err)
	}
	// Anything unknown is stable.
	if r, _ := Latest(ctx, ""); r != st {
		t.Fatal(r)
	}
	ed, err := Latest(ctx, "edge")
	if err != nil || ed != (Release{Tag: "edge", Version: "v0.0.3-dev.14+abc1234"}) {
		t.Fatal(ed, err)
	}
}

func TestLatestEdgeName(t *testing.T) {
	for name, ok := range map[string]bool{
		"v0.0.3-dev.1+abc1234":      true,
		"atto v0.0.3-dev.1+abc1234": true,
		"edge":                      false,
		"":                          false, // also what a 404 looks like
	} {
		fakeGitHub(t, name)
		_, err := Latest(context.Background(), "edge")
		if (err == nil) != ok {
			t.Errorf("name %q: err = %v", name, err)
		}
	}
}

func TestPlan(t *testing.T) {
	stable := Release{Tag: "v0.0.2", Version: "v0.0.2"}
	edge := Release{Tag: "edge", Version: "v0.0.3-dev.14+abc1234"}
	for _, c := range []struct {
		name          string
		rel           Release
		cur           string
		switching     bool
		install, down bool
	}{
		{"stable upgrade", stable, "v0.0.1", false, true, false},
		{"stable current", stable, "v0.0.2", false, false, false},
		{"edge upgrade", edge, "v0.0.3-dev.13+aaa", false, true, false},
		{"edge current", edge, "v0.0.3-dev.14+abc1234", false, false, false},
		{"stable to edge", edge, "v0.0.2", true, true, false},
		{"dev build takes anything", stable, "dev", false, true, false},
		// Stable is lower than an edge build: only a channel switch installs it.
		{"edge to stable, update", stable, "v0.0.3-dev.14+abc1234", false, false, false},
		{"edge to stable, switch", stable, "v0.0.3-dev.14+abc1234", true, true, true},
		{"already on it, switch", stable, "v0.0.2", true, false, false},
	} {
		install, down := Plan(c.rel, c.cur, c.switching)
		if install != c.install || down != c.down {
			t.Errorf("%s: Plan = %v, %v", c.name, install, down)
		}
	}
}

func TestInstallEdge(t *testing.T) {
	fakeGitHub(t, "v0.0.3-dev.1+abc1234")
	exe := filepath.Join(t.TempDir(), "atto")
	os.WriteFile(exe, []byte("old"), 0o755)
	// Edge assets live under the fixed "edge" tag, not the version name.
	if err := InstallRelease(context.Background(), EdgeTag, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "edge-binary" {
		t.Fatalf("got %q", b)
	}
}

func TestChecksum(t *testing.T) {
	sums := "AAAA  atto_linux_amd64\nbbbb *atto_windows_arm64.exe\n"
	if s, err := checksum(sums, "atto_linux_amd64"); err != nil || s != "aaaa" {
		t.Fatal(s, err)
	}
	if s, err := checksum(sums, "atto_windows_arm64.exe"); err != nil || s != "bbbb" {
		t.Fatal(s, err)
	}
	if _, err := checksum(sums, "atto_plan9_386"); err == nil {
		t.Fatal("missing asset must fail")
	}
}

func TestReplace(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "atto")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := replace(exe, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}

func TestManaged(t *testing.T) {
	t.Setenv("GOBIN", "")
	if Managed("/opt/homebrew/Cellar/atto/0.1.0/bin/atto") == "" || Managed("/Users/x/go/bin/atto") == "" {
		t.Fatal("brew and go install builds are managed")
	}
	if Managed("/Users/x/.local/bin/atto") != "" {
		t.Fatal("the install script's location is ours")
	}
}

func TestAvailableUsesCache(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	data, _ := json.Marshal(check{Checked: time.Now(), Latest: "v9.0.0"})
	os.WriteFile(checkFile(), data, 0o644)

	oldV, oldC := Version, Channel
	defer func() { Version, Channel = oldV, oldC }()
	Version, Channel = "v0.1.0", Stable
	if got := Available(context.Background()); got != "v9.0.0" {
		t.Fatalf("got %q", got)
	}
	Version = "dev"
	if got := Available(context.Background()); got != "" {
		t.Fatalf("dev builds get no notice, got %q", got)
	}
}

func TestAvailableFollowsBinaryChannel(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	fakeGitHub(t, "v0.0.3-dev.14+abc1234")
	oldV, oldC := Version, Channel
	defer func() { Version, Channel = oldV, oldC }()
	Version = "v0.0.2"

	// No channel (go install, local build): no check at all.
	Channel = ""
	if got := Available(context.Background()); got != "" {
		t.Fatalf("no channel: got %q", got)
	}
	if _, err := os.Stat(checkFile()); err == nil {
		t.Fatal("a build without a channel must not even ask GitHub")
	}
	// Stable: v0.0.2 is the latest, nothing to report.
	Channel = Stable
	if got := Available(context.Background()); got != "" {
		t.Fatalf("stable: got %q", got)
	}
	// The same cache file must not answer for the edge channel.
	Channel = Edge
	if got := Available(context.Background()); got != "v0.0.3-dev.14+abc1234" {
		t.Fatalf("edge: got %q", got)
	}
	// A cache from before channels existed counts as stable.
	data, _ := json.Marshal(map[string]any{"checked": time.Now(), "latest": "v9.0.0"})
	os.WriteFile(checkFile(), data, 0o644)
	Channel = Stable
	if got := Available(context.Background()); got != "v9.0.0" {
		t.Fatalf("old cache: got %q", got)
	}
}

func TestDescribe(t *testing.T) {
	oldV, oldC := Version, Channel
	defer func() { Version, Channel = oldV, oldC }()
	suffix := ""
	if Variant == "slim" {
		suffix = " (slim)"
	}
	Version, Channel = "v0.0.3-dev.14+abc1234", Edge
	if got := Describe(); got != "v0.0.3-dev.14+abc1234"+suffix+" (edge)" {
		t.Fatal(got)
	}
	Version, Channel = "v0.0.2", Stable
	if got := Describe(); got != "v0.0.2"+suffix+" (stable)" {
		t.Fatal(got)
	}
	Version, Channel = "v0.0.2", ""
	if got := Describe(); got != "v0.0.2"+suffix {
		t.Fatal(got)
	}
}

func TestFetchRetriesServerErrors(t *testing.T) {
	defer func(n int, w time.Duration) { fetchTries, fetchWait = n, w }(fetchTries, fetchWait)
	fetchWait = time.Millisecond
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	b, err := fetch(context.Background(), srv.URL, 10)
	if err != nil || string(b) != "ok" || calls != 3 {
		t.Fatalf("got %q, %v after %d calls", b, err, calls)
	}
	// A 404 is not retried.
	calls = 0
	nf := httptest.NewServer(http.NotFoundHandler())
	defer nf.Close()
	if _, err := fetch(context.Background(), nf.URL, 10); err == nil {
		t.Fatal("404 succeeded")
	}
}
