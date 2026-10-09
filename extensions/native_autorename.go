package extensions

import (
	"context"
	"fmt"
	"github.com/sebastianrcnt/atto/ui"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const nameSystem = "You name conversations between a user and a coding assistant. Reply with a title of 3 to 6 words " +
	"that says what the conversation is about, in the language the user writes in. " +
	"No quotes, no punctuation at the end, nothing else."

var namePrefix = regexp.MustCompile(`(?i)^(title|name)[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*:[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*`)

func jsSpace(r rune) bool {
	return unicode.Is(unicode.Zs, r) || strings.ContainsRune("\t\n\v\f\r\u2028\u2029\ufeff", r)
}

func cleanName(reply string) string {
	name, _, _ := strings.Cut(strings.TrimFunc(reply, jsSpace), "\n")
	name = strings.TrimFunc(name, jsSpace)
	name = namePrefix.ReplaceAllString(name, "")
	name = strings.TrimLeftFunc(name, func(r rune) bool { return jsSpace(r) || strings.ContainsRune("#*_`\"'“”‘’", r) })
	name = strings.TrimRightFunc(name, func(r rune) bool { return jsSpace(r) || strings.ContainsRune("*_`\"'“”‘’.", r) })
	if jsLen(name) > 80 {
		name = strings.TrimRightFunc(jsSlice(name, 80), jsSpace)
	}
	return name
}
func clipNameMessage(text string, n int) string {
	if jsLen(text) > n {
		return jsSlice(text, n) + "…"
	}
	return text
}

// The shipped TS registered /autorename only, with no automatic trigger.
// Preserve that: /name is never overwritten until /autorename is invoked.
func (m *Manager) nativeAutorename(ctx context.Context, h Host, n *nativeState) {
	id, model := m.session()
	current, msgs := m.sessionText(30)
	notify := func(text, level string) {
		if ctx.Err() == nil {
			h.Notify("autorename", text, level)
		}
	}
	if model == "" {
		notify("autorename: no model to ask", "warning")
		return
	}
	hasUser := false
	var convo []string
	for _, msg := range msgs {
		role, limit := "Assistant", 300
		if msg.Role == "user" {
			hasUser = true
			role, limit = "User", 600
		}
		convo = append(convo, role+": "+clipNameMessage(msg.Content, limit))
	}
	if !hasUser {
		notify("autorename: nothing to name yet", "warning")
		return
	}
	prompt := ""
	if current != "" {
		prompt = "The conversation is named \"" + current + "\" now.\n\n"
	}
	prompt += "The conversation so far (latest last):\n\n" + strings.Join(convo, "\n\n") + "\n\nTitle:"
	naming := ui.Text(ui.TextProps{Text: "naming…", Color: ui.Muted})
	UIWork(h, func(r *ui.Registry) error {
		return r.OpenDefault("autorename", ui.OpenOptions{Site: ui.Status, ID: "autorename/naming"}, nil, &naming)
	}, func(error) {})
	defer UIWork(h, func(r *ui.Registry) error { return r.Close("autorename", ui.Status, "autorename/naming") }, func(error) {})
	ask := func(effort string) (string, error) {
		select {
		case n.slots <- struct{}{}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		defer func() { <-n.slots }()
		start := time.Now()
		ref, text, err := complete(ctx, model, nameSystem, prompt, effort, 64, 60*time.Second, id)
		n.count(orDefault(ref, model), err != nil)
		outcome := "ok"
		if err != nil {
			outcome = "failed: " + err.Error()
		}
		m.log("autorename", fmt.Sprintf("complete %s %s in %s (prompt %d chars, reply %d chars)", orDefault(ref, model), outcome, time.Since(start).Round(time.Millisecond), len(nameSystem)+len(prompt), len(text)))
		return text, err
	}
	reply, err := ask("none")
	if err != nil && ctx.Err() == nil {
		reply, err = ask("")
	}
	if err != nil {
		notify("autorename: "+err.Error(), "error")
		return
	}
	name := cleanName(reply)
	if name == "" {
		notify("autorename: the model gave no name", "warning")
		return
	}
	// ctx.session.setName normalizes whitespace; retain that API behavior.
	normalized := strings.Join(strings.Fields(name), " ")
	if normalized == "" {
		notify("autorename: the name is empty", "error")
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err := h.SetSessionName("autorename", normalized); err != nil {
		notify("autorename: "+err.Error(), "error")
		return
	}
	notify("Named this conversation \""+name+"\" (by "+model+").", "info")
}
