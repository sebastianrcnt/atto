package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func codexJWT(account string) string {
	p, _ := json.Marshal(map[string]any{codexJWTClaimPath: map[string]any{"chatgpt_account_id": account}})
	return "h." + base64.RawURLEncoding.EncodeToString(p) + ".s"
}

func fakeCodexAuth(t *testing.T) (*httptest.Server, *[]url.Values) {
	var mu sync.Mutex
	var forms []url.Values
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_ = r.ParseForm()
			mu.Lock()
			forms = append(forms, r.PostForm)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"access_token": codexJWT("acct-1"), "refresh_token": "ref-" + r.PostForm.Get("grant_type"), "expires_in": 3600})
		case "/usercode":
			json.NewEncoder(w).Encode(map[string]any{"device_auth_id": "dev-1", "user_code": "ABCD-1234", "interval": "0"})
		case "/devtoken":
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			if n == 1 {
				w.WriteHeader(http.StatusForbidden) // still pending
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"authorization_code": "dev-code", "code_verifier": "dev-verifier"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &forms
}

func testCodex(srv *httptest.Server, listen string) *Codex {
	c := NewCodex()
	c.AuthorizeURL = "http://auth.invalid/oauth/authorize"
	c.TokenURL = srv.URL + "/oauth/token"
	c.DeviceUserCodeURL = srv.URL + "/usercode"
	c.DeviceTokenURL = srv.URL + "/devtoken"
	c.ListenAddr = listen
	c.Now = func() time.Time { return t0 }
	return c
}

func TestCodexLoginViaPaste(t *testing.T) {
	srv, forms := fakeCodexAuth(t)
	// Hold a port so the callback server cannot bind; the paste is used.
	hold := httptest.NewServer(http.NotFoundHandler())
	defer hold.Close()
	c := testCodex(srv, strings.TrimPrefix(hold.URL, "http://"))
	urls := make(chan string, 1)
	pasted := make(chan string, 3)
	go func() {
		u, _ := url.Parse(<-urls)
		q := u.Query()
		if q.Get("client_id") != codexClientID || q.Get("redirect_uri") != codexRedirectURI || q.Get("codex_cli_simplified_flow") != "true" {
			t.Errorf("authorize url %s", u)
		}
		pasted <- codexRedirectURI + "?code=c1&state=wrong"
		pasted <- codexRedirectURI + "?code=c1&state=" + q.Get("state")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cred, err := c.Login(ctx, UI{ShowURL: func(u string) { urls <- u }, ReadPasted: func() (string, error) { return <-pasted, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if cred.Type != "oauth" || cred.AccountID != "acct-1" || cred.Refresh != "ref-authorization_code" || cred.Expires != t0.Add(time.Hour).UnixMilli() {
		t.Fatalf("cred %+v", cred)
	}
	if f := (*forms)[0]; f.Get("code") != "c1" || f.Get("code_verifier") == "" || f.Get("redirect_uri") != codexRedirectURI {
		t.Fatalf("form %v", f)
	}
	fresh, err := c.Refresh(ctx, cred)
	if err != nil || fresh.Refresh != "ref-refresh_token" || (*forms)[1].Get("refresh_token") != "ref-authorization_code" {
		t.Fatalf("refresh %+v %v", fresh, err)
	}
}

func TestCodexLoginViaCallback(t *testing.T) {
	srv, _ := fakeCodexAuth(t)
	c := testCodex(srv, "127.0.0.1:0")
	// The redirect URI is fixed, so Login does not report the port it
	// bound; pick a free one up front.
	ln := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(ln.URL, "http://")
	ln.Close()
	c.ListenAddr = addr
	urls := make(chan string, 1)
	go func() {
		u, _ := url.Parse(<-urls)
		state := u.Query().Get("state")
		for range 50 {
			resp, err := http.Get("http://" + addr + "/auth/callback?code=cb&state=" + state)
			if err == nil {
				resp.Body.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cred, err := c.Login(ctx, UI{ShowURL: func(u string) { urls <- u }})
	if err != nil || cred.AccountID != "acct-1" {
		t.Fatalf("%+v %v", cred, err)
	}
}

func TestCodexDeviceCode(t *testing.T) {
	srv, forms := fakeCodexAuth(t)
	c := testCodex(srv, "127.0.0.1:0")
	var code string
	cred, err := c.LoginDeviceCode(context.Background(), func(user, verify string) { code = user + " " + verify })
	if err != nil || cred.AccountID != "acct-1" || code != "ABCD-1234 "+CodexDeviceVerifyURL {
		t.Fatalf("%+v %v %q", cred, err, code)
	}
	if f := (*forms)[0]; f.Get("code") != "dev-code" || f.Get("code_verifier") != "dev-verifier" || f.Get("redirect_uri") != codexDeviceRedirectURI {
		t.Fatalf("form %v", f)
	}
}

func TestParseAuthorizationInput(t *testing.T) {
	for in, want := range map[string][2]string{
		"http://localhost:1455/auth/callback?code=a&state=b": {"a", "b"},
		"a#b":            {"a", "b"},
		"code=a&state=b": {"a", "b"},
		"just-the-code":  {"just-the-code", ""},
		"   ":            {"", ""},
	} {
		c, s := ParseAuthorizationInput(in)
		if c != want[0] || s != want[1] {
			t.Errorf("%q: %q %q", in, c, s)
		}
	}
	if AccountID(codexJWT("x")) != "x" || AccountID("bad") != "" {
		t.Fatal("account id")
	}
}

func TestCredentialJSONPiShape(t *testing.T) {
	var c Credential
	if err := json.Unmarshal([]byte(`{"type":"oauth","access":"a","refresh":"r","expires":1.5e12,"clientId":"c","other":[1]}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Expires != 1500000000000 || c.ClientID != "c" || string(c.Extra["other"]) != "[1]" {
		t.Fatalf("%+v", c)
	}
	b, _ := json.Marshal(c)
	if string(b) != `{"type":"oauth","access":"a","refresh":"r","expires":1500000000000,"clientId":"c","other":[1]}` {
		t.Fatalf("marshal %s", b)
	}
}

func TestCodexLoginClosedPasteWithBusyCallback(t *testing.T) {
	srv, _ := fakeCodexAuth(t)
	hold := httptest.NewServer(http.NotFoundHandler())
	defer hold.Close()
	c := testCodex(srv, strings.TrimPrefix(hold.URL, "http://"))
	for _, readErr := range []error{io.EOF, errors.New("input closed")} {
		t.Run(readErr.Error(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := c.Login(ctx, UI{ReadPasted: func() (string, error) { return "", readErr }})
			if err == nil || !strings.HasPrefix(err.Error(), "no redirect received:") || !errors.Is(err, readErr) {
				t.Fatalf("closed paste did not fail promptly: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatal("login waited for cancellation")
			}
		})
	}
}

func TestCodexLoginCallbackAfterPasteEOF(t *testing.T) {
	srv, _ := fakeCodexAuth(t)
	ln := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(ln.URL, "http://")
	ln.Close()
	c := testCodex(srv, addr)
	urls := make(chan string, 1)
	closed := make(chan struct{})
	go func() {
		u, _ := url.Parse(<-urls)
		<-closed
		resp, err := http.Get("http://" + addr + "/auth/callback?code=cb&state=" + u.Query().Get("state"))
		if err != nil {
			t.Error(err)
			return
		}
		resp.Body.Close()
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cred, err := c.Login(ctx, UI{
		ShowURL:    func(u string) { urls <- u },
		ReadPasted: func() (string, error) { close(closed); return "", io.EOF },
	})
	if err != nil || cred.AccountID != "acct-1" {
		t.Fatalf("callback must still work after paste EOF: %+v %v", cred, err)
	}
}
