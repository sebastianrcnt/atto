package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/sebastianrcnt/atto/auth"
	"github.com/sebastianrcnt/atto/fsutil"
)

// AuthEntry is one provider's credentials in auth.json, in pi's format, so
// ~/.pi/agent/auth.json can be copied to ~/.atto/auth.json as is:
//
//	{"<provider>": {"type": "api_key", "key": "…", "env": {…}}}
//	{"<provider>": {"type": "oauth", "access": "…", "refresh": "…", "expires": <unix ms>, …}}
//
// An api_key "key" is a config value as in pi: a literal, "$VAR" or
// "${VAR}" (from the environment or the entry's "env") or "!command".
type AuthEntry = auth.Credential

// LoadAuth reads auth.json; a missing file yields no entries. Entries are
// checked as pi's ReadOnlyAuthStorage does, but where pi rejects the whole
// file, atto skips the entry it cannot use (a newer pi may write types
// atto does not know); rewrites keep such entries.
func LoadAuth() (map[string]AuthEntry, error) {
	out := map[string]AuthEntry{}
	data, err := os.ReadFile(AuthPath())
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(data)) == 0 {
		return out, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid auth.json: %w", err)
	}
	for id, r := range raw {
		var e AuthEntry
		if json.Unmarshal(r, &e) != nil || (e.Type != "api_key" && e.Type != "oauth") || (e.Type == "oauth" && (e.Access == "" || e.Refresh == "")) {
			continue
		}
		out[id] = e
	}
	return out, nil
}

// ResolvedKey is the entry's bearer token with config values resolved.
func ResolvedKey(e AuthEntry) string {
	if e.Type == "oauth" {
		return e.Access
	}
	if e.Key == "" {
		return ""
	}
	v, _ := ResolveConfigValue(e.Key, e.Env)
	return v
}

// updateAuth rewrites auth.json (mode 0600) after fn edits its raw entries,
// so entries this version does not understand survive.
func updateAuth(fn func(raw map[string]json.RawMessage) error) error {
	return fsutil.WithFileLock(AuthPath(), func() error {
		return updateAuthLocked(fn)
	})
}

func updateAuthLocked(fn func(raw map[string]json.RawMessage) error) error {
	raw := map[string]json.RawMessage{}
	if data, err := os.ReadFile(AuthPath()); err == nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &raw); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := fn(raw); err != nil {
		return err
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := fsutil.PrivateDirs(Dir(), Dir()); err != nil {
		return err
	}
	// Atomic so a crash never leaves a truncated credentials file.
	return fsutil.WriteAtomic(AuthPath(), append(out, '\n'), 0o600)
}

func setEntry(provider string, e AuthEntry) error {
	return updateAuth(func(raw map[string]json.RawMessage) error {
		b, err := json.Marshal(e)
		raw[provider] = b
		return err
	})
}

// SetAPIKey stores an API key for provider in auth.json (mode 0600),
// preserving other entries.
func SetAPIKey(provider, key string) error {
	return setEntry(provider, AuthEntry{Type: "api_key", Key: key})
}

// SetOAuth stores a login credential for provider, preserving other entries.
func SetOAuth(provider string, c auth.Credential) error {
	c.Type = "oauth"
	return setEntry(provider, c)
}

// RemoveAuth deletes provider's entry; it reports whether one existed.
func RemoveAuth(provider string) (bool, error) {
	found := false
	err := updateAuth(func(raw map[string]json.RawMessage) error {
		_, found = raw[provider]
		delete(raw, provider)
		return nil
	})
	return found, err
}

// DeviceID returns this installation's stable UUID, creating it on first use.
func DeviceID() (string, error) {
	path := filepath.Join(Dir(), "device-id")
	if b, err := os.ReadFile(path); err == nil && len(b) >= 36 {
		return string(b[:36]), nil
	}
	id := auth.NewDeviceID()
	if err := fsutil.PrivateDirs(Dir(), Dir()); err != nil {
		return "", err
	}
	return id, os.WriteFile(path, []byte(id+"\n"), 0o600)
}

// OAuthToken returns a valid access token for provider, refreshing and
// persisting the credential when it is about to expire.
func OAuthToken(ctx context.Context, provider string) (string, error) {
	var token string
	err := fsutil.WithFileLock(AuthPath(), func() error {
		var err error
		token, err = oauthTokenLocked(ctx, provider)
		return err
	})
	return token, err
}

func oauthTokenLocked(ctx context.Context, provider string) (string, error) {
	entries, err := LoadAuth()
	if err != nil {
		return "", err
	}
	e, ok := entries[provider]
	if !ok || e.Type != "oauth" {
		return "", errors.New("not logged in; use /login or run: atto login " + provider)
	}
	if !e.Expired(time.Now()) {
		return e.Access, nil
	}
	p := auth.GetOAuthProvider(provider)
	if p == nil {
		return "", fmt.Errorf("the %s login expired and atto cannot refresh it; log in again", provider)
	}
	fresh, err := p.Refresh(ctx, e)
	if err != nil {
		return "", err
	}
	// Keep provider fields the refresh does not return.
	if fresh.ClientID == "" {
		fresh.ClientID = e.ClientID
	}
	if fresh.Extra == nil {
		fresh.Extra = e.Extra
	}
	fresh.Type = "oauth"
	if err := updateAuthLocked(func(raw map[string]json.RawMessage) error {
		b, err := json.Marshal(fresh)
		raw[provider] = b
		return err
	}); err != nil {
		return "", err
	}
	return fresh.Access, nil
}
