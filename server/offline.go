package server

import (
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/session"
)

// ReadOffline reads a saved thread without taking its writer lease.
func ReadOffline(id string) (ThreadInfo, error) { return readOffline(id) }

// readOffline reads a saved session without loading it: its items, read-only.
func readOffline(id string) (ThreadInfo, error) {
	path, err := session.Find(id)
	if err != nil {
		return ThreadInfo{}, &rpcError{Code: codeInvalidParams, Message: err.Error(), Data: &ErrorData{Reason: ReasonNotFound}}
	}
	loaded, err := session.ReadContext(path)
	saved := core.Saved{Header: loaded.Header, Entries: loaded.Entries, State: loaded.State}
	for _, e := range loaded.State {
		switch e.Type {
		case session.TypeName:
			saved.Name = e.Name
		case session.TypeContext:
			saved.LongContext = e.LongContext
		case session.TypeModel:
			saved.Model = e.Provider + "/" + e.Model
		case session.TypeEffort:
			saved.Effort = e.Effort
		}
	}
	if err != nil {
		return ThreadInfo{}, err
	}
	info := ThreadInfo{ID: saved.Header.ID, Cwd: saved.Header.Cwd, Name: saved.Name, Model: saved.Model, Effort: saved.Effort, Offline: true, SessionPath: path, LongContext: saved.LongContext}
	b := transcript.Builder{IDPrefix: itemPrefix(info.ID)}
	page, _, pageErr := replayFile(&b, info.ID, path, "", DefaultItemLimit)
	if pageErr != nil {
		return ThreadInfo{}, pageErr
	}
	info.Items, info.HasMore, info.Before = page.Items, page.HasMore, page.Before
	var u Usage
	u.Add(loaded.Usage)
	last := loaded.LastUsage
	u.Last = &last
	if settings, models, err := core.Load(); err == nil {
		if model, _, err := core.PickModelFrom(models, settings, "", saved.Model); err == nil {
			SetModel(&info, model, models)
		}
		if model, ok := models.Find("", loaded.UsageModel); ok {
			u.LastCost = model.Model.Cost
		}
		if info.Effort == "" {
			info.Effort = core.Effort(settings, "")
		}
	}
	u.LastInputTokens, u.LastCachedInputTokens = loaded.LastUsage.PromptTokens, loaded.LastUsage.CachedTokens
	info.Usage = &u
	return info, nil
}
