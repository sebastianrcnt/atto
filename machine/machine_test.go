package machine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newMachine(t *testing.T) (*Machine, string) {
	t.Helper()
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# demo\nhello world\n")
	write("src/main.go", "package main\n\n// TODO: greet\nfunc main() {}\n")
	write("src/util.go", "package main\n\nfunc add(a, b int) int { return a + b }\n")
	write(".hidden", "secret-ish\n")
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, root
}

func run(t *testing.T, m *Machine, code string) string {
	t.Helper()
	out, err := m.Run(context.Background(), code)
	if err != nil {
		t.Fatalf("%s: %v\n%s", code, err, out)
	}
	return out
}

func TestCommands(t *testing.T) {
	m, _ := newMachine(t)
	cases := []struct{ code, want string }{
		{`pwd()`, "/\n"},
		{`ls()`, "README.md\nsrc/\n"},
		{`ls("-a")`, ".hidden\nREADME.md\nsrc/\n"},
		{`cat("README.md")`, "# demo\nhello world\n"},
		{`head("src/main.go", 1)`, "package main\n"},
		{`tail("-n", "1", "src/main.go")`, "func main() {}\n"},
		{`lines("src/main.go", 3, 4)`, "     3  // TODO: greet\n     4  func main() {}\n"},
		{`grep("TODO")`, "src/main.go:3:// TODO: greet\n"},
		{`grep("-l", "package")`, "src/main.go\nsrc/util.go\n"},
		{`find(".", "*.go")`, "src/main.go\nsrc/util.go\n"},
		{`cd("src") pwd() ls()`, "/src\nmain.go\nutil.go\n"},
		{`return #fs.find("/", "*.go")`, "2\n"},
		{`local n = 0 for l in fs.lines("src/main.go") do n = n + 1 end return n`, "4\n"},
		{`return fs.grep("add", "src")[1].line`, "3\n"},
		{`return fs.exists("nope"), fs.exists("README.md")`, "false\ntrue\n"},
	}
	for _, c := range cases {
		m2, _ := newMachine(t)
		_ = m
		if got := run(t, m2, c.code); got != c.want {
			t.Errorf("%s:\ngot  %q\nwant %q", c.code, got, c.want)
		}
	}
}

// Nothing reaches outside the root: not .., not an absolute path, not a
// link, and none of the libraries that touch the host.
func TestSandbox(t *testing.T) {
	m, root := newMachine(t)
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{
		`cat("../outside.txt")`,
		`cat("/../outside.txt")`,
		`cd("..")`,
		`cat("link")`,
		`os.execute("true")`,
		`io.open("/etc/passwd")`,
		`require("os")`,
		`load("return 1")`,
		`dofile("/etc/passwd")`,
	} {
		out, err := m.Run(context.Background(), code)
		if err == nil {
			t.Errorf("%s ran: %q", code, out)
		}
		if strings.Contains(out, "no") && strings.Contains(code, "outside") {
			t.Errorf("%s read outside: %q", code, out)
		}
	}
	// "/" is the root, so an absolute path stays inside.
	if got := run(t, m, `cat("/README.md")`); !strings.Contains(got, "hello") {
		t.Errorf("absolute path: %q", got)
	}
}

func TestRunawayCodeStops(t *testing.T) {
	m, _ := newMachine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Run(ctx, `while true do end`); err == nil {
		t.Fatal("an endless loop finished")
	}
}

func TestErrorsKeepOutput(t *testing.T) {
	m, _ := newMachine(t)
	out, err := m.Run(context.Background(), `print("before") cat("missing.txt")`)
	if err == nil || !strings.Contains(err.Error(), "missing.txt: no such file") || out != "before\n" {
		t.Fatalf("out %q err %v", out, err)
	}
}
