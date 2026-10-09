package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/update"
)

func fakeReleases(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.0.2"}`)
	})
	mux.HandleFunc("/api/tags/edge", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"name":"v0.0.3-dev.14+abc1234"}`)
	})
	for tag, body := range map[string]string{"edge": "edge-binary", "v0.0.2": "stable-binary"} {
		sum := sha256.Sum256([]byte(body))
		mux.HandleFunc("/dl/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), update.Asset())
			other := "slim"
			if update.Variant == "slim" {
				other = "full"
			}
			otherSum := sha256.Sum256([]byte(body + "-" + other))
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(otherSum[:]), update.AssetFor(other))
		})
		mux.HandleFunc("/dl/"+tag+"/"+update.Asset(), func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		})
		other := "slim"
		if update.Variant == "slim" {
			other = "full"
		}
		mux.HandleFunc("/dl/"+tag+"/"+update.AssetFor(other), func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body+"-"+other) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(update.SetEndpoints(srv.URL+"/api/", srv.URL+"/dl/"))
}

func setBuild(t *testing.T, version, channel string) {
	t.Helper()
	oldV, oldC := update.Version, update.Channel
	t.Cleanup(func() { update.Version, update.Channel = oldV, oldC })
	update.Version, update.Channel = version, channel
}

func newExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "atto")
	os.WriteFile(exe, []byte("old"), 0o755)
	return exe
}

func TestChannelSwitch(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("GOBIN", "")
	fakeReleases(t)
	exe := newExe(t)

	// stable -> edge installs the edge binary.
	setBuild(t, "v0.0.2", "stable")
	var out bytes.Buffer
	if err := runChannel([]string{"edge"}, &out, "v0.0.2", exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "edge-binary" {
		t.Fatalf("exe = %q\n%s", b, out.String())
	}
	if !strings.Contains(out.String(), "Switched to edge: atto v0.0.2 → v0.0.3-dev.14+abc1234") {
		t.Fatal(out.String())
	}
	// Nothing is saved: the channel travels with the binary.
	if _, err := os.Stat(config.SettingsPath()); !os.IsNotExist(err) {
		t.Fatal("atto channel must not write settings")
	}

	// edge -> stable is a downgrade and says so.
	setBuild(t, "v0.0.3-dev.14+abc1234", "edge")
	out.Reset()
	if err := runChannel([]string{"stable"}, &out, "v0.0.3-dev.14+abc1234", exe); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Switched to stable: atto v0.0.3-dev.14+abc1234 → v0.0.2") {
		t.Fatal(out.String())
	}
	if b, _ := os.ReadFile(exe); string(b) != "stable-binary" {
		t.Fatalf("exe = %q", b)
	}

	// Naming the channel you're already on is just an update: no downgrade.
	setBuild(t, "v0.0.3-dev.20+abc1234", "edge")
	os.WriteFile(exe, []byte("old"), 0o755)
	out.Reset()
	if err := runChannel([]string{"edge"}, &out, "v0.0.3-dev.20+abc1234", exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" || !strings.Contains(out.String(), "already the latest edge") {
		t.Fatalf("%q %s", b, out.String())
	}
}

func TestChannelShowAndValidate(t *testing.T) {
	var out bytes.Buffer
	setBuild(t, "v0.0.3-dev.14+abc1234", "edge")
	if err := runChannel(nil, &out, update.Current(), "x"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "atto "+update.Describe()+"\n" {
		t.Fatalf("got %q", got)
	}
	if err := runChannel([]string{"nightly"}, &out, "v0.0.2", "x"); err == nil {
		t.Fatal("unknown channel must fail")
	}
	if err := runChannel([]string{"edge", "stable"}, &out, "v0.0.2", "x"); err == nil {
		t.Fatal("two channels must fail")
	}
}

func TestChannelRefusesManagedBinaries(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("GOBIN", "")
	fakeReleases(t)
	setBuild(t, "v0.0.2", "stable")
	err := runChannel([]string{"edge"}, &bytes.Buffer{}, "v0.0.2", "/opt/homebrew/Cellar/atto/0.0.2/bin/atto")
	if err == nil || !strings.Contains(err.Error(), "brew upgrade atto") {
		t.Fatal(err)
	}
}

func TestUpdateStaysOnBinaryChannel(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("GOBIN", "")
	fakeReleases(t)
	exe := newExe(t)

	// An edge binary is not pulled down to stable...
	setBuild(t, "v0.0.3-dev.20+abc1234", "edge")
	var out bytes.Buffer
	if err := runUpdate(nil, &out, "v0.0.3-dev.20+abc1234", exe); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(latest)") {
		t.Fatal(out.String())
	}
	// ...but follows edge forward.
	out.Reset()
	if err := runUpdate(nil, &out, "v0.0.3-dev.13+abc1234", exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "edge-binary" {
		t.Fatalf("exe = %q\n%s", b, out.String())
	}
	// The -channel flag is gone.
	if err := runUpdate([]string{"-channel", "edge"}, &out, "v0.0.2", exe); err == nil {
		t.Fatal("update takes no -channel")
	}
}

func TestUpdateCheckDoesNotInstall(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	fakeReleases(t)
	setBuild(t, "v0.0.1", "stable")
	exe := newExe(t)
	var out bytes.Buffer
	if err := runUpdate([]string{"-check"}, &out, "v0.0.1", exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" || !strings.Contains(out.String(), "v0.0.2 is available") {
		t.Fatalf("%q %s", b, out.String())
	}
}

func TestUpdateWording(t *testing.T) {
	if got := latestLine("v0.0.2"); got != "atto v0.0.2 (latest)" {
		t.Error(got)
	}
	if got := availableLine("v0.0.1", "v0.0.2"); got != "atto v0.0.1 · v0.0.2 is available (atto update)" {
		t.Error(got)
	}
	if got := updatedLine("v0.0.1", "v0.0.2"); got != "Updated atto v0.0.1 → v0.0.2" {
		t.Error(got)
	}
	if got := availableLine("dev", "v0.0.2"); got != "atto dev · v0.0.2 is available (atto update)" {
		t.Error(got)
	}
}

func TestUpdateSwitchesVariantExplicitlyAtSameVersion(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("GOBIN", "")
	fakeReleases(t)
	setBuild(t, "v0.0.2", "stable")
	exe := newExe(t)
	other := "slim"
	if update.Variant == "slim" {
		other = "full"
	}
	var out bytes.Buffer
	if err := runUpdate(nil, &out, "v0.0.2", exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("implicit variant change")
	}
	if err := runUpdate([]string{"-variant", other, "-check"}, &out, "v0.0.2", exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("check installed")
	}
	if err := runUpdate([]string{"-variant", other}, &out, "v0.0.2", exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "stable-binary-"+other {
		t.Fatalf("%q", b)
	}
	if !strings.Contains(out.String(), "Switched variant: "+update.Variant+" → "+other) {
		t.Fatal(out.String())
	}
	if err := runUpdate([]string{"-variant", "unknown"}, &out, "v0.0.2", exe); err == nil {
		t.Fatal("unknown variant accepted")
	}
}
