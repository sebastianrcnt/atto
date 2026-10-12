package auth

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import "context"

// OAuthProvider is one login method (pi: OAuthAuth, keyed by provider id
// as in auth/oauth/load.ts).
type OAuthProvider struct {
	ID   string // provider id in auth.json and models.json
	Name string // shown in login pickers
	// Subscription marks a login that draws usage from a flat-rate plan
	// (both ChatGPT logins): models used through it are priced as an
	// estimate, not at API rates.
	Subscription bool
	// Login runs the browser flow; deviceID is this installation's UUID.
	Login   func(ctx context.Context, ui UI, deviceID string) (Credential, error)
	Refresh func(ctx context.Context, c Credential) (Credential, error)
}

// NewChatGPTClient and NewCodexClient build the OAuth clients; tests
// replace them to point at fake servers.
var (
	NewChatGPTClient = func(deviceID string) *ChatGPT { return New(deviceID) }
	NewCodexClient   = NewCodex
)

// OAuthProviders lists the logins atto supports, in picker order.
func OAuthProviders() []OAuthProvider {
	return []OAuthProvider{
		{
			ID: "openai", Name: "OpenAI (ChatGPT subscription)", Subscription: true,
			Login: func(ctx context.Context, ui UI, deviceID string) (Credential, error) {
				return NewChatGPTClient(deviceID).Login(ctx, ui)
			},
			Refresh: func(ctx context.Context, c Credential) (Credential, error) {
				return NewChatGPTClient("").Refresh(ctx, c)
			},
		},
		{
			ID: "openai-codex", Name: "OpenAI Codex (ChatGPT Plus/Pro, legacy)", Subscription: true,
			Login: func(ctx context.Context, ui UI, _ string) (Credential, error) {
				return NewCodexClient().Login(ctx, ui)
			},
			Refresh: func(ctx context.Context, c Credential) (Credential, error) {
				return NewCodexClient().Refresh(ctx, c)
			},
		},
	}
}

// GetOAuthProvider returns the login for provider id, or nil.
func GetOAuthProvider(id string) *OAuthProvider {
	for _, p := range OAuthProviders() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}
