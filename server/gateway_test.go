package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

func TestWebClientServedFromEmbed(t *testing.T) {
	s := New("test", t.TempDir())
	defer s.Close()
	h := httptest.NewServer(s.HTTPHandler("tok-tok-tok-tok-tok"))
	defer h.Close()
	get := func(path string) (string, *http.Response) {
		resp, err := http.Get(h.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), resp
	}
	page, resp := get("/")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, asset := range []struct{ ref, typ string }{{`src="app.js?v=`, "javascript"}, {`href="app.css?v=`, "text/css"}} {
		i := strings.Index(page, asset.ref)
		if i < 0 {
			t.Fatalf("index.html does not reference %s:\n%s", asset.ref, page)
		}
		url := page[i+strings.Index(asset.ref, `"`)+1:]
		url = url[:strings.Index(url, `"`)]
		body, resp := get("/" + url)
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), asset.typ) || len(body) < 1000 {
			t.Fatalf("%s: %d %s, %d bytes", url, resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
		}
	}
	if _, resp := get("/nope.js"); resp.StatusCode != 404 {
		t.Fatalf("missing file: %d", resp.StatusCode)
	}
}

// remote/start without a port listens on settings.json's remote.port, else
// 7879 (the port /remote used in the terminal).
func TestRemotePortDefault(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if got := remotePort(); got != DefaultRemotePort {
		t.Fatalf("no setting: %d", got)
	}
	if err := config.UpdateSettings(map[string]any{"remote": map[string]any{"port": 7999}}); err != nil {
		t.Fatal(err)
	}
	if got := remotePort(); got != 7999 {
		t.Fatalf("remote.port: %d", got)
	}
}
