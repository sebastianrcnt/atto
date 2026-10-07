// Package update installs new atto releases from GitHub: `atto update`,
// and a once-a-day check that tells the user when a release is out. atto
// never updates itself without being asked.
//
// Releases carry one raw binary per platform (atto_<os>_<arch>[.exe]) and
// a checksums.txt; install.sh and install.ps1 use the same layout.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// Install is the reinstall command for this system.
var Install = installSh

func init() {
	if runtime.GOOS == "windows" {
		Install = installPs1
	}
}

const (
	installSh  = "curl -fsSL https://raw.githubusercontent.com/" + Repo + "/main/install.sh | sh"
	installPs1 = "irm https://raw.githubusercontent.com/" + Repo + "/main/install.ps1 | iex"
)

const (
	Repo = "sebastianrcnt/atto"

	Stable = "stable"
	// Edge is the channel of builds from every push to main. Its release
	// tag carries the same name; that tag is rolling, so its version lives
	// in the release name.
	Edge    = "edge"
	EdgeTag = Edge
)

// The GitHub endpoints are variables so tests can point them at a local
// server.
var (
	apiBase = "https://api.github.com/repos/" + Repo + "/releases/"
	dlURL   = "https://github.com/" + Repo + "/releases/download/"
)

// Version is set at build time (-ldflags "-X .../update.Version=v0.1.0").
// Builds without it report the module version (go install ...@v0.1.0) or
// "dev".
var Version = ""

// Channel is the release channel this binary was built for, "stable" or
// "edge" (-ldflags "-X .../update.Channel=edge"). Builds without it (go
// install, local builds) have none: they report "dev" and are never told
// about updates.
var Channel = ""

// Describe is the version with its channel, as `atto -version` prints it.
func Describe() string {
	if Channel == "" {
		return Current()
	}
	return Current() + " (" + Channel + ")"
}

func Current() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// Asset is this platform's binary name in a release.
func Asset() string {
	name := "atto_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

var client = &http.Client{Timeout: 60 * time.Second}

// Release is a downloadable release. Tag names where its assets live;
// Version is what `atto -version` reports. They differ on the edge channel,
// whose tag never changes but whose version does.
type Release struct {
	Tag     string
	Version string
}

// Latest returns the newest release on a channel.
func Latest(ctx context.Context, channel string) (Release, error) {
	if channel == Edge {
		var r struct {
			Name string `json:"name"`
		}
		if err := getJSON(ctx, apiBase+"tags/"+EdgeTag, &r); err != nil {
			return Release{}, err
		}
		// The workflow names the release after its version; tolerate a
		// prefix such as "atto v0.0.3-dev.1+abc1234".
		f := strings.Fields(r.Name)
		if len(f) == 0 {
			return Release{}, fmt.Errorf("the edge release has no version in its name")
		}
		v := f[len(f)-1]
		if _, ok := parse(v); !ok {
			return Release{}, fmt.Errorf("the edge release name %q is not a version", r.Name)
		}
		return Release{Tag: EdgeTag, Version: v}, nil
	}
	var r struct {
		Tag string `json:"tag_name"`
	}
	if err := getJSON(ctx, apiBase+"latest", &r); err != nil {
		return Release{}, err
	}
	if r.Tag == "" {
		return Release{}, fmt.Errorf("no release found")
	}
	return Release{Tag: r.Tag, Version: r.Tag}, nil
}

func getJSON(ctx context.Context, url string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("checking for releases: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// Newer reports whether version a is newer than b, by semver precedence:
// v0.0.2 < v0.0.3-dev.1 < v0.0.3-dev.14 < v0.0.3. Build metadata (+sha) is
// ignored. Anything unparsable, like "dev", is older than every release.
func Newer(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	switch {
	case !oka:
		return false
	case !okb:
		return true
	}
	return pa.cmp(pb) > 0
}

type semver struct {
	core [3]int
	pre  []string // pre-release identifiers; empty for a release
}

// cmp orders by semver 2.0 precedence.
func (a semver) cmp(b semver) int {
	for i := range a.core {
		if a.core[i] != b.core[i] {
			return sign(a.core[i] - b.core[i])
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1 // a release outranks its pre-releases
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		nx, errx := strconv.Atoi(x)
		ny, erry := strconv.Atoi(y)
		switch {
		case errx == nil && erry == nil:
			if nx != ny {
				return sign(nx - ny)
			}
		case errx == nil:
			return -1 // numeric identifiers sort before alphanumeric ones
		case erry == nil:
			return 1
		case x != y:
			return sign(strings.Compare(x, y))
		}
	}
	return sign(len(a.pre) - len(b.pre))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// parse reads v1.2.3, v1.2.3-dev.4 and v1.2.3+sha. Go pseudo-versions
// (v0.0.3-0.20261005-abcdef) come out as pre-releases of v0.0.3, which is
// what they are: commits made before that release.
func parse(v string) (semver, bool) {
	var out semver
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+")
	core, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return out, false
		}
		out.pre = strings.Split(pre, ".")
	}
	return out, true
}

// Plan decides whether to install rel over cur. Normally only a newer
// version qualifies (and a dev build takes anything). switching is `atto
// channel` moving to another channel: that installs whatever the channel
// has, even a lower version (edge to stable); downgrade reports that case.
func Plan(rel Release, cur string, switching bool) (install, downgrade bool) {
	if Newer(rel.Version, cur) || cur == "dev" {
		return true, false
	}
	if switching && rel.Version != cur {
		return true, true
	}
	return false, false
}

// SetEndpoints points the GitHub API and download URLs somewhere else
// (api ends in /releases/, dl in /releases/download/) and returns a func
// that restores them. For tests.
func SetEndpoints(api, dl string) (restore func()) {
	a, d := apiBase, dlURL
	apiBase, dlURL = api, dl
	return func() { apiBase, dlURL = a, d }
}

// Managed returns how to update a binary another tool installed ("" when
// atto may replace it itself).
func Managed(exe string) string {
	p := filepath.ToSlash(exe)
	switch {
	case strings.Contains(p, "/Cellar/") || strings.Contains(p, "/homebrew/"):
		return "brew upgrade atto"
	case strings.Contains(p, "/go/bin/") || os.Getenv("GOBIN") != "" && strings.HasPrefix(exe, os.Getenv("GOBIN")):
		return "go install github.com/" + Repo + "/cmd/atto@latest"
	}
	return ""
}

// InstallRelease downloads tag's binary, checks it against the release's
// checksums.txt and replaces exe with it.
func InstallRelease(ctx context.Context, tag, exe string) error {
	sums, err := fetch(ctx, dlURL+tag+"/checksums.txt", 1<<20)
	if err != nil {
		return err
	}
	want, err := checksum(string(sums), Asset())
	if err != nil {
		return err
	}
	bin, err := fetch(ctx, dlURL+tag+"/"+Asset(), 256<<20)
	if err != nil {
		return err
	}
	if got := sha256.Sum256(bin); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s: refusing to install", Asset())
	}
	return replace(exe, bin)
}

// fetchTries is how many times fetch tries a download: GitHub's release
// downloads now and then answer 500 or 502 for a moment.
var fetchTries, fetchWait = 3, 2 * time.Second

// fetch downloads url, retrying network errors and 5xx answers.
func fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	var err error
	for try := 1; ; try++ {
		var b []byte
		var retry bool
		if b, retry, err = fetchOnce(ctx, url, limit); err == nil || !retry || try >= fetchTries {
			return b, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(fetchWait):
		}
	}
}

func fetchOnce(ctx context.Context, url string, limit int64) (b []byte, retry bool, err error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode >= 500, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err = io.ReadAll(io.LimitReader(resp.Body, limit))
	return b, err != nil, err
}

// checksum finds name in a sha256sum-style list.
func checksum(sums, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("the release has no %s", name)
}

// replace swaps exe for data. The new file is written next to exe and
// renamed over it, so a failure never leaves a half-written binary. Windows
// can't overwrite a running executable but can rename it, so the old one
// moves to exe.old first (removed on the next start, see Cleanup).
func replace(exe string, data []byte) error {
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// A running exe can be renamed but not overwritten. exe.old may
		// itself still be running (an atto started before the last update),
		// and then it can't be replaced either: move exe aside under a new
		// name instead. Cleanup removes them once nothing runs them.
		old := exe + ".old"
		if os.Remove(old) != nil && fileExists(old) {
			old = fmt.Sprintf("%s.old-%d", exe, time.Now().UnixNano())
		}
		if err := os.Rename(exe, old); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Cleanup removes the binaries Windows updates left behind; those still
// running stay until a later start.
func Cleanup() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
		olds, _ := filepath.Glob(exe + ".old-*")
		for _, o := range olds {
			_ = os.Remove(o)
		}
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
