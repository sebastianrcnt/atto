package machine

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

// Read-only file access, as shell commands that print (ls, cat, head,
// tail, grep, find, wc, stat, pwd, cd) and as the fs table of values
// for programs (fs.read, fs.lines, fs.list, fs.find, fs.grep, fs.stat,
// fs.exists).

const (
	maxRead    = 1 << 20 // bytes cat and fs.read take from one file
	maxMatches = 200     // lines grep shows
	maxFound   = 500     // paths find shows
)

// skipDir are directories find and grep do not walk into.
var skipDir = map[string]bool{".git": true, "node_modules": true}

func (m *Machine) installFS() {
	cmds := map[string]lua.LGFunction{
		"pwd":   m.cmdPwd,
		"cd":    m.cmdCd,
		"ls":    m.cmdLs,
		"cat":   m.cmdCat,
		"head":  m.cmdHead,
		"lines": m.cmdLines,
		"tail":  m.cmdTail,
		"grep":  m.cmdGrep,
		"find":  m.cmdFind,
		"wc":    m.cmdWc,
		"stat":  m.cmdStat,
	}
	for name, f := range cmds {
		m.L.SetGlobal(name, m.L.NewFunction(f))
	}
	t := m.L.NewTable()
	for name, f := range map[string]lua.LGFunction{
		"read":   m.fsRead,
		"lines":  m.fsLines,
		"list":   m.fsList,
		"find":   m.fsFind,
		"grep":   m.fsGrep,
		"stat":   m.fsStat,
		"exists": m.fsExists,
	} {
		t.RawSetString(name, m.L.NewFunction(f))
	}
	m.L.SetGlobal("fs", t)
}

// args splits a command's arguments into flags ("-l") and the rest.
func args(L *lua.LState) (flags map[rune]bool, rest []string) {
	flags = map[rune]bool{}
	for i := 1; i <= L.GetTop(); i++ {
		s := L.CheckString(i)
		if len(s) > 1 && s[0] == '-' && len(rest) == 0 && !strings.ContainsAny(s[1:], "-0123456789") {
			for _, r := range s[1:] {
				flags[r] = true
			}
			continue
		}
		rest = append(rest, s)
	}
	return flags, rest
}

// fail raises a Lua error naming the command.
func fail(L *lua.LState, cmd string, err error) int {
	L.RaiseError("%s: %v", cmd, err)
	return 0
}

func (m *Machine) cmdPwd(L *lua.LState) int {
	m.out.line(m.Pwd())
	return 0
}

func (m *Machine) cmdCd(L *lua.LState) int {
	p := L.OptString(1, "/")
	host, err := m.resolve(p)
	if err != nil {
		return fail(L, "cd", err)
	}
	fi, err := statFile(host, p)
	if err != nil {
		return fail(L, "cd", err)
	}
	if !fi.IsDir() {
		return fail(L, "cd", fmt.Errorf("%s: not a directory", p))
	}
	m.cwd = host
	return 0
}

func (m *Machine) cmdLs(L *lua.LState) int {
	flags, paths := args(L)
	if len(paths) == 0 {
		paths = []string{"."}
	}
	for i, p := range paths {
		ents, err := m.list(p, flags['a'])
		if err != nil {
			return fail(L, "ls", err)
		}
		if len(paths) > 1 {
			if i > 0 {
				m.out.line("")
			}
			m.out.line(p + ":")
		}
		for _, e := range ents {
			name := e.name
			if e.dir {
				name += "/"
			}
			if flags['l'] {
				m.out.line(fmt.Sprintf("%10d  %s  %s", e.size, e.mod, name))
			} else {
				m.out.line(name)
			}
		}
	}
	return 0
}

type entry struct {
	name string
	dir  bool
	size int64
	mod  string
}

func (m *Machine) list(p string, hidden bool) ([]entry, error) {
	host, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	fi, err := statFile(host, p)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []entry{{name: p, size: fi.Size(), mod: fi.ModTime().Format("2006-01-02 15:04")}}, nil
	}
	des, err := os.ReadDir(host)
	if err != nil {
		return nil, err
	}
	var out []entry
	for _, d := range des {
		if !hidden && strings.HasPrefix(d.Name(), ".") {
			continue
		}
		e := entry{name: d.Name(), dir: d.IsDir()}
		if info, err := d.Info(); err == nil {
			e.size, e.mod = info.Size(), info.ModTime().Format("2006-01-02 15:04")
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// read returns a file's text.
func (m *Machine) read(p string) (string, error) {
	host, err := m.resolve(p)
	if err != nil {
		return "", err
	}
	fi, err := statFile(host, p)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s: is a directory", p)
	}
	if fi.Size() > maxRead {
		return "", fmt.Errorf("%s: %d bytes, more than %d; use head, tail or grep", p, fi.Size(), maxRead)
	}
	b, err := os.ReadFile(host)
	if err != nil {
		return "", err
	}
	if binary(b) {
		return "", fmt.Errorf("%s: binary file", p)
	}
	return string(b), nil
}

func binary(b []byte) bool { return bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 }

func (m *Machine) cmdCat(L *lua.LState) int {
	flags, paths := args(L)
	for _, p := range paths {
		s, err := m.read(p)
		if err != nil {
			return fail(L, "cat", err)
		}
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		for i, l := range lines {
			if flags['n'] {
				l = fmt.Sprintf("%6d  %s", i+1, l)
			}
			m.out.line(l)
		}
	}
	return 0
}

// count reads "-n 20" or a bare number among a command's arguments.
func count(rest []string, def int) (int, []string) {
	n := def
	var out []string
	for i := 0; i < len(rest); i++ {
		var v int
		if rest[i] == "-n" && i+1 < len(rest) {
			if _, err := fmt.Sscan(rest[i+1], &v); err == nil {
				n, i = v, i+1
				continue
			}
		}
		if _, err := fmt.Sscan(rest[i], &v); err == nil && strings.Trim(rest[i], "0123456789-") == "" {
			n = v
			if n < 0 {
				n = -n
			}
			continue
		}
		out = append(out, rest[i])
	}
	return n, out
}

func (m *Machine) headTail(L *lua.LState, cmd string, tail bool) int {
	var rest []string
	for i := 1; i <= L.GetTop(); i++ {
		rest = append(rest, L.Get(i).String())
	}
	n, paths := count(rest, 10)
	for _, p := range paths {
		s, err := m.read(p)
		if err != nil {
			return fail(L, cmd, err)
		}
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		if tail {
			lines = lines[max(0, len(lines)-n):]
		} else {
			lines = lines[:min(n, len(lines))]
		}
		for _, l := range lines {
			m.out.line(l)
		}
	}
	return 0
}

// cmdLines prints lines from..to of a file, numbered: sed -n 'from,top'.
func (m *Machine) cmdLines(L *lua.LState) int {
	p := L.CheckString(1)
	from, to := L.OptInt(2, 1), L.OptInt(3, 0)
	s, err := m.read(p)
	if err != nil {
		return fail(L, "lines", err)
	}
	ls := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if to <= 0 || to > len(ls) {
		to = len(ls)
	}
	for i := max(from, 1); i <= to; i++ {
		m.out.line(fmt.Sprintf("%6d  %s", i, ls[i-1]))
	}
	return 0
}

func (m *Machine) cmdHead(L *lua.LState) int { return m.headTail(L, "head", false) }
func (m *Machine) cmdTail(L *lua.LState) int { return m.headTail(L, "tail", true) }

// match is one line grep found.
type match struct {
	file string
	line int
	text string
}

// grep finds the lines matching pattern (a Go regular expression, as
// grep -E) in the files under paths.
func (m *Machine) grep(pattern string, paths []string, ignoreCase bool, limit int) ([]match, bool, error) {
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false, err
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	var out []match
	more := false
	for _, p := range paths {
		err := m.walk(p, func(host, vp string) error {
			b, err := os.ReadFile(host)
			if err != nil || len(b) > 4*maxRead || binary(b) {
				return nil
			}
			sc := bufio.NewScanner(bytes.NewReader(b))
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for n := 1; sc.Scan(); n++ {
				if re.Match(sc.Bytes()) {
					if len(out) == limit {
						more = true
						return fs.SkipAll
					}
					out = append(out, match{vp, n, sc.Text()})
				}
			}
			return nil
		})
		if err != nil {
			return nil, false, err
		}
	}
	return out, more, nil
}

// walk calls f for each file at or under the agent's path p.
func (m *Machine) walk(p string, f func(host, vp string) error) error {
	host, err := m.resolve(p)
	if err != nil {
		return err
	}
	if _, err := statFile(host, p); err != nil {
		return err
	}
	err = filepath.WalkDir(host, func(h string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if h != host && skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return f(h, m.shown(p, host, h))
	})
	if err == fs.SkipAll {
		return nil
	}
	return err
}

// shown is a found file's path as the agent would write it: relative
// when the search started from a relative path.
func (m *Machine) shown(p, base, host string) string {
	if strings.HasPrefix(p, "/") {
		return m.virtual(host)
	}
	rel, err := filepath.Rel(base, host)
	if err != nil {
		return m.virtual(host)
	}
	if rel == "." {
		return p
	}
	return path.Join(p, filepath.ToSlash(rel))
}

func (m *Machine) cmdGrep(L *lua.LState) int {
	flags, rest := args(L)
	if len(rest) == 0 {
		L.RaiseError("grep: usage: grep(pattern, path...)  (flags: -i ignore case, -l files only)")
	}
	ms, more, err := m.grep(rest[0], rest[1:], flags['i'], maxMatches)
	if err != nil {
		return fail(L, "grep", err)
	}
	seen := map[string]bool{}
	for _, x := range ms {
		if flags['l'] {
			if !seen[x.file] {
				seen[x.file] = true
				m.out.line(x.file)
			}
			continue
		}
		m.out.line(fmt.Sprintf("%s:%d:%s", x.file, x.line, x.text))
	}
	if more {
		m.out.line(fmt.Sprintf("[stopped at %d matches]", maxMatches))
	}
	return 0
}

// find lists the files under p whose name matches glob ("" for all).
func (m *Machine) find(p, glob string, limit int) ([]string, bool, error) {
	var out []string
	more := false
	err := m.walk(p, func(host, vp string) error {
		if glob != "" {
			if ok, _ := path.Match(glob, filepath.Base(host)); !ok {
				return nil
			}
		}
		if len(out) == limit {
			more = true
			return fs.SkipAll
		}
		out = append(out, vp)
		return nil
	})
	return out, more, err
}

func (m *Machine) cmdFind(L *lua.LState) int {
	_, rest := args(L)
	p, glob := ".", ""
	for i := 0; i < len(rest); i++ {
		switch {
		case rest[i] == "-name" && i+1 < len(rest):
			glob, i = rest[i+1], i+1
		case strings.ContainsAny(rest[i], "*?["):
			glob = rest[i]
		default:
			p = rest[i]
		}
	}
	files, more, err := m.find(p, glob, maxFound)
	if err != nil {
		return fail(L, "find", err)
	}
	for _, f := range files {
		m.out.line(f)
	}
	if more {
		m.out.line(fmt.Sprintf("[stopped at %d files]", maxFound))
	}
	return 0
}

func (m *Machine) cmdWc(L *lua.LState) int {
	_, paths := args(L)
	for _, p := range paths {
		s, err := m.read(p)
		if err != nil {
			return fail(L, "wc", err)
		}
		m.out.line(fmt.Sprintf("%7d %7d %7d %s", strings.Count(s, "\n"), len(strings.Fields(s)), len(s), p))
	}
	return 0
}

func (m *Machine) cmdStat(L *lua.LState) int {
	_, paths := args(L)
	for _, p := range paths {
		host, err := m.resolve(p)
		if err != nil {
			return fail(L, "stat", err)
		}
		fi, err := statFile(host, p)
		if err != nil {
			return fail(L, "stat", err)
		}
		kind := "file"
		if fi.IsDir() {
			kind = "directory"
		}
		m.out.line(fmt.Sprintf("%s: %s, %d bytes, modified %s", p, kind, fi.Size(), fi.ModTime().Format("2006-01-02 15:04:05")))
	}
	return 0
}

// --- fs: the same as values ---

func (m *Machine) fsRead(L *lua.LState) int {
	s, err := m.read(L.CheckString(1))
	if err != nil {
		return fail(L, "fs.read", err)
	}
	L.Push(lua.LString(s))
	return 1
}

// fsLines is an iterator over a file's lines: for l in fs.lines(p) do … end.
func (m *Machine) fsLines(L *lua.LState) int {
	s, err := m.read(L.CheckString(1))
	if err != nil {
		return fail(L, "fs.lines", err)
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	i := 0
	L.Push(L.NewFunction(func(L *lua.LState) int {
		if i >= len(lines) {
			return 0
		}
		i++
		L.Push(lua.LString(lines[i-1]))
		return 1
	}))
	return 1
}

func (m *Machine) fsList(L *lua.LState) int {
	ents, err := m.list(L.OptString(1, "."), L.OptBool(2, false))
	if err != nil {
		return fail(L, "fs.list", err)
	}
	t := L.NewTable()
	for _, e := range ents {
		r := L.NewTable()
		r.RawSetString("name", lua.LString(e.name))
		r.RawSetString("dir", lua.LBool(e.dir))
		r.RawSetString("size", lua.LNumber(e.size))
		t.Append(r)
	}
	L.Push(t)
	return 1
}

func (m *Machine) fsFind(L *lua.LState) int {
	files, _, err := m.find(L.OptString(1, "."), L.OptString(2, ""), 10*maxFound)
	if err != nil {
		return fail(L, "fs.find", err)
	}
	t := L.NewTable()
	for _, f := range files {
		t.Append(lua.LString(f))
	}
	L.Push(t)
	return 1
}

func (m *Machine) fsGrep(L *lua.LState) int {
	pattern := L.CheckString(1)
	var paths []string
	if p := L.OptString(2, ""); p != "" {
		paths = []string{p}
	}
	ms, _, err := m.grep(pattern, paths, false, 10*maxMatches)
	if err != nil {
		return fail(L, "fs.grep", err)
	}
	t := L.NewTable()
	for _, x := range ms {
		r := L.NewTable()
		r.RawSetString("file", lua.LString(x.file))
		r.RawSetString("line", lua.LNumber(x.line))
		r.RawSetString("text", lua.LString(x.text))
		t.Append(r)
	}
	L.Push(t)
	return 1
}

func (m *Machine) fsStat(L *lua.LState) int {
	p := L.CheckString(1)
	host, err := m.resolve(p)
	if err != nil {
		return fail(L, "fs.stat", err)
	}
	fi, err := statFile(host, p)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	r := L.NewTable()
	r.RawSetString("dir", lua.LBool(fi.IsDir()))
	r.RawSetString("size", lua.LNumber(fi.Size()))
	r.RawSetString("modified", lua.LNumber(fi.ModTime().Unix()))
	L.Push(r)
	return 1
}

func (m *Machine) fsExists(L *lua.LState) int {
	host, err := m.resolve(L.CheckString(1))
	if err == nil {
		_, err = os.Stat(host)
	}
	L.Push(lua.LBool(err == nil))
	return 1
}
