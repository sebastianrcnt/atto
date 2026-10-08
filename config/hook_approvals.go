package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sebastianrcnt/atto/fsutil"
)

func HookApprovalsPath() string { return filepath.Join(Dir(), "hook-approvals.json") }

const (
	HookApproved = "approved"
	HookPending  = "pending"
	HookDenied   = "denied"
)

// ProjectHook is one hook and the context in which it runs.
type ProjectHook struct {
	Path    string
	Event   string
	Matcher string
	Spec    HookSpec
}

// Hash covers all executable configuration, including HTTP headers. JSON
// orders map keys, so formatting and header order do not change the hash.
func (h ProjectHook) Hash() string {
	data, _ := json.Marshal(struct {
		Event   string
		Matcher string
		Spec    HookSpec
	}{h.Event, h.Matcher, h.Spec})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Name identifies a hook in atto trust without depending on list order.
func (h ProjectHook) Name() string { return h.Event + "-" + h.Hash()[:12] }

func (h ProjectHook) Target() string {
	if h.Spec.Type == "http" {
		return "POST " + h.Spec.URL
	}
	return h.Spec.Command
}

func sameHookPath(a, b string) bool {
	if abs, err := filepath.Abs(a); err == nil {
		a = abs
	}
	if abs, err := filepath.Abs(b); err == nil {
		b = abs
	}
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// IsProjectHookSource distinguishes repository hooks from the user's own
// settings, including when cwd points at the user's configuration directory.
func IsProjectHookSource(path, cwd string) bool {
	return sameHookPath(path, ProjectSettingsPath(cwd)) && !sameHookPath(path, SettingsPath())
}

// ProjectHooks lists the repository's hooks without running or filtering them.
func ProjectHooks(cwd string) ([]ProjectHook, error) {
	sources, err := LoadHookSources(cwd)
	if err != nil {
		return nil, err
	}
	var out []ProjectHook
	seen := map[string]bool{}
	for _, src := range sources {
		if !IsProjectHookSource(src.Path, cwd) {
			continue
		}
		var events []string
		for event := range src.Hooks {
			events = append(events, event)
		}
		sort.Strings(events)
		for _, event := range events {
			for _, matcher := range src.Hooks[event] {
				for _, spec := range matcher.Hooks {
					h := ProjectHook{Path: src.Path, Event: event, Matcher: matcher.Matcher, Spec: spec}
					if !seen[h.Hash()] {
						out = append(out, h)
						seen[h.Hash()] = true
					}
				}
			}
		}
	}
	return out, nil
}

type hookApprovals struct {
	Approved map[string]string `json:"approved"`
	Denied   map[string]string `json:"denied,omitempty"`
}

func loadHookApprovals() hookApprovals {
	a := hookApprovals{}
	if data, err := os.ReadFile(HookApprovalsPath()); err == nil {
		_ = json.Unmarshal(data, &a)
	}
	if a.Approved == nil {
		a.Approved = map[string]string{}
	}
	if a.Denied == nil {
		a.Denied = map[string]string{}
	}
	return a
}

func hookKey(h ProjectHook) string {
	path := h.Path
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path) + "#" + h.Hash()
}

func HookApprovalOf(h ProjectHook) string {
	a := loadHookApprovals()
	key, hash := hookKey(h), h.Hash()
	switch {
	case a.Approved[key] == hash:
		return HookApproved
	case a.Denied[key] == hash:
		return HookDenied
	default:
		return HookPending
	}
}

// SetHookApproval records a decision for exactly this hook's content, rather
// than re-reading a file that could have changed since the prompt appeared.
func SetHookApproval(h ProjectHook, allow bool) error {
	if h.Path == "" || sameHookPath(h.Path, SettingsPath()) {
		return fmt.Errorf("user hooks need no approval")
	}
	return editHookApprovals(func(a *hookApprovals) {
		key := hookKey(h)
		if allow {
			a.Approved[key] = h.Hash()
			delete(a.Denied, key)
		} else {
			a.Denied[key] = h.Hash()
			delete(a.Approved, key)
		}
	})
}

func RevokeHook(h ProjectHook) error {
	return editHookApprovals(func(a *hookApprovals) {
		delete(a.Approved, hookKey(h))
		delete(a.Denied, hookKey(h))
	})
}

func editHookApprovals(edit func(*hookApprovals)) error {
	return fsutil.WithFileLock(HookApprovalsPath(), func() error {
		a := loadHookApprovals()
		edit(&a)
		data, err := json.MarshalIndent(a, "", "  ")
		if err != nil {
			return err
		}
		return fsutil.WriteAtomic(HookApprovalsPath(), append(data, '\n'), 0o600)
	})
}

// ApprovedHookSources leaves user hooks alone and removes unapproved project
// hooks. The original sources remain available for the Loaded block.
func ApprovedHookSources(sources []HookSource, cwd string) []HookSource {
	var out []HookSource
	for _, src := range sources {
		if !IsProjectHookSource(src.Path, cwd) {
			out = append(out, src)
			continue
		}
		filtered := HookSource{Path: src.Path, Hooks: map[string][]HookMatcher{}}
		for event, matchers := range src.Hooks {
			for _, matcher := range matchers {
				m := HookMatcher{Matcher: matcher.Matcher}
				for _, spec := range matcher.Hooks {
					h := ProjectHook{Path: src.Path, Event: event, Matcher: matcher.Matcher, Spec: spec}
					if HookApprovalOf(h) == HookApproved {
						m.Hooks = append(m.Hooks, spec)
					}
				}
				if len(m.Hooks) > 0 {
					filtered.Hooks[event] = append(filtered.Hooks[event], m)
				}
			}
		}
		if len(filtered.Hooks) > 0 {
			out = append(out, filtered)
		}
	}
	return out
}

// RevokeProjectHooks forgets every recorded hook decision for cwd, including
// content no longer in the file that could otherwise regain trust if restored.
func RevokeProjectHooks(cwd string) error {
	path, err := filepath.Abs(ProjectSettingsPath(cwd))
	if err != nil {
		return err
	}
	prefix := filepath.Clean(path) + "#"
	return editHookApprovals(func(a *hookApprovals) {
		for key := range a.Approved {
			if strings.HasPrefix(key, prefix) {
				delete(a.Approved, key)
			}
		}
		for key := range a.Denied {
			if strings.HasPrefix(key, prefix) {
				delete(a.Denied, key)
			}
		}
	})
}
