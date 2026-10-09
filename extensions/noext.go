//go:build noext

package extensions

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/mcp"
)

const Supported = false

type Manager struct {
	cwd    string
	ag     *agent.Agent
	mu     sync.Mutex
	logMu  sync.Mutex
	h      Host
	id     string
	native *nativeState
	infos  []Info
	cmdVer atomic.Uint64
}

func Load(o Options) *Manager {
	m := &Manager{cwd: o.Cwd, ag: o.Agent, h: o.Host}
	if m.h == nil {
		m.h = &Headless{}
	}
	m.load()
	return m
}
func (m *Manager) SetHost(h Host)       { m.mu.Lock(); m.h = h; m.mu.Unlock() }
func (m *Manager) host() Host           { m.mu.Lock(); defer m.mu.Unlock(); return m.h }
func (m *Manager) SetSession(id string) { m.mu.Lock(); m.id = id; m.mu.Unlock() }
func (m *Manager) session() (id, model string) {
	m.mu.Lock()
	id = m.id
	m.mu.Unlock()
	if m.ag != nil {
		if ref, _ := m.ag.Current(); ref.Model.ID != "" {
			model = ref.String()
		}
	}
	return
}
func Inspect(cwd string) []Info {
	var out []Info
	for _, d := range Dirs(cwd) {
		for _, s := range scan(d) {
			out = append(out, Info{Name: s.Name, Path: s.Path, Source: s.Source, Status: Ignored})
		}
	}
	return out
}
func (m *Manager) load() {
	m.mu.Lock()
	m.infos = Inspect(m.cwd)
	m.mu.Unlock()
	_, disabled := settings()
	m.loadNative(builtinSpecs(), disabled)
	m.cmdVer.Add(1)
}
func (m *Manager) Reload() { m.Close(); m.load() }
func (m *Manager) Close()  { m.closeNative(); m.cmdVer.Add(1) }
func (m *Manager) Report() []Info {
	m.mu.Lock()
	out := append([]Info{}, m.infos...)
	m.mu.Unlock()
	return out
}
func (m *Manager) CommandsVersion() uint64                              { return m.cmdVer.Load() }
func (m *Manager) Commands() []Command                                  { return m.nativeCommands() }
func (m *Manager) RunCommand(name, args string) bool                    { return m.runNative(name, args) }
func (m *Manager) SetMCP(mcp.Backend)                                   {}
func (m *Manager) SessionStart(string)                                  {}
func (m *Manager) SessionEnd(string)                                    { m.endNative() }
func (m *Manager) BlockEnd(string, string, string, string)              {}
func (m *Manager) StepEnd(agent.StepEnd, string)                        {}
func (m *Manager) TurnStart(string)                                     {}
func (m *Manager) TurnEnd(error)                                        {}
func (m *Manager) UserPrompt(context.Context, string) agent.HookOutcome { return agent.HookOutcome{} }
func (m *Manager) ToolCall(_ context.Context, args agent.BashArgs) (agent.BashArgs, agent.HookOutcome) {
	return args, agent.HookOutcome{}
}
func (m *Manager) ToolResult(_ context.Context, _ agent.BashArgs, _ agent.BashResult, output string) (string, agent.HookOutcome) {
	return output, agent.HookOutcome{}
}
func Bundle(string) (string, error) { return "", fmt.Errorf("%s", UnsupportedMessage(0)) }
