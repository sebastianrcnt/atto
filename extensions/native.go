package extensions

import (
	"context"
	_ "embed"
	"slices"
	"sync"
)

//go:embed native_diff.go
var diffSource string

//go:embed native_autorename.go
var autorenameSource string

// nativeState bounds the lifetime of the native commands just as the old
// runtime did. Close/reload cancel requests and wait before clearing UI.
type nativeState struct {
	mu     sync.Mutex
	infos  []Info
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool
	slots  chan struct{}
}

func (m *Manager) loadNative(specs []Spec, disabled []string) {
	n := &nativeState{slots: make(chan struct{}, 1)}
	n.ctx, n.cancel = context.WithCancel(context.Background())
	for _, s := range specs {
		status := Loaded
		if slices.Contains(disabled, s.Name) {
			status = Disabled
		}
		src, _ := BuiltinSource(s.Name)
		in := Info{Name: s.Name, Path: s.Path, Source: Builtin, Status: status, Hash: hash(src)}
		if status == Loaded {
			in.Commands = []string{s.Name}
		}
		n.infos = append(n.infos, in)
	}
	m.mu.Lock()
	m.native = n
	m.mu.Unlock()
}
func (m *Manager) nativeReport() []Info {
	m.mu.Lock()
	n := m.native
	m.mu.Unlock()
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	out := slices.Clone(n.infos)
	for i := range out {
		out[i].Completes = slices.Clone(out[i].Completes)
	}
	return out
}
func (m *Manager) nativeCommands() []Command {
	var out []Command
	for _, in := range m.nativeReport() {
		if in.Status != Loaded {
			continue
		}
		desc := "Show what changed in the working tree: /diff [--staged] [path]"
		if in.Name == "autorename" {
			desc = "Name this conversation from what it is about (the current model writes the name)"
		}
		out = append(out, Command{Name: in.Name, Ext: in.Name, Description: desc})
	}
	return out
}
func (m *Manager) runNative(name, args string) bool {
	m.mu.Lock()
	n := m.native
	m.mu.Unlock()
	if n == nil {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || n.ctx.Err() != nil || !slices.ContainsFunc(n.infos, func(in Info) bool { return in.Name == name && in.Status == Loaded }) {
		return false
	}
	h := m.host()
	ctx := n.ctx
	n.wg.Go(func() {
		if name == "diff" {
			m.nativeDiff(ctx, h, args)
		} else {
			m.nativeAutorename(ctx, h, n)
		}
	})
	return true
}
func (m *Manager) closeNative() {
	m.mu.Lock()
	n := m.native
	m.native = nil
	m.mu.Unlock()
	if n == nil {
		return
	}
	n.mu.Lock()
	n.closed = true
	n.cancel()
	n.mu.Unlock()
	n.wg.Wait()
	for _, in := range n.infos {
		m.host().ClearUI(in.Name)
	}
}
func (n *nativeState) count(model string, failed bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := range n.infos {
		in := &n.infos[i]
		if in.Name != "autorename" {
			continue
		}
		for j := range in.Completes {
			if in.Completes[j].Model == model {
				in.Completes[j].Calls++
				if failed {
					in.Completes[j].Failed++
				}
				return
			}
		}
		st := CompleteStat{Model: model, Calls: 1}
		if failed {
			st.Failed = 1
		}
		in.Completes = append(in.Completes, st)
	}
}

func (m *Manager) endNative() {
	m.mu.Lock()
	n := m.native
	m.mu.Unlock()
	if n == nil {
		return
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.cancel()
	n.ctx, n.cancel = context.WithCancel(context.Background())
	n.mu.Unlock()
}

// NativeCommand identifies a native catalog entry without changing Command's
// exported layout, which user-facing callers may construct positionally.
func (m *Manager) NativeCommand(c Command) bool {
	if c.Name != c.Ext {
		return false
	}
	for _, in := range m.nativeReport() {
		if in.Name == c.Ext && in.Status == Loaded {
			return true
		}
	}
	return false
}
