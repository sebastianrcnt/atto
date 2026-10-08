package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/sebastianrcnt/atto/approval"
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

type hookApprovals = approval.Decisions

func loadHookApprovals() hookApprovals {
	a, _ := fsutil.ReadJSON[hookApprovals](HookApprovalsPath())
	return a
}

func hookKey(h ProjectHook) string { return approval.Path(h.Path) + "#" + h.Hash() }

func HookApprovalOf(h ProjectHook) string {
	switch loadHookApprovals().Of(hookKey(h), h.Hash()) {
	case approval.Approved:
		return HookApproved
	case approval.Denied:
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
	return editHookApprovals(func(a *hookApprovals) { a.Set(hookKey(h), h.Hash(), allow) })
}

func RevokeHook(h ProjectHook) error {
	return editHookApprovals(func(a *hookApprovals) { a.Forget(hookKey(h)) })
}

func editHookApprovals(edit func(*hookApprovals)) error {
	return fsutil.EditJSON(HookApprovalsPath(), func(a *hookApprovals) { a.Init(); edit(a) })
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
	prefix := approval.Path(ProjectSettingsPath(cwd)) + "#"
	return editHookApprovals(func(a *hookApprovals) { a.ForgetPrefix(prefix) })
}
