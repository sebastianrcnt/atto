package app

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/sebastianrcnt/atto/auth"
	"github.com/sebastianrcnt/atto/clipboard"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/tui"
)

// /login and /logout follow pi's interactive mode
// (packages/coding-agent/src/modes/interactive/interactive-mode.ts):
// pick "Sign in with an account" or "Sign in with an API key", then a
// provider, then run its flow in a dialog in place of the editor. After a
// login the model list is reloaded; without a current model the /model
// picker opens for the new provider.

const (
	loginAccount = "Sign in with an account"
	loginAPIKey  = "Sign in with an API key"
)

// loginHooks are swapped in tests: no browser, no clipboard, fixed device.
type loginHooks struct {
	openURL  func(string) error
	copyText func(string) error
	deviceID func() (string, error)
}

var defaultLoginHooks = loginHooks{openURL: auth.OpenBrowser, copyText: clipboard.WriteText, deviceID: config.DeviceID}

func (a *App) loginHooks() loginHooks {
	if a.login.openURL == nil {
		return defaultLoginHooks
	}
	return a.login
}

func (a *App) cmdLogin(arg string) {
	if a.busy {
		a.notice("Still working — press esc to interrupt first.")
		return
	}
	providers := config.LoginProviders(a.models)
	if arg != "" {
		for _, p := range providers {
			if p.ID == arg {
				a.startLogin(p)
				return
			}
		}
		a.notice("Unknown login provider %q.", arg)
		return
	}
	p := &tui.SelectList{Title: "Select authentication method:"}
	p.Items = []tui.SelectItem{{Label: loginAccount, Value: "oauth"}, {Label: loginAPIKey, Value: "api_key"}}
	p.OnCancel = a.closeModal
	p.OnSelect = func(it tui.SelectItem) {
		a.closeModal()
		a.loginProviderPicker(it.Value == "oauth", providers)
	}
	a.openModal(p)
}

func (a *App) loginProviderPicker(oauth bool, providers []config.LoginProvider) {
	p := &tui.SelectList{Title: "Select provider to log in:", Filterable: true}
	for _, lp := range providers {
		if lp.OAuth != oauth {
			continue
		}
		detail := lp.ID
		if lp.Status != "" {
			detail += " · " + lp.Status
		}
		p.Items = append(p.Items, tui.SelectItem{Label: lp.Name, Detail: detail, Value: lp.ID, Data: lp})
	}
	p.OnCancel = func() { a.closeModal(); a.cmdLogin("") } // back, as in pi
	p.OnSelect = func(it tui.SelectItem) {
		a.closeModal()
		a.startLogin(it.Data.(config.LoginProvider))
	}
	a.openModal(p)
}

func (a *App) startLogin(p config.LoginProvider) {
	if p.OAuth {
		a.oauthLogin(p)
	} else {
		a.apiKeyLogin(p)
	}
}

func (a *App) apiKeyLogin(p config.LoginProvider) {
	d := &loginDialog{title: "Log in to " + p.Name, label: "API key: ", masked: true,
		lines: []string{"Paste the API key and press enter. It is saved to " + shortPath(config.AuthPath()) + "."}}
	d.onCancel = a.closeModal
	d.onSubmit = func(key string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		a.closeModal()
		if err := config.SetAPIKey(p.ID, key); err != nil {
			a.errorNotice(err)
			return
		}
		a.afterLogin(p, "Saved API key for "+p.Name)
	}
	a.openModal(d)
}

func (a *App) oauthLogin(p config.LoginProvider) {
	op := auth.GetOAuthProvider(p.ID)
	if op == nil {
		a.notice("No login for %s.", p.ID)
		return
	}
	hooks := a.loginHooks()
	deviceID, err := hooks.deviceID()
	if err != nil {
		a.errorNotice(err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	pasted := make(chan string, 1)
	d := &loginDialog{title: "Log in to " + p.Name, label: "Redirect URL: ",
		lines: []string{"Starting the login…"}}
	d.onCancel = func() {
		cancel()
		a.closeModal()
		a.notice("Login cancelled.")
	}
	d.onSubmit = func(s string) {
		if strings.TrimSpace(s) == "" {
			return
		}
		d.input = nil
		select {
		case pasted <- s:
		default:
		}
	}
	a.openModal(d)

	ui := auth.UI{
		ShowURL: func(url string) {
			opened := hooks.openURL(url) == nil
			copied := hooks.copyText(url) == nil
			a.ui.Do(func() {
				how := "Open this URL in your browser"
				switch {
				case opened && copied:
					how = "A browser window should open (the URL is also on the clipboard)"
				case opened:
					how = "A browser window should open"
				case copied:
					how = "Open this URL in your browser (it is on the clipboard)"
				}
				d.lines = []string{how + ":", url, "",
					"Waiting for the browser. If it cannot reach this machine, paste the final redirect URL below."}
			})
		},
		Notice: func(msg string) { a.ui.Do(func() { d.lines = append(d.lines, msg) }) },
		ReadPasted: func() (string, error) {
			select {
			case s := <-pasted:
				return s, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	}
	go func() {
		defer cancel()
		cred, err := op.Login(ctx, ui, deviceID)
		if err == nil {
			err = config.SetOAuth(p.ID, cred)
		}
		a.ui.Do(func() {
			if ctx.Err() != nil && err != nil {
				return // cancelled; the dialog is gone
			}
			if a.modal == d {
				a.closeModal()
			}
			if err != nil {
				a.errorNotice(fmt.Errorf("failed to log in to %s: %w", p.Name, err))
				return
			}
			a.afterLogin(p, "Logged in to "+p.Name)
		})
	}()
}

// afterLogin reloads the model list so the provider's models appear in
// /model, re-applies the current model (its key may have changed) and,
// when there is no model yet, opens /model for the provider.
func (a *App) afterLogin(p config.LoginProvider, action string) {
	models, err := config.LoadModels()
	if err != nil {
		a.errorNotice(err)
		return
	}
	a.models = models
	cur := a.model()
	a.rpcErr("models/reload", nil) // the runtime takes the new key

	n := 0
	for _, r := range models.List() {
		if r.ProviderName == p.ID {
			n++
		}
	}
	switch {
	case n == 0 && config.CatalogStale():
		a.notice("%s. Its models appear once the model catalog has downloaded (atto models refresh).", action)
		go a.refreshCatalog()
	case n == 0:
		a.notice("%s, but no models are available for that provider. Use /model to select a model.", action)
	case cur.Model.ID == "":
		a.notice("%s. %d models available; pick one:", action, n)
		a.modelPicker(p.ID)
	default:
		a.notice("%s. %d models available in /model.", action, n)
	}
	a.statusTrigger()
}

func (a *App) cmdLogout(arg string) {
	ids := config.StoredCredentials()
	if len(ids) == 0 {
		a.notice("No stored credentials to remove. /logout only removes credentials saved by /login; environment variables and models.json are unchanged.")
		return
	}
	remove := func(id string) {
		found, err := config.RemoveAuth(id)
		switch {
		case err != nil:
			a.errorNotice(err)
			return
		case !found:
			a.notice("No stored credentials for %s.", id)
			return
		}
		if models, err := config.LoadModels(); err == nil {
			a.models = models
		}
		a.notice("Logged out of %s.", id)
		a.statusTrigger()
	}
	if arg != "" {
		remove(arg)
		return
	}
	p := &tui.SelectList{Title: "Select provider to log out:"}
	for _, id := range ids {
		p.Items = append(p.Items, tui.SelectItem{Label: id, Value: id})
	}
	p.OnCancel = a.closeModal
	p.OnSelect = func(it tui.SelectItem) {
		a.closeModal()
		remove(it.Value)
	}
	a.openModal(p)
}

// noModel reports, with pi's hint, that no model is selected.
func (a *App) noModel() bool {
	if a.model().Model.ID != "" {
		return false
	}
	a.notice("%s", core.NoModelsHint())
	return true
}

// loginDialog is pi's LoginDialogComponent: what the flow says, and one
// input line (masked for API keys). Esc cancels.
type loginDialog struct {
	title    string
	lines    []string
	label    string
	masked   bool
	input    []rune
	focused  bool
	onSubmit func(string)
	onCancel func()
}

func (d *loginDialog) SetFocused(f bool) { d.focused = f }

func (d *loginDialog) Render(width int) []string {
	out := []string{tui.Truncate(tui.Bold(d.title), width, "…")}
	for _, l := range d.lines {
		for _, w := range tui.Wrap(l, width) {
			out = append(out, tui.Dim(w))
		}
	}
	val := string(d.input)
	if d.masked {
		val = strings.Repeat("•", len(d.input))
	}
	cursor := ""
	if d.focused {
		cursor = tui.CursorMarker
	}
	out = append(out, "", tui.Truncate(tui.FG(6, d.label)+val+cursor, width, "…"), tui.Dim("enter submit · esc cancel"))
	return out
}

func (d *loginDialog) HandleInput(data string) {
	if text, ok := strings.CutPrefix(data, tui.PastePrefix); ok {
		d.input = append(d.input, []rune(strings.TrimSpace(text))...)
		return
	}
	switch tui.Key(data) {
	case "escape", "ctrl+c":
		d.onCancel()
	case "enter":
		d.onSubmit(string(d.input))
	case "backspace":
		if len(d.input) > 0 {
			d.input = d.input[:len(d.input)-1]
		}
	case "ctrl+u":
		d.input = nil
	default:
		for _, r := range data {
			if !unicode.IsPrint(r) {
				return
			}
		}
		d.input = append(d.input, []rune(data)...)
	}
}
