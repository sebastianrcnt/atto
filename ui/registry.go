package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"
)

type Match struct {
	Site Site   `json:"site"`
	ID   string `json:"id,omitempty"`
}
type Event struct {
	Site    Site
	ID      string
	Surface string
	Props   map[string]any
}
type Next func(Event) (*Node, error)
type Renderer func(Event, Next) (*Node, error)
type Handler func(context.Context, Action) error
type Dispose func()
type Action struct {
	Site     Site      `json:"site"`
	ID       string    `json:"id"`
	Key      string    `json:"key"`
	Type     EventType `json:"type"`
	Value    *string   `json:"value,omitempty"`
	Rev      int64     `json:"rev"`
	ClientID string    `json:"-"`
	Surface  string    `json:"-"`
}
type OpenOptions struct {
	Site          Site   `json:"site"`
	ID            string `json:"id"`
	Title         string `json:"title,omitempty"`
	Placement     string `json:"placement,omitempty"`
	Columns       int    `json:"columns,omitempty"`
	Rows          int    `json:"rows,omitempty"`
	CloseOnEscape bool   `json:"closeOnEscape,omitempty"`
	Priority      int    `json:"priority,omitempty"`
	Align         string `json:"align,omitempty"`
	Level         string `json:"level,omitempty"`
	ExpiresAt     int64  `json:"expiresAt,omitempty"`
	FocusClientID string `json:"-"`
}
type Instance struct {
	Site    Site        `json:"site"`
	ID      string      `json:"id"`
	Rev     int64       `json:"rev"`
	Options OpenOptions `json:"options"`
	Tree    *Node       `json:"tree"`
}
type Snapshot struct {
	Version   int        `json:"version"`
	Instances []Instance `json:"instances"`
}
type Mutation struct {
	Method        string
	Instance      Instance
	Reason        string
	FocusClientID string
}
type RouteError struct {
	Reason          string
	CurrentRevision int64
	Message         string
}

func (e *RouteError) Error() string { return e.Message }

type registration struct {
	owner string
	match Match
	fn    Renderer
	seq   int
}
type binding struct {
	owner string
	fn    Handler
}
type live struct {
	Instance
	owner      string
	props      map[string]any
	fallback   *Node
	last       time.Time
	timer      *time.Timer
	generation uint64
	bindings   map[string]binding
	expired    *time.Timer
}

// Registry serializes publication and protects state, but never calls a renderer,
// handler or publisher while holding its mutex. Dispatch schedules timer work on
// the owning session lane. Publish should preserve the order of lane work.
type Registry struct {
	mu         sync.Mutex
	sites      map[Match]*live
	tombstones map[Match]int64
	renders    []*registration
	binds      map[Match]map[string]binding
	seq        int
	rev        int64
	stopped    bool
	Publish    func(Mutation)
	Dispatch   func(func())
	Log        func(owner string, site Site, id string, err error)
	ingress    map[string][]time.Time
}

func NewRegistry(publish func(Mutation), dispatch func(func())) *Registry {
	if dispatch == nil {
		dispatch = func(fn func()) { fn() }
	}
	return &Registry{sites: map[Match]*live{}, tombstones: map[Match]int64{}, binds: map[Match]map[string]binding{}, Publish: publish, Dispatch: dispatch, ingress: map[string][]time.Time{}}
}
func matches(m Match, s Match) bool {
	return (m.Site == "" || m.Site == s.Site) && (m.ID == "" || m.ID == s.ID)
}
func owned(owner, id string) bool {
	return localID.MatchString(owner) && ValidID(id) && strings.HasPrefix(id, owner+"/")
}
func (r *Registry) Render(owner string, match Match, fn Renderer) Dispose {
	r.mu.Lock()
	r.seq++
	reg := &registration{owner, match, fn, r.seq}
	r.renders = append(r.renders, reg)
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			for i, x := range r.renders {
				if x == reg {
					r.renders = append(r.renders[:i], r.renders[i+1:]...)
					break
				}
			}
			r.mu.Unlock()
			r.Invalidate(match)
		})
	}
}
func bindKey(key string, kind EventType) string { return key + "\x00" + string(kind) }
func (r *Registry) Bind(owner string, match Match, key string, kind EventType, fn Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.binds[match] == nil {
		r.binds[match] = map[string]binding{}
	}
	r.binds[match][bindKey(key, kind)] = binding{owner, fn}
}
func (r *Registry) nextRevision() int64 { r.rev++; return r.rev }
func (r *Registry) emit(m Mutation) {
	if r.Publish != nil {
		r.Publish(m)
	}
}
func (r *Registry) Open(owner string, o OpenOptions) error { return r.OpenDefault(owner, o, nil, nil) }

// OpenDefault supplies the unmodified built-in fallback and immutable render
// props. Item defaults are engine references minted only here.
func (r *Registry) OpenDefault(owner string, o OpenOptions, props map[string]any, fallback *Node) error {
	if !ValidSite(o.Site) || !owned(owner, o.ID) && !IsItem(o.Site) {
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
		fallback = &n
	}
	m := Match{o.Site, o.ID}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return fmt.Errorf("registry stopped")
	}
	s := r.sites[m]
	if s == nil {
		if len(r.sites) >= MaxSites {
			r.mu.Unlock()
			return fmt.Errorf("live site limit")
		}
		s = &live{owner: owner}
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
	s.generation++
	gen := s.generation
	e := Event{Site: m.Site, ID: m.ID, Surface: "shared", Props: copyMap(s.props)}
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
	var call func(int, Event) (*Node, error)
	call = func(i int, ev Event) (*Node, error) {
		if i == len(regs) {
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
				return n, nil
			}
			return clone(fallback), nil
		}
		reg := regs[i]
		start := time.Now()
		n, err := safeRender(reg.fn, ev, func(next Event) (*Node, error) {
			if next.Site != e.Site || next.ID != e.ID {
				return nil, fmt.Errorf("immutable identity")
			}
			return call(i+1, next)
		})
		if time.Since(start) > 100*time.Millisecond {
			return nil, fmt.Errorf("render deadline exceeded")
		}
		return n, err
	}
	started := time.Now()
	tree, err := call(0, e)
	if time.Since(started) > 250*time.Millisecond {
		err = fmt.Errorf("site deadline exceeded")
	}
	if err == nil && tree != nil {
		err = validate(m.Site, *tree, false, m.ID, nil)
	}
	if err != nil {
		tree = clone(fallback)
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
	}
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
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := Snapshot{Version: 1, Instances: []Instance{}}
	for _, s := range r.sites {
		if !IsItem(s.Site) && s.Site != Transcript && (s.Options.ExpiresAt == 0 || s.Options.ExpiresAt > time.Now().UnixMilli()) {
			out.Instances = append(out.Instances, copyInstance(s.Instance))
		}
	}
	sort.Slice(out.Instances, func(i, j int) bool { return out.Instances[i].Rev < out.Instances[j].Rev })
	return out
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
	return b.fn(ctx, a)
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
}
func findNode(n *Node, key string) *Node {
	if n == nil {
		return nil
	}
	if n.Key == key {
		return n
	}
	for i := range n.Children {
		if c := findNode(&n.Children[i], key); c != nil {
			return c
		}
	}
	return nil
}
func clone(n *Node) *Node {
	if n == nil {
		return nil
	}
	b, _ := json.Marshal(n)
	var out Node
	_ = json.Unmarshal(b, &out)
	out.engine = n.engine
	for i := range n.Children {
		out.Children[i] = *clone(&n.Children[i])
	}
	return &out
}
func copyMap(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
func copyInstance(i Instance) Instance { i.Tree = clone(i.Tree); return i }
