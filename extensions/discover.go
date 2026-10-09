package extensions

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/approval"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/mcp"
)

// Where an extension was found.
const (
	User    = "user"    // ~/.atto/extensions
	Project = "project" // <project root>/.atto/extensions; needs approval
	// Builtin extensions are part of atto (see builtin.go); a user or project
	// extension of the same name replaces one.
	Builtin = "builtin"
)

// Spec is an extension found on disk.
type Spec struct {
	Name   string `json:"name"`
	Path   string `json:"path"` // the entry file
	Source string `json:"source"`
}

// Dirs are the extension directories for a session in cwd: the user's,
// then the project's (at its root, see agent.ProjectRoot).
func Dirs(cwd string) []Dir {
	user := config.ExtensionsDir()
	dirs := []Dir{{Path: user, Source: User}}
	if p := config.ProjectExtensionsDir(agent.ProjectRoot(cwd)); !samePath(p, user) {
		dirs = append(dirs, Dir{Path: p, Source: Project})
	}
	return dirs
}

// Dir is a directory extensions are loaded from.
type Dir struct{ Path, Source string }

// Discover lists the extensions for a session in cwd: user ones first,
// then the project's, then the built-in ones that no other extension of
// the same name replaces; each directory in name order: <dir>/<name>.ts or .js, and
// <dir>/<name>/index.ts or index.js. Declaration files (.d.ts) and names
// starting with "." are not extensions.
func Discover(cwd string) []Spec {
	var out []Spec
	for _, d := range Dirs(cwd) {
		out = append(out, scan(d)...)
	}
	for _, b := range builtinSpecs() {
		if !slices.ContainsFunc(out, func(s Spec) bool { return s.Name == b.Name }) {
			out = append(out, b)
		}
	}
	return out
}

func scan(d Dir) []Spec {
	entries, err := os.ReadDir(d.Path)
	if err != nil {
		return nil
	}
	var out []Spec
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		path := filepath.Join(d.Path, name)
		if e.IsDir() || isDirLink(path, e) {
			for _, index := range []string{"index.ts", "index.tsx", "index.js", "index.jsx"} {
				if st, err := os.Stat(filepath.Join(path, index)); err == nil && !st.IsDir() {
					out = append(out, Spec{Name: name, Path: filepath.Join(path, index), Source: d.Source})
					break
				}
			}
			continue
		}
		if strings.HasSuffix(name, ".d.ts") {
			continue
		}
		if ext := filepath.Ext(name); ext == ".ts" || ext == ".js" || ext == ".tsx" || ext == ".jsx" {
			out = append(out, Spec{Name: strings.TrimSuffix(name, ext), Path: path, Source: d.Source})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func isDirLink(path string, e fs.DirEntry) bool {
	if e.Type()&fs.ModeSymlink == 0 {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func samePath(a, b string) bool {
	ca, err1 := filepath.EvalSymlinks(a)
	cb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ca == cb
}

// approvals is extension-approvals.json: the hash of the code approved
// for each project extension, by the entry file's absolute path.
type approvals = approval.Decisions

func loadApprovals() approvals {
	a, _ := fsutil.ReadJSON[approvals](config.ExtensionApprovalsPath())
	return a
}

// approved reports whether the project extension at path was approved
// with exactly this code.
func approved(path, code string) bool {
	return ApprovalOf(Spec{Path: path, Source: Project}, hash(code)) == mcp.Approved
}

// ErrNotFound is returned by Approve for a name no extension has.
var ErrNotFound = errors.New("no such extension")

// Approve records approval of the project extension called name, for a
// session in cwd, as its code is now; a later change needs approval
// again. It returns the extension approved. User extensions need none.
func Approve(cwd, name string) (Spec, error) {
	if !Supported {
		return Spec{}, fmt.Errorf("%s", UnsupportedMessage(IgnoredCount(Inspect(cwd))))
	}
	for _, s := range Discover(cwd) {
		if s.Name != name {
			continue
		}
		if s.Source != Project {
			return s, fmt.Errorf("%s is a %s extension (%s); those need no approval", name, s.Source, s.Path)
		}
		code, err := Bundle(s.Path)
		if err != nil {
			return s, err
		}
		return s, SetApproval(s, hash(code), true)
	}
	return Spec{}, fmt.Errorf("%w %q (see atto extensions)", ErrNotFound, name)
}

// ApprovalOf reports the decision for this project extension's bundled hash.
// User and built-in extensions need no approval.
func ApprovalOf(s Spec, codeHash string) mcp.Approval {
	if s.Source != Project {
		return mcp.Approved
	}
	if codeHash == "" {
		return mcp.Pending
	}
	return loadApprovals().Of(approval.Path(s.Path), codeHash)
}

// SetApproval records a decision for exactly the code the user reviewed.
// A later change to its bundle needs approval again.
func SetApproval(s Spec, codeHash string, allow bool) error {
	if s.Source != Project {
		return fmt.Errorf("%s is a %s extension; those need no approval", s.Name, s.Source)
	}
	if codeHash == "" {
		return fmt.Errorf("%s has no bundled code to approve", s.Name)
	}
	return editApprovals(func(a *approvals) { a.Set(approval.Path(s.Path), codeHash, allow) })
}

// Revoke forgets the decision for a project extension.
func Revoke(s Spec) error {
	if s.Source != Project {
		return fmt.Errorf("%s is a %s extension; those need no approval", s.Name, s.Source)
	}
	return editApprovals(func(a *approvals) { a.Forget(approval.Path(s.Path)) })
}

func editApprovals(edit func(*approvals)) error {
	return fsutil.EditJSON(config.ExtensionApprovalsPath(), func(a *approvals) { a.Init(); edit(a) })
}

// RevokeProject forgets decisions for this project's extensions, including
// entries that were removed from the repository since they were approved.
func RevokeProject(cwd string) error {
	prefix := approval.Path(config.ProjectExtensionsDir(agent.ProjectRoot(cwd))) + string(filepath.Separator)
	return editApprovals(func(a *approvals) { a.ForgetPrefix(prefix) })
}
