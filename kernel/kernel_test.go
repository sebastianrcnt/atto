package kernel_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"atto2/kernel"
	"atto2/machine"
)

func project(t *testing.T) (*kernel.Kernel, *machine.Machine, string) {
	t.Helper()
	dir := t.TempDir()
	k, err := kernel.Project(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := machine.New(k)
	t.Cleanup(m.Close)
	return k, m, dir
}
func TestFormsAndPurity(t *testing.T) {
	k := kernel.New()
	calls := 0
	err := k.Register(kernel.Syscall{Name: "echo", Fields: []kernel.Field{{Name: "msg", Type: "string", Required: true}, {Name: "count", Type: "number"}, {Name: "flag", Type: "boolean"}}, Call: func(_ context.Context, a kernel.Args) (any, error) { calls++; return a["msg"], nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err = k.Grant("echo"); err != nil {
		t.Fatal(err)
	}
	m := machine.New(k)
	defer m.Close()
	r, err := m.Run(context.Background(), `local b=sys.echo;return b("hi"),b{msg="hi"}`)
	if err != nil || r.Pure || r.Output != "hi\nhi\n" || calls != 2 || len(k.Log) != 2 {
		t.Fatalf("%+v %v calls=%d log=%+v", r, err, calls, k.Log)
	}
	if fmt.Sprint(k.Log[0].Args) != fmt.Sprint(k.Log[1].Args) {
		t.Fatal("forms differ")
	}
	r, err = m.Run(context.Background(), `local b=sys.echo; return 1+2`)
	if err != nil || !r.Pure || k.PureRuns != 1 || k.ImpureRuns != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, c := range []struct{ code, want string }{
		{`sys.echo{msg="hi",other=1}`, "unknown field other; use sys.echo{msg = string"},
		{`sys.echo(1)`, "field msg expected string"},
		{`sys.echo{msg="hi",count="1"}`, "field count expected number"},
		{`sys.echo{msg="hi",flag=0}`, "field flag expected boolean"},
		{`sys.echo{}`, "field msg required (string)"},
		{`sys.echo("hi",1,true,"extra")`, "too many arguments"},
		{`sys.missing()`, "unknown syscall"},
	} {
		before := len(k.Log)
		r, err = m.Run(context.Background(), c.code)
		if err == nil || !strings.Contains(err.Error(), c.want) || r.Pure || len(k.Log) != before+1 {
			t.Fatalf("%s: %+v %v log=%+v", c.code, r, err, k.Log)
		}
	}
	if calls != 2 {
		t.Fatal("malformed call reached handler")
	}
	_, _ = m.Run(context.Background(), `sys.echo{msg=17,unknown="supplied"}`)
	last := k.Log[len(k.Log)-1]
	if last.Args["msg"] != float64(17) || last.Args["unknown"] != "supplied" || last.Time.IsZero() || last.Error == "" {
		t.Fatalf("lost rejected arguments: %+v", last)
	}

}
func TestGrantAndRegistry(t *testing.T) {
	k := kernel.New()
	calls := 0
	s := kernel.Syscall{Name: "secret", Call: func(context.Context, kernel.Args) (any, error) { calls++; return "secret", nil }}
	if err := k.Register(s); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(s); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := k.Grant("missing"); err == nil {
		t.Fatal("unknown grant accepted")
	}
	m := machine.New(k)
	defer m.Close()
	r, err := m.Run(context.Background(), `local alias=sys.secret;return alias()`)
	if err == nil || !strings.Contains(err.Error(), "not granted") || r.Pure || calls != 0 || len(k.Log) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if strings.Contains(k.Instructions(), "secret") {
		t.Fatal("ungranted syscall in instructions")
	}
	if err := k.Grant("secret"); err != nil {
		t.Fatal(err)
	}
	r, err = m.Run(context.Background(), `return sys.secret()`)
	if err != nil || r.Output != "secret\n" || calls != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestExitCannotBeCaught(t *testing.T) {
	for _, code := range []string{
		`print("before");sys.exit{report="done"};print("after");sys.now()`,
		`pcall(function() sys.exit("done") end);print("after");sys.now()`,
		`xpcall(function() sys.exit("done") end,function() print("handler");sys.now() end);print("after")`,
	} {
		k, m, _ := project(t)
		r, err := m.Run(context.Background(), code)
		if err != nil || r.Pure || !k.Exited || k.Report != "done" || len(k.Log) != 1 || strings.Contains(r.Output, "after") || strings.Contains(r.Output, "handler") {
			t.Fatalf("%s: %+v %v kernel=%+v", code, r, err, k)
		}
		if _, err = m.Run(context.Background(), `print("resurrected")`); err == nil {
			t.Fatal("exited agent ran")
		}
	}
}
func TestNow(t *testing.T) {
	k, m, _ := project(t)
	before := time.Now().Unix()
	r, err := m.Run(context.Background(), `return sys.now()`)
	if err != nil || r.Pure || len(k.Log) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	stamp := k.Log[0].Result.(int64)
	if stamp < before || stamp > time.Now().Unix() {
		t.Fatal(stamp)
	}
}
func TestBashSandbox(t *testing.T) {
	k, m, dir := project(t)
	if runtime.GOOS != "darwin" {
		_, err := m.Run(context.Background(), `sys.bash("ls")`)
		if err == nil || !strings.Contains(err.Error(), "only implemented on macOS") {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "sample"), []byte("hello world\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	commands := []struct {
		cmd    string
		code   int
		stdout string
	}{
		{`ls sample; grep hello sample`, 0, "sample\nhello world\n"},
		{`printf forbidden > project-write`, 1, ""},
		{`printf forbidden > escape/outside-write`, 1, ""},
		{`ln -s "$PWD" "$TMPDIR/root"; printf forbidden > "$TMPDIR/root/temp-escape"`, 1, ""},
		{`printf allowed > /dev/null; printf allowed > "$TMPDIR/file"; cat "$TMPDIR/file"`, 0, "allowed"},
	}
	for _, c := range commands {
		r, err := m.Run(context.Background(), `sys.bash{cmd=`+fmt.Sprintf("%q", c.cmd)+`}`)
		if err != nil || r.Pure {
			t.Fatalf("%+v %v", r, err)
		}
		result := k.Log[len(k.Log)-1].Result.(map[string]any)
		if result["code"] != c.code || result["stdout"] != c.stdout {
			t.Fatalf("%s: %+v", c.cmd, result)
		}
	}
	for _, p := range []string{filepath.Join(dir, "project-write"), filepath.Join(dir, "temp-escape"), filepath.Join(outside, "outside-write")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("write escaped: %s %v", p, err)
		}
	}
	_, err := m.Run(context.Background(), `sys.bash{cmd="sleep 5 & wait",timeout=0.1}`)
	if err != nil {
		t.Fatal(err)
	}
	result := k.Log[len(k.Log)-1].Result.(map[string]any)
	if result["code"] != 124 || !strings.Contains(result["stderr"].(string), "timeout after 0.1 seconds") {
		t.Fatal(result)
	}
	_, err = m.Run(context.Background(), `sys.bash{cmd="head -c 70000 /dev/zero; head -c 80000 /dev/zero >&2"}`)
	if err != nil {
		t.Fatal(err)
	}
	result = k.Log[len(k.Log)-1].Result.(map[string]any)
	if !strings.HasSuffix(result["stdout"].(string), "[4464 bytes cut]\n") || !strings.HasSuffix(result["stderr"].(string), "[14464 bytes cut]\n") {
		t.Fatal("output not independently capped")
	}
	_, err = m.Run(context.Background(), `sys.bash{cmd="true",timeout=-1}`)
	if err == nil || !strings.Contains(err.Error(), "field timeout expected positive finite seconds") {
		t.Fatal(err)
	}
}

func TestPureGrant(t *testing.T) {
	k, err := kernel.WithGrant("/does/not/exist", "now", "exit")
	if err != nil {
		t.Fatal(err)
	}
	m := machine.New(k)
	defer m.Close()
	r, err := m.Run(context.Background(), `return rawget(sys,"bash"),rawget(sys,"now")~=nil`)
	if err != nil || r.Output != "nil\ntrue\n" || !r.Pure {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = m.Run(context.Background(), `sys.bash("true")`)
	want := "sys.bash is not granted to this agent"
	if err == nil || err.Error() != want || r.Pure || len(k.Log) != 1 || k.Log[0].Error != want {
		t.Fatalf("%+v %v %+v", r, err, k.Log)
	}
	if _, err := kernel.WithGrant("/does/not/exist", "bash"); err == nil {
		t.Fatal("unchecked bash directory")
	}
}
