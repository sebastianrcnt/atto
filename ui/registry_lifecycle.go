package ui

import (
	"encoding/json"
	"fmt"
	"time"
)

type Snapshot struct {
	Version   int        `json:"version"`
	Instances []Instance `json:"instances"`
}

func (r *Registry) Open(owner string, o OpenOptions) error { return r.OpenDefault(owner, o, nil, nil) }

// OpenDefault supplies the unmodified built-in fallback and immutable render
// props. Item defaults are engine references minted only here.
func (r *Registry) OpenDefault(owner string, o OpenOptions, props map[string]any, fallback *Node) error {
	if !ValidSite(o.Site) || !owned(owner, o.ID) && !IsItem(o.Site) || IsItem(o.Site) && (o.ID == "" || len(o.ID) > 128) {
		return fmt.Errorf("invalid site/id/owner")
	}
	if o.Columns < 0 || o.Columns > 512 || o.Rows < 0 || o.Rows > 256 || o.Placement != "" && o.Placement != "auto" && o.Placement != "side" && o.Placement != "abovePrompt" || o.Align != "" && o.Align != "start" && o.Align != "end" {
		return fmt.Errorf("invalid open options")
	}
	if fallback != nil {
		if err := Validate(o.Site, *fallback); err != nil {
			return err
		}
	}
	if o.Title == "" {
		o.Title = o.ID
	}
	o.Title = CleanText(o.Title)
	if len(o.Title) > 4096 {
		return fmt.Errorf("title exceeds 4096 bytes")
	}
	if o.Placement == "" {
		o.Placement = "auto"
	}
	if o.Columns == 0 {
		o.Columns = 40
	}
	if o.Rows == 0 {
		o.Rows = 8
	}
	if o.Align == "" {
		o.Align = "start"
	}
	if IsItem(o.Site) {
		n := Node{Type: "engine", Props: map[string]any{"site": string(o.Site), "id": o.ID, "overrides": map[string]any{}}, engine: true}
		sealEngine(&n)
		fallback = &n
	}
	m := Match{o.Site, o.ID}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return fmt.Errorf("registry stopped")
	}
	total := 0
	for match, live := range r.sites {
		if match != m {
			b, _ := json.Marshal(live.Tree)
			total += len(b)
		}
	}
	defaultBytes, _ := json.Marshal(fallback)
	if total+len(defaultBytes) > MaxLiveBytes {
		r.mu.Unlock()
		return fmt.Errorf("live tree byte budget")
	}
	s := r.sites[m]
	if s == nil {
		if len(r.sites) >= MaxSites {
			r.mu.Unlock()
			return fmt.Errorf("live site limit")
		}
		r.siteSeq++
		s = &live{owner: owner, order: r.siteSeq}
		r.sites[m] = s
	} else if s.owner != owner {
		r.mu.Unlock()
		return fmt.Errorf("site owned by another provider")
	}
	s.Options = o
	s.Site = o.Site
	s.ID = o.ID
	s.props = props
	s.fallback = clone(fallback)
	s.generation++
	s.Rev = r.nextRevision()
	inst := copyInstance(s.Instance)
	r.mu.Unlock()
	r.emit(Mutation{Method: "ui/open", Instance: inst, FocusClientID: o.FocusClientID})
	r.render(m, true)
	if o.ExpiresAt > 0 {
		r.mu.Lock()
		if s.expired != nil {
			s.expired.Stop()
		}
		s.expired = time.AfterFunc(time.Until(time.UnixMilli(o.ExpiresAt)), func() { r.Dispatch(func() { _ = r.CloseReason(owner, o.Site, o.ID, "expired") }) })
		r.mu.Unlock()
	}
	return nil
}

func (r *Registry) Close(owner string, site Site, id string) error {
	return r.CloseReason(owner, site, id, "provider")
}

func (r *Registry) CloseReason(owner string, site Site, id, reason string) error {
	m := Match{site, id}
	r.mu.Lock()
	s := r.sites[m]
	if s == nil {
		r.mu.Unlock()
		return nil
	}
	if s.owner != owner {
		r.mu.Unlock()
		return fmt.Errorf("site owned by another provider")
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.expired != nil {
		s.expired.Stop()
	}
	delete(r.sites, m)
	delete(r.binds, m)
	s.Rev = r.nextRevision()
	r.tombstones[m] = s.Rev
	inst := copyInstance(s.Instance)
	r.mu.Unlock()
	r.emit(Mutation{Method: "ui/close", Instance: inst, Reason: reason})
	return nil
}

func (r *Registry) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	for _, s := range r.sites {
		if s.timer != nil {
			s.timer.Stop()
		}
		if s.expired != nil {
			s.expired.Stop()
		}
	}
	r.binds = map[Match]map[string]binding{}
}

func (r *Registry) Unload(owner string) {
	r.mu.Lock()
	var ms []Match
	for m, s := range r.sites {
		if s.owner == owner {
			ms = append(ms, m)
		}
	}
	for _, s := range r.sites {
		for key, b := range s.bindings {
			if b.owner == owner {
				delete(s.bindings, key)
			}
		}
	}
	for m, bs := range r.binds {
		for key, b := range bs {
			if b.owner == owner {
				delete(bs, key)
			}
		}
		if len(bs) == 0 {
			delete(r.binds, m)
		}
	}
	var regs []*registration
	for _, reg := range r.renders {
		if reg.owner != owner {
			regs = append(regs, reg)
		}
	}
	r.renders = regs
	r.mu.Unlock()
	for _, m := range ms {
		_ = r.CloseReason(owner, m.Site, m.ID, "unload")
	}
	r.mu.Lock()
	var remaining []Match
	for m := range r.sites {
		remaining = append(remaining, m)
	}
	r.mu.Unlock()
	for _, m := range remaining {
		r.render(m, true)
	}
}

// HasRenderer allows a worker to avoid creating overlays for unwrapped native items.
func (r *Registry) HasRenderer(match Match) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, reg := range r.renders {
		if matches(reg.match, match) {
			return true
		}
	}
	return false
}

// Forget drops completed display data without hiding its saved transcript item.
func (r *Registry) Forget(match Match) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for m, s := range r.sites {
		if matches(match, m) {
			if s.timer != nil {
				s.timer.Stop()
			}
			if s.expired != nil {
				s.expired.Stop()
			}
			delete(r.sites, m)
			delete(r.binds, m)
		}
	}
}

func (r *Registry) Revision(site Site, id string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sites[Match{site, id}]; s != nil {
		return s.Rev
	}
	if rev := r.tombstones[Match{site, id}]; rev != 0 {
		return rev
	}
	return r.rev
}

// DrawItem is the automatic item-site lifecycle, not provider Open/Close.
func (r *Registry) DrawItem(site Site, id string, props map[string]any) (*Node, int64, error) {
	if !IsItem(site) || id == "" {
		return nil, 0, fmt.Errorf("invalid item identity")
	}
	m := Match{site, id}
	r.mu.Lock()
	s := r.sites[m]
	if s == nil {
		if len(r.sites) >= MaxSites {
			r.mu.Unlock()
			return nil, 0, fmt.Errorf("live site limit")
		}
		n := Node{Type: "engine", Props: map[string]any{"site": string(site), "id": id, "overrides": map[string]any{}}, engine: true}
		s = &live{owner: "atto", Site: site, ID: id, fallback: &n}
		r.sites[m] = s
	}
	s.props = copyMap(props)
	r.mu.Unlock()
	r.render(m, false)
	r.mu.Lock()
	defer r.mu.Unlock()
	s = r.sites[m]
	if s == nil {
		return nil, 0, nil
	}
	tree := clone(s.Tree)
	if tree != nil && tree.Type == "engine" && len(tree.Props["overrides"].(map[string]any)) == 0 {
		tree = nil
	}
	return tree, s.Rev, nil
}

// UpdateProps coalesces props changes without publishing another open.
func (r *Registry) UpdateProps(match Match, props map[string]any) {
	r.mu.Lock()
	s := r.sites[match]
	if s == nil || equalProps(s.props, props) {
		r.mu.Unlock()
		return
	}
	s.props = copyMap(props)
	r.mu.Unlock()
	r.render(match, false)
}

// SetProps supplies worker-owned identity during open publication, before render.
func (r *Registry) SetProps(match Match, props map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sites[match]; s != nil {
		s.props = copyMap(props)
	}
}
