package auth

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

// Port of pi's src/auth/oauth/openai-codex.ts and oauth/device-code.ts:
// the ChatGPT Plus/Pro login used by the Codex backend
// (openai-codex-responses). Unlike Sign in with ChatGPT it uses the Codex
// CLI's public client and a fixed localhost redirect.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	codexClientID            = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexAuthBaseURL         = "https://auth.openai.com"
	codexRedirectURI         = "http://localhost:1455/auth/callback"
	codexScope               = "openid profile email offline_access"
	codexJWTClaimPath        = "https://api.openai.com/auth"
	codexDeviceTimeout       = 15 * time.Minute
	CodexDeviceVerifyURL     = codexAuthBaseURL + "/codex/device"
	codexDeviceRedirectURI   = codexAuthBaseURL + "/deviceauth/callback"
	codexDefaultCallbackAddr = "127.0.0.1:1455"
)

// Codex holds the endpoints; tests point them at fake servers.
type Codex struct {
	AuthorizeURL      string
	TokenURL          string
	DeviceUserCodeURL string
	DeviceTokenURL    string
	// ListenAddr is the loopback address for the redirect; the redirect
	// URI itself stays http://localhost:1455/auth/callback (registered).
	ListenAddr string
	// Originator is sent on the authorize URL (pi sends "pi").
	Originator string
	HTTP       *http.Client
	Now        func() time.Time
}

// NewCodex returns the production configuration.
func NewCodex() *Codex {
	return &Codex{
		AuthorizeURL:      codexAuthBaseURL + "/oauth/authorize",
		TokenURL:          codexAuthBaseURL + "/oauth/token",
		DeviceUserCodeURL: codexAuthBaseURL + "/api/accounts/deviceauth/usercode",
		DeviceTokenURL:    codexAuthBaseURL + "/api/accounts/deviceauth/token",
		ListenAddr:        codexDefaultCallbackAddr,
		Originator:        "atto",
	}
}

func (c *Codex) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Codex) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// AuthURL builds the authorization URL.
func (c *Codex) AuthURL(state, challenge string) string {
	q := url.Values{
		"response_type":              {"code"},
		"client_id":                  {codexClientID},
		"redirect_uri":               {codexRedirectURI},
		"scope":                      {codexScope},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"state":                      {state},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {c.Originator},
	}
	return c.AuthorizeURL + "?" + q.Encode()
}

// ParseAuthorizationInput accepts a redirect URL, "code#state", a query
// string or a bare code.
func ParseAuthorizationInput(input string) (code, state string) {
	v := strings.TrimSpace(input)
	if v == "" {
		return "", ""
	}
	if u, err := url.Parse(v); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Query().Get("code"), u.Query().Get("state")
	}
	if c, s, ok := strings.Cut(v, "#"); ok {
		return c, s
	}
	if strings.Contains(v, "code=") {
		q, _ := url.ParseQuery(v)
		return q.Get("code"), q.Get("state")
	}
	return v, ""
}

// AccountID reads chatgpt_account_id from an access token's JWT claims.
func AccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	var a struct {
		ID string `json:"chatgpt_account_id"`
	}
	_ = json.Unmarshal(claims[codexJWTClaimPath], &a)
	return a.ID
}

func (c *Codex) tokenRequest(ctx context.Context, form url.Values, op string) (Credential, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Credential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Credential{}, errors.New("login cancelled")
		}
		return Credential{}, fmt.Errorf("OpenAI Codex token %s error: %w", op, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Credential{}, fmt.Errorf("OpenAI Codex token %s failed (%d): %s", op, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tr struct {
		AccessToken  string   `json:"access_token"`
		RefreshToken string   `json:"refresh_token"`
		ExpiresIn    *float64 `json:"expires_in"`
	}
	if json.Unmarshal(body, &tr) != nil || tr.AccessToken == "" || tr.RefreshToken == "" || tr.ExpiresIn == nil {
		return Credential{}, fmt.Errorf("OpenAI Codex token %s response missing fields: %s", op, strings.TrimSpace(string(body)))
	}
	id := AccountID(tr.AccessToken)
	if id == "" {
		return Credential{}, errors.New("failed to extract accountId from token")
	}
	exp := c.now().Add(time.Duration(*tr.ExpiresIn * float64(time.Second)))
	return Credential{Type: "oauth", Access: tr.AccessToken, Refresh: tr.RefreshToken, Expires: exp.UnixMilli(), AccountID: id}, nil
}

func (c *Codex) exchange(ctx context.Context, code, verifier, redirectURI string) (Credential, error) {
	return c.tokenRequest(ctx, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {codexClientID},
		"code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirectURI},
	}, "exchange")
}

// Refresh trades the refresh token for a new credential.
func (c *Codex) Refresh(ctx context.Context, old Credential) (Credential, error) {
	return c.tokenRequest(ctx, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {old.Refresh}, "client_id": {codexClientID},
	}, "refresh")
}

// Login runs the browser flow. The redirect arrives on the loopback
// listener (shared with the Codex CLI); when the port is taken, or the
// browser is elsewhere, the pasted code or redirect URL is used.
func (c *Codex) Login(ctx context.Context, ui UI) (Credential, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	verifier, challenge := PKCE()
	state := randomHex(16)
	codes := make(chan string, 1)
	errs := make(chan error, 1)

	ln, listenErr := net.Listen("tcp", c.ListenAddr)
	if listenErr == nil {
		mux := http.NewServeMux()
		var claimed atomic.Bool
		mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			switch {
			case q.Get("state") != state:
				page(w, http.StatusBadRequest, "State mismatch.")
			case claimed.Load():
				page(w, http.StatusConflict, "This sign-in has already been handled.")
			case q.Get("error") != "":
				d := q.Get("error_description")
				if d == "" {
					d = q.Get("error")
				}
				page(w, http.StatusBadRequest, "OpenAI authorization failed: "+d)
				select {
				case errs <- fmt.Errorf("OpenAI authorization failed: %s", d):
				default:
				}
			case q.Get("code") == "":
				page(w, http.StatusBadRequest, "Missing authorization code.")
			case !claimed.CompareAndSwap(false, true):
				page(w, http.StatusConflict, "This sign-in has already been handled.")
			default:
				page(w, http.StatusOK, "Signed in to OpenAI. You may now close this page.")
				select {
				case codes <- q.Get("code"):
				default:
				}
			}
		})
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go srv.Serve(ln)
		defer srv.Close()
	} else {
		ui.notice("port %s is busy (another login or the Codex CLI?); paste the redirect URL when asked", c.ListenAddr)
	}
	if ui.ReadPasted != nil {
		go func() {
			for ctx.Err() == nil {
				line, err := ui.ReadPasted()
				if err != nil {
					if listenErr != nil {
						select {
						case errs <- fmt.Errorf("no redirect received: %w", err):
						default:
						}
					}
					return
				}
				code, st := ParseAuthorizationInput(line)
				if st != "" && st != state {
					ui.notice("state mismatch; paste the redirect URL of this login")
					continue
				}
				if code == "" {
					ui.notice("missing authorization code")
					continue
				}
				select {
				case codes <- code:
				default:
				}
				return
			}
		}()
	}
	if ui.ShowURL != nil {
		ui.ShowURL(c.AuthURL(state, challenge))
	}
	select {
	case code := <-codes:
		return c.exchange(ctx, code, verifier, codexRedirectURI)
	case err := <-errs:
		return Credential{}, err
	case <-ctx.Done():
		return Credential{}, errors.New("login cancelled")
	}
}

// LoginDeviceCode runs the headless device-code flow; show receives the
// user code and the verification URL.
func (c *Codex) LoginDeviceCode(ctx context.Context, show func(userCode, verifyURL string)) (Credential, error) {
	post := func(u string, v any) (*http.Response, error) {
		b, _ := json.Marshal(v)
		req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return c.client().Do(req)
	}
	resp, err := post(c.DeviceUserCodeURL, map[string]string{"client_id": codexClientID})
	if err != nil {
		return Credential{}, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Credential{}, errors.New("OpenAI Codex device code login is not enabled for this server")
	}
	if resp.StatusCode != http.StatusOK {
		return Credential{}, fmt.Errorf("OpenAI Codex device code request failed with status %d: %s", resp.StatusCode, body)
	}
	var dev struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	_ = json.Unmarshal(body, &dev)
	interval, err := strconv.ParseFloat(strings.Trim(string(dev.Interval), `" `), 64)
	if dev.DeviceAuthID == "" || dev.UserCode == "" || err != nil || interval < 0 {
		return Credential{}, fmt.Errorf("invalid OpenAI Codex device code response: %s", body)
	}
	if show != nil {
		show(dev.UserCode, CodexDeviceVerifyURL)
	}
	wait := time.Duration(interval * float64(time.Second))
	deadline := c.now().Add(codexDeviceTimeout)
	for {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return Credential{}, errors.New("login cancelled")
		}
		if c.now().After(deadline) {
			return Credential{}, errors.New("OpenAI Codex device code expired")
		}
		resp, err := post(c.DeviceTokenURL, map[string]string{"device_auth_id": dev.DeviceAuthID, "user_code": dev.UserCode})
		if err != nil {
			if ctx.Err() != nil {
				return Credential{}, errors.New("login cancelled")
			}
			return Credential{}, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var tok struct {
				Code     string `json:"authorization_code"`
				Verifier string `json:"code_verifier"`
			}
			if json.Unmarshal(body, &tok) != nil || tok.Code == "" || tok.Verifier == "" {
				return Credential{}, fmt.Errorf("invalid OpenAI Codex device auth token response: %s", body)
			}
			return c.exchange(ctx, tok.Code, tok.Verifier, codexDeviceRedirectURI)
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			continue
		}
		var e struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		code := strings.Trim(string(e.Error), `"`)
		var nested struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(e.Error, &nested) == nil && nested.Code != "" {
			code = nested.Code
		}
		switch code {
		case "deviceauth_authorization_pending":
			continue
		case "slow_down":
			wait += 5 * time.Second
			continue
		}
		return Credential{}, fmt.Errorf("OpenAI Codex device auth failed with status %d: %s", resp.StatusCode, body)
	}
}
