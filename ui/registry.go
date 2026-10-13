package ui

import (
	"context"
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

func (r *Registry) nextRevision() int64 { r.rev++; return r.rev }

func (r *Registry) emit(m Mutation) {
	if r.Publish != nil {
		r.Publish(m)
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
