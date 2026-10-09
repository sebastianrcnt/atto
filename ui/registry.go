package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Match struct {
	Site Site   `json:"site"`
	ID   string `json:"id,omitempty"`
}
type Event struct {
	Context context.Context
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
	Site          Site   `json:"-"`
	ID            string `json:"-"`
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
	running atomic.Bool
	owner   string
	match   Match
	fn      Renderer
	seq     int
}
type binding struct {
	owner string
	fn    Handler
}
type live struct {
	order int
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
	siteSeq int
	busy    bool
	// Enqueue dispatches accepted callbacks after Route returns; worker lanes use it.
	Enqueue         func(context.Context, Handler, Action)
	providerRenders map[string][]time.Time
	inputAt         map[string]time.Time
	mu              sync.Mutex
	sites           map[Match]*live
	tombstones      map[Match]int64
	renders         []*registration
	binds           map[Match]map[string]binding
	seq             int
	rev             int64
	stopped         bool
	Publish         func(Mutation)
	Dispatch        func(func())
	Log             func(owner string, site Site, id string, err error)
	ingress         map[string][]time.Time
}

func NewRegistry(publish func(Mutation), dispatch func(func())) *Registry {
	if dispatch == nil {
		dispatch = func(fn func()) { fn() }
	}
	return &Registry{rev: time.Now().UnixMilli() << 10, providerRenders: map[string][]time.Time{}, inputAt: map[string]time.Time{}, sites: map[Match]*live{}, tombstones: map[Match]int64{}, binds: map[Match]map[string]binding{}, Publish: publish, Dispatch: dispatch, ingress: map[string][]time.Time{}}
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
	reg := &registration{owner: owner, match: match, fn: fn, seq: r.seq}
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
			for m, bs := range r.binds {
				if matches(match, m) {
					for key, b := range bs {
						if b.owner == owner {
							delete(bs, key)
						}
					}
				}
			}
			for m, s := range r.sites {
				if matches(match, m) {
					for key, b := range s.bindings {
						if b.owner == owner {
							delete(s.bindings, key)
						}
					}
					s.bindings = nil
				}
			}
			r.mu.Unlock()
			r.Invalidate(match)
		})
	}
}
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
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := Snapshot{Version: 1, Instances: []Instance{}}
	order := map[Match]int{}
	for _, s := range r.sites {
		if !IsItem(s.Site) && s.Site != Transcript && (s.Options.ExpiresAt == 0 || s.Options.ExpiresAt > time.Now().UnixMilli()) {
			out.Instances = append(out.Instances, copyInstance(s.Instance))
			order[Match{s.Site, s.ID}] = s.order
		}
	}
	sort.Slice(out.Instances, func(i, j int) bool {
		return order[Match{out.Instances[i].Site, out.Instances[i].ID}] < order[Match{out.Instances[j].Site, out.Instances[j].ID}]
	})
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
func findNode(n *Node, key string) *Node {
	if n == nil {
		return nil
	}
	if n.Key == key {
		return n
	}
	if !KnownElement(n.Type) {
		return nil
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
	out.owner = n.owner
	out.seal = n.seal
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

func expandDialog(tree *Node, id string, fallback *Node) (*Node, error) {
	refs := 0
	count := 0
	var expand func(*Node, int) (*Node, error)
	expand = func(n *Node, depth int) (*Node, error) {
		if n == nil {
			return nil, nil
		}
		count++
		if count > MaxNodes || depth > MaxDepth {
			return nil, fmt.Errorf("dialog wrapper too large")
		}
		if n.Type == "engine" {
			refs++
			if !n.engine || n.Props["site"] != string(Dialog) || n.Props["id"] != id || len(n.Props) != 2 || len(n.Children) > 0 || len(n.Events) > 0 {
				return nil, fmt.Errorf("invalid dialog reference")
			}
			return clone(fallback), nil
		}
		out := *n
		out.Children = append([]Node(nil), n.Children...)
		for i := range out.Children {
			child, err := expand(&out.Children[i], depth+1)
			if err != nil {
				return nil, err
			}
			out.Children[i] = *child
		}
		return &out, nil
	}
	out, err := expand(tree, 1)
	if err != nil {
		return nil, err
	}
	if refs != 1 {
		return nil, fmt.Errorf("helper dialog must include next exactly once")
	}
	return out, nil
}

func equalTrees(a, b *Node) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
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

func markOwner(n *Node, owner string) {
	if n == nil {
		return
	}
	n.owner = owner
	for i := range n.Children {
		markOwner(&n.Children[i], owner)
	}
}
func prefixKeys(n *Node, owner string) {
	if n == nil || n.Type == "engine" {
		return
	}
	if n.owner == "" {
		n.owner = owner
		if owner != "atto" && n.Key != "" {
			n.Key = owner + "/" + n.Key
		}
	}
	for i := range n.Children {
		prefixKeys(&n.Children[i], owner)
	}
}

func (r *Registry) Bound(site Site, id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sites[Match{site, id}]
	return s != nil && len(s.bindings) > 0
}

func validNextProps(original, next map[string]any, site Site) bool {
	allowed := map[string]bool{}
	for _, key := range overrideFields(site) {
		allowed[key] = true
	}
	a, b := map[string]any{}, map[string]any{}
	for key, value := range original {
		if !allowed[key] {
			a[key] = value
		}
	}
	for key, value := range next {
		if !allowed[key] {
			b[key] = value
		}
	}
	return equalProps(a, b)
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

func sealEngine(n *Node) { b, _ := json.Marshal(n.Props); n.seal = string(b) }

func setEngineOverrides(n *Node, overrides map[string]any) {
	if n == nil {
		return
	}
	if n.Type == "engine" {
		n.Props["overrides"] = overrides
		sealEngine(n)
	}
	for i := range n.Children {
		setEngineOverrides(&n.Children[i], overrides)
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

// SetProps supplies worker-owned identity during open publication, before render.
func (r *Registry) SetProps(match Match, props map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sites[match]; s != nil {
		s.props = copyMap(props)
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
