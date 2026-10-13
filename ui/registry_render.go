package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"time"
)

func (r *Registry) Invalidate(match Match) {
	r.mu.Lock()
	var ms []Match
	for m := range r.sites {
		if matches(match, m) {
			ms = append(ms, m)
		}
	}
	r.mu.Unlock()
	sort.Slice(ms, func(i, j int) bool { return ms[i].Site < ms[j].Site || ms[i].Site == ms[j].Site && ms[i].ID < ms[j].ID })
	for _, m := range ms {
		r.render(m, false)
	}
}

func (r *Registry) render(m Match, force bool) {
	r.mu.Lock()
	s := r.sites[m]
	if s == nil || r.stopped {
		r.mu.Unlock()
		return
	}
	if !force && time.Since(s.last) < 100*time.Millisecond {
		if s.timer == nil {
			s.timer = time.AfterFunc(100*time.Millisecond-time.Since(s.last), func() {
				r.Dispatch(func() {
					r.mu.Lock()
					if r.sites[m] == s {
						s.timer = nil
					}
					r.mu.Unlock()
					r.render(m, false)
				})
			})
		}
		r.mu.Unlock()
		return
	}
	// Bound provider publication rate as well as the per-site rate.
	now := time.Now()
	times := r.providerRenders[s.owner]
	cut := 0
	for cut < len(times) && now.Sub(times[cut]) >= time.Second {
		cut++
	}
	times = times[cut:]
	r.providerRenders[s.owner] = times
	if !force && len(times) >= 60 {
		if s.timer == nil {
			wait := max(time.Millisecond, time.Second-now.Sub(times[0]))
			s.timer = time.AfterFunc(wait, func() {
				r.Dispatch(func() {
					r.mu.Lock()
					if r.sites[m] == s {
						s.timer = nil
					}
					r.mu.Unlock()
					r.render(m, false)
				})
			})
		}
		r.mu.Unlock()
		return
	}
	s.generation++
	gen := s.generation
	e := Event{Site: m.Site, ID: m.ID, Surface: "shared", Props: copyMap(s.props)}
	if e.Props == nil {
		e.Props = map[string]any{}
	}
	switch m.Site {
	case Pane:
		for key, value := range map[string]any{"title": s.Options.Title, "placement": s.Options.Placement, "columns": s.Options.Columns, "rows": s.Options.Rows, "closeOnEscape": s.Options.CloseOnEscape} {
			if _, ok := e.Props[key]; !ok {
				e.Props[key] = value
			}
		}
	case Band:
		e.Props["busy"] = r.busy
	case Status:
		e.Props["busy"] = r.busy
		e.Props["priority"] = s.Options.Priority
		e.Props["align"] = s.Options.Align
	case Toast:
		e.Props["level"] = s.Options.Level
		e.Props["expiresAt"] = s.Options.ExpiresAt
	}
	originalProps := copyMap(e.Props)
	fallback := clone(s.fallback)
	owner := s.owner
	var regs []*registration
	for _, reg := range r.renders {
		if matches(reg.match, m) {
			regs = append(regs, reg)
		}
	}
	r.mu.Unlock()
	// Built-ins are always at the tail, independent of when they registered.
	sort.SliceStable(regs, func(i, j int) bool { return regs[i].owner != "atto" && regs[j].owner == "atto" })
	siteContext, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	e.Context = siteContext
	tailStart := len(regs)
	for i, reg := range regs {
		if reg.owner == "atto" {
			tailStart = i
			break
		}
	}
	preparing := true
	var defaultTree *Node
	var defaultErr error
	var call func(int, Event) (*Node, error)
	call = func(i int, ev Event) (*Node, error) {
		if siteContext.Err() != nil {
			return nil, fmt.Errorf("site render deadline exceeded")
		}
		if !preparing && i == tailStart {
			if defaultErr != nil {
				return nil, defaultErr
			}
			n := clone(defaultTree)
			if IsItem(m.Site) {
				over := map[string]any{}
				for _, key := range overrideFields(m.Site) {
					if value, ok := ev.Props[key]; ok && value != originalProps[key] {
						if _, ok := value.(string); !ok {
							return nil, fmt.Errorf("invalid display override")
						}
						over[key] = value
					}
				}
				setEngineOverrides(n, over)
			}
			return n, nil
		}

		if i == len(regs) {
			if m.Site == Dialog && fallback != nil {
				return &Node{Type: "engine", Props: map[string]any{"site": string(Dialog), "id": m.ID}, engine: true}, nil
			}
			if IsItem(m.Site) {
				n := clone(fallback)
				over := map[string]any{}
				for _, k := range overrideFields(m.Site) {
					if v, ok := ev.Props[k]; ok && v != originalProps[k] {
						if _, ok := v.(string); !ok {
							return nil, fmt.Errorf("invalid override")
						}
						over[k] = v
					}
				}
				n.Props["overrides"] = over
				sealEngine(n)
				return n, nil
			}
			n := clone(fallback)
			markOwner(n, owner)
			return n, nil
		}
		reg := regs[i]
		start := time.Now()
		nextFn := func(next Event) (*Node, error) {
			if next.Site != e.Site || next.ID != e.ID || next.Surface != e.Surface || !validNextProps(originalProps, next.Props, m.Site) {
				return nil, fmt.Errorf("immutable identity")
			}
			return call(i+1, next)
		}
		var n *Node
		var err error
		if reg.owner == "atto" {
			n, err = safeRender(reg.fn, ev, nextFn)
		} else {
			n, err = deadlineRender(reg, ev, nextFn, siteContext)
		}
		if time.Since(start) > 100*time.Millisecond {
			return nil, fmt.Errorf("render deadline exceeded")
		}
		if n != nil {
			prefixKeys(n, reg.owner)
		}
		return n, err
	}
	// Evaluate the Go-owned tail on the worker lane before external providers.
	// A timed-out provider can only see these immutable snapshots via next().
	defaultTree, defaultErr = call(tailStart, e)
	preparing = false
	started := time.Now()
	tree, err := call(0, e)
	if err == nil && m.Site == Dialog && fallback != nil {
		tree, err = expandDialog(tree, m.ID, fallback)
	}
	if time.Since(started) > 250*time.Millisecond {
		err = fmt.Errorf("site deadline exceeded")
	}
	if err == nil && tree != nil {
		err = validate(m.Site, *tree, false, m.ID, nil)
	}
	if err != nil {
		tree = clone(fallback)
		// Recompute the unmodified built-in tail, not the last published tree or
		// metadata from when the site first opened.
		if defaultTree, defaultErr := clone(defaultTree), defaultErr; defaultErr == nil {
			if m.Site == Dialog && fallback != nil {
				defaultTree, defaultErr = expandDialog(defaultTree, m.ID, fallback)
			}
			if defaultErr == nil && defaultTree != nil && validate(m.Site, *defaultTree, false, m.ID, nil) == nil {
				tree = defaultTree
			}
		}
		if r.Log != nil {
			r.Log(owner, m.Site, m.ID, err)
		}
	}
	if tree == nil && fallback == nil && m.Site != Band && m.Site != Status && m.Site != Transcript {
		n := Text(TextProps{Text: "[unavailable]", Color: Muted})
		tree = &n
	}
	r.mu.Lock()
	if r.sites[m] != s || s.generation != gen {
		r.mu.Unlock()
		return
	}
	size := 0
	for other, x := range r.sites {
		if other != m {
			b, _ := json.Marshal(x.Tree)
			size += len(b)
		}
	}
	b, _ := json.Marshal(tree)
	if size+len(b) > MaxLiveBytes {
		tree = clone(fallback)
		fb, _ := json.Marshal(tree)
		if size+len(fb) > MaxLiveBytes {
			tree = nil
		}
	}
	external := false
	for _, reg := range regs {
		if reg.owner != "atto" {
			external = true
		}
	}
	if !force && !external && s.bindings != nil && equalTrees(s.Tree, tree) {
		s.last = time.Now()
		r.mu.Unlock()
		return
	}
	r.providerRenders[s.owner] = append(r.providerRenders[s.owner], time.Now())
	s.Tree = clone(tree)
	s.Rev = r.nextRevision()
	s.last = time.Now()
	s.bindings = map[string]binding{}
	for match, bs := range r.binds {
		if matches(match, m) {
			maps.Copy(s.bindings, bs)
		}
	}
	inst := copyInstance(s.Instance)
	r.mu.Unlock()
	r.emit(Mutation{Method: "ui/render", Instance: inst})
}

func safeRender(fn Renderer, e Event, next Next) (n *Node, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("renderer panic: %v", p)
		}
	}()
	return fn(e, next)
}

func overrideFields(site Site) []string {
	switch site {
	case ToolCall:
		return []string{"description", "output"}
	case Notice:
		return []string{"text", "title"}
	default:
		return []string{"text"}
	}
}

func deadlineRender(reg *registration, e Event, next Next, site context.Context) (*Node, error) {
	if !reg.running.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("provider's previous render has not returned")
	}
	ctx, cancel := context.WithTimeout(site, 100*time.Millisecond)
	defer cancel()
	e.Context = ctx
	type result struct {
		tree *Node
		err  error
	}
	done := make(chan result, 1)
	go func() {
		defer reg.running.Store(false)
		n, err := safeRender(reg.fn, e, func(nextEvent Event) (*Node, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return next(nextEvent)
		})
		done <- result{n, err}
	}()
	select {
	case out := <-done:
		return out.tree, out.err
	case <-ctx.Done():
		return nil, fmt.Errorf("provider render deadline exceeded")
	}
}

func (r *Registry) InvalidateOwner(owner string, match Match) {
	r.mu.Lock()
	var ms []Match
	for m, s := range r.sites {
		if matches(match, m) && (s.owner == owner || IsItem(m.Site)) {
			ms = append(ms, m)
		}
	}
	r.mu.Unlock()
	for _, m := range ms {
		r.Invalidate(m)
	}
}

// SetBusy refreshes session-owned band/status props, independent of clients.
func (r *Registry) SetBusy(busy bool) {
	r.mu.Lock()
	changed := r.busy != busy
	r.busy = busy
	r.mu.Unlock()
	if changed {
		r.Invalidate(Match{Site: Band})
		r.Invalidate(Match{Site: Status})
	}
}
