package ui

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

func bindKey(key string, kind EventType) string { return key + "\x00" + string(kind) }

func (r *Registry) Bind(owner string, match Match, key string, kind EventType, fn Handler) {
	if owner != "atto" {
		key = owner + "/" + key
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.binds[match] == nil {
		r.binds[match] = map[string]binding{}
	}
	r.binds[match][bindKey(key, kind)] = binding{owner, fn}
}

func (r *Registry) Route(ctx context.Context, a Action) error {
	r.mu.Lock()
	s := r.sites[Match{a.Site, a.ID}]
	fail := func(reason, msg string) error {
		rev := int64(0)
		if s != nil {
			rev = s.Rev
		} else {
			rev = r.tombstones[Match{a.Site, a.ID}]
		}
		r.mu.Unlock()
		return &RouteError{reason, rev, msg}
	}
	if s == nil || a.Rev != s.Rev {
		return fail("revisionConflict", "UI revision is stale")
	}
	if a.Type == CloseEvent {
		if a.Key != "$site" || a.Value != nil || a.Site != Pane && a.Site != Dialog {
			return fail("invalidAction", "site is not closable")
		}
	} else {
		n := findNode(s.Tree, a.Key)
		if n == nil || n.Props["disabled"] == true {
			return fail("invalidAction", "unknown or disabled control")
		}
		known := schemas[n.Type] != ""
		declared := false
		for _, e := range n.Events {
			if e == a.Type {
				declared = true
			}
		}
		if !known || !declared {
			return fail("invalidAction", "undeclared event")
		}
		if a.Type == Press {
			if a.Value != nil {
				return fail("invalidAction", "press forbids value")
			}
		} else {
			if a.Value == nil || len(*a.Value) > MaxText || strings.ContainsAny(*a.Value, "\x00\r\n") {
				return fail("invalidAction", "invalid event value")
			}
			if n.Type == "Input" {
				limit := 4096
				if f, ok := n.Props["maxLength"].(float64); ok {
					limit = int(f)
				}
				if len([]rune(*a.Value)) > limit {
					return fail("invalidAction", "input too long")
				}
			}
			if n.Type == "Select" {
				valid := false
				b, _ := json.Marshal(n.Props["options"])
				var opts []Option
				_ = json.Unmarshal(b, &opts)
				for _, o := range opts {
					if o.Value == *a.Value && !o.Disabled {
						valid = true
					}
				}
				if !valid {
					return fail("invalidAction", "unknown/disabled option")
				}
			}
		}
	}
	b, ok := s.bindings[bindKey(a.Key, a.Type)]
	if !ok && a.Type != CloseEvent {
		return fail("unboundAction", "control is not rebound")
	}
	now := time.Now()
	if a.Type == InputEvent {
		key := a.ClientID + "\x00" + s.ID + "\x00" + a.Key
		if now.Sub(r.inputAt[key]) < 100*time.Millisecond {
			return fail("rateLimited", "input events are debounced")
		}
		r.inputAt[key] = now
	}
	times := r.ingress[a.ClientID]
	cut := 0
	for cut < len(times) && now.Sub(times[cut]) >= time.Second {
		cut++
	}
	times = times[cut:]
	if len(times) >= 20 {
		return fail("rateLimited", "UI action rate limit")
	}
	r.ingress[a.ClientID] = append(times, now)
	// Retire the accepted revision before invoking code, even if no state changed.
	// A duplicate cannot call the same callback again, including during a wait.
	s.Rev = r.nextRevision()
	inst := copyInstance(s.Instance)
	owner := s.owner
	r.mu.Unlock()
	r.emit(Mutation{Method: "ui/render", Instance: inst})
	if !ok {
		return r.CloseReason(owner, a.Site, a.ID, "user")
	}
	if r.Enqueue != nil {
		r.Enqueue(ctx, b.fn, a)
		return nil
	}
	return b.fn(ctx, a)
}

// ResetBindings retires current actions after branch movement. Fresh rendering
// must explicitly bind them; saved trees are passive until then.
func (r *Registry) ResetBindings() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.binds = map[Match]map[string]binding{}
	for _, s := range r.sites {
		s.bindings = nil
		s.generation++
		s.Rev = r.nextRevision()
	}
}

func (r *Registry) Bound(site Site, id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sites[Match{site, id}]
	return s != nil && len(s.bindings) > 0
}

func (r *Registry) DetachClient(client string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.ingress, client)
	for key := range r.inputAt {
		if strings.HasPrefix(key, client+"\x00") {
			delete(r.inputAt, key)
		}
	}
}

// ReplaceBindings retires the provider's previous callback set for this site.
// Published revisions keep their own immutable snapshot until the next render.
func (r *Registry) ReplaceBindings(owner string, match Match, handlers map[string]map[EventType]Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	bs := r.binds[match]
	if bs == nil {
		bs = map[string]binding{}
		r.binds[match] = bs
	}
	for key, b := range bs {
		if b.owner == owner {
			delete(bs, key)
		}
	}
	for key, events := range handlers {
		for kind, fn := range events {
			bs[bindKey(owner+"/"+key, kind)] = binding{owner, fn}
		}
	}
}
