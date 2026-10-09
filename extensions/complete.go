package extensions

import (
	"context"
	"errors"
	"fmt"
	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"maps"
	"net"
	"slices"
	"sort"
	"strings"
	"time"
)

const canceledMsg = "atto.complete: canceled (the session ended or extensions were reloaded)"

func orDefault(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

// CompleteStat counts the requests an extension made with atto.complete
// to one model.
type CompleteStat struct {
	Model  string `json:"model"` // provider/id
	Calls  int    `json:"calls"`
	Failed int    `json:"failed,omitempty"`
}

// complete makes the request; ref is the model's provider/id once found.
func complete(ctx context.Context, model, system, prompt, effort string, maxTokens int, timeout time.Duration, sessionID string) (ref, text string, err error) {
	models, err := config.LoadModels()
	if err != nil {
		return "", "", fmt.Errorf("atto.complete: reading models.json: %w", err)
	}
	m, ok := models.Find("", model)
	if !ok {
		var names []string
		for _, r := range models.List() {
			names = append(names, r.String())
		}
		sort.Strings(names)
		return "", "", fmt.Errorf("atto.complete: unknown model %q (configured: %s)", model, orDefault(strings.Join(names, ", "), "none"))
	}
	ref = m.String()
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	am := m.AIModel()
	client := &provider.Client{Model: am, APIKey: m.APIKey, KeyFunc: m.KeyFunc, Headers: m.RequestHeaders()}
	req := provider.Request{SessionID: sessionID, Model: m.Model.ID, MaxTokens: maxTokens, ToolChoice: "none"}
	for _, l := range m.Model.Levels() {
		if l == "off" { // a side call has no use for thinking
			req.Effort = "off"
		}
	}
	if effort != "" {
		if slices.Contains(m.Model.Levels(), effort) { // a level the model's configuration knows
			req.Effort = effort
		} else if m.API() == provider.APICompletions { // as LM Studio and others take it: "none" turns thinking off
			am.ExtraBody = maps.Clone(am.ExtraBody)
			if am.ExtraBody == nil {
				am.ExtraBody = map[string]any{}
			}
			am.ExtraBody["reasoning_effort"] = effort
			client.Model = am
		} else {
			req.Effort = effort
		}
	}
	if system != "" {
		req.Messages = append(req.Messages, provider.Message{Role: "system", Content: system})
	}
	req.Messages = append(req.Messages, provider.Message{Role: "user", Content: prompt})
	res, err := client.Stream(rctx, req, provider.Handler{})
	if err != nil && rctx.Err() == nil && (req.Effort != "" || effort != "") && effortRejected(err) {
		// The provider takes no such level (a model that always thinks
		// rejects "none"): ask again at its default.
		req.Effort, client.Model = "", m.AIModel()
		res, err = client.Stream(rctx, req, provider.Handler{})
	}
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return ref, "", errors.New(canceledMsg)
		case errors.Is(rctx.Err(), context.DeadlineExceeded):
			return ref, "", fmt.Errorf("atto.complete: %s timed out after %s", ref, timeout)
		}
		return ref, "", fmt.Errorf("atto.complete: %s: %s", ref, describeNetError(err))
	}
	return ref, res.Message.Content, nil
}

// effortRejected reports a request refused for its reasoning level.
func effortRejected(err error) bool {
	msg := strings.ToLower(err.Error())
	return ai.StatusOf(err) == 400 && strings.Contains(msg, "effort")
}

// describeNetError says what went wrong in a few words: the error as the
// client words it, with a plain "connection refused" for a server that is
// not there.
func describeNetError(err error) string {
	msg := err.Error()
	var op *net.OpError
	if errors.As(err, &op) || strings.Contains(msg, "refused") {
		return "connection refused (is the server running?): " + msg
	}
	return msg
}
