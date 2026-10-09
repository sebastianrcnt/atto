package server

import (
	"context"
	"errors"
	"strings"

	"github.com/sebastianrcnt/atto/auth"
	"github.com/sebastianrcnt/atto/config"
)

// Login belongs to the runtime, not a desktop's process. Disconnect leaves
// the question available to another client; cancel or session close ends it.
type loginRun struct {
	provider string
	cancel   context.CancelFunc
	pasted   chan string
	promptID string
	url      string
	note     string
}

var loginProvider = auth.GetOAuthProvider

func (t *thread) authList() any {
	providers := []map[string]any{}
	for _, p := range config.LoginProviders(t.models) {
		providers = append(providers, map[string]any{"id": p.ID, "name": p.Name, "oauth": p.OAuth, "status": p.Status})
	}
	out := map[string]any{"providers": providers, "stored": config.StoredCredentials()}
	if t.login != nil {
		out["login"] = map[string]any{"provider": t.login.provider, "status": "pending", "url": t.login.url, "note": t.login.note}
	}
	return out
}

func (t *thread) authLogin(p threadParams) (any, error) {
	if t.readOnly != "" {
		return nil, failure(ReasonReadOnly, "%s", t.readOnly)
	}
	if t.turns.Busy || t.login != nil || t.prompt != nil {
		return nil, failure(ReasonBusy, "finish the turn or pending question before logging in")
	}
	known := false
	for _, provider := range config.LoginProviders(t.models) {
		known = known || provider.ID == p.Provider && provider.OAuth == p.OAuth
	}
	if !known {
		return nil, invalid("unknown provider or authentication method %q", p.Provider)
	}
	if !p.OAuth {
		if strings.TrimSpace(p.APIKey) == "" {
			return nil, invalid("apiKey is required")
		}
		if err := config.SetAPIKey(p.Provider, strings.TrimSpace(p.APIKey)); err != nil {
			return nil, err
		}
		err := t.reloadModels()
		t.publish("auth/updated", map[string]any{"provider": p.Provider, "status": "completed"})
		return map[string]any{"status": "completed"}, err
	}
	op := loginProvider(p.Provider)
	if op == nil {
		return nil, invalid("no OAuth flow for %q", p.Provider)
	}
	deviceID, err := config.DeviceID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &loginRun{provider: p.Provider, cancel: cancel, pasted: make(chan string, 1)}
	t.login = run
	t.publish("auth/updated", map[string]any{"provider": p.Provider, "status": "pending"})
	ui := auth.UI{
		ShowURL: func(url string) {
			t.do(func() {
				if t.login == run {
					run.url = url
					t.publish("auth/updated", map[string]any{"provider": run.provider, "status": "pending", "url": url})
				}
			})
		},
		Notice: func(message string) {
			t.do(func() {
				if t.login == run {
					run.note = message
					t.publish("auth/updated", map[string]any{"provider": run.provider, "status": "pending", "note": message})
				}
			})
		},
		ReadPasted: func() (string, error) {
			t.do(func() {
				if t.login != run || ctx.Err() != nil {
					return
				}
				q := &openPrompt{origin: "auth", wire: Prompt{Kind: PromptInput, Title: "Sign in to " + run.provider, Subtitle: run.url,
					Placeholder: "Paste the final browser redirect URL (optional if callback succeeds)"},
					submit: func(text string) { run.pasted <- text }, cancel: cancel}
				t.ask(q)
				run.promptID = q.wire.ID
			})
			select {
			case text := <-run.pasted:
				return text, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	}
	go func() {
		credential, err := op.Login(ctx, ui, deviceID)
		t.do(func() {
			defer cancel()
			if t.login != run || t.closing {
				return
			}
			t.login = nil
			if t.prompt != nil && t.prompt.wire.ID == run.promptID {
				t.closePrompt("withdrawn", "")
			}
			status := "completed"
			if ctx.Err() != nil {
				err = ctx.Err()
			} else if err == nil {
				err = config.SetOAuth(run.provider, credential)
				if err == nil {
					err = t.reloadModels()
				}
			}
			params := map[string]any{"provider": run.provider, "status": status}
			if err != nil {
				params["status"] = "failed"
				if errors.Is(err, context.Canceled) {
					params["status"] = "cancelled"
				} else {
					params["error"] = err.Error()
				}
			}
			t.publish("auth/updated", params)
			t.afterPrompt()
		})
	}()
	return map[string]any{"status": "pending"}, nil
}
