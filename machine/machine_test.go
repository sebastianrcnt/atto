package machine

import (
	"context"
	"strings"
	"testing"
)

func newMachine(t *testing.T) *Machine { t.Helper(); m := New(nil); t.Cleanup(m.Close); return m }
func run(t *testing.T, m *Machine, code string) string {
	t.Helper()
	r, err := m.Run(context.Background(), code)
	if err != nil {
		t.Fatalf("%s: %v", code, err)
	}
	if !r.Pure {
		t.Fatal("pure machine reported impure")
	}
	return r.Output
}
func TestHelpers(t *testing.T) {
	cases := []struct{ code, want string }{
		{`return table.concat(text.split("a::b::", "::"), "|")`, "a|b|\n"},
		{`return #text.lines(""), table.concat(text.lines("a\r\nb\n"), "|")`, "0\na|b\n"},
		{`return text.trim(" \t hi\n")`, "hi\n"},
		{`return table.concat(text.match_all("ab12 cd34", "[0-9]+"), ",")`, "12,34\n"},
		{`local a=text.match_all("ab12 cd34", "([a-z]+)([0-9]+)");return a[1][1],a[2][2]`, "ab\n34\n"},
		{`return json.encode({z=2,a={true,"x"}})`, "{\"a\":[true,\"x\"],\"z\":2}\n"},
		{`return json.encode(json.decode('{"a":[],"b":{},"c":[null,false,3]}'))`, "{\"a\":[],\"b\":{},\"c\":[null,false,3]}\n"},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			if got := run(t, newMachine(t), c.code); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}
func TestDeterminism(t *testing.T) {
	code := `local t={} for i=150,1,-1 do t["key"..i]=i end
 for k,v in pairs(t) do print(k,v) end
 local k=nil repeat k=next(t,k);if k then print(k) end until not k
 print(t, {b={z=2,a=1},a=3})
 for k,v in pairs(math) do print(k,type(v)) end
 for k,v in pairs(_G) do print(k,type(v)) end
 print(tostring({}),tostring(function() end),string.format("%s",{}))`
	want := run(t, newMachine(t), code)
	for range 20 {
		if got := run(t, newMachine(t), code); got != want {
			t.Fatalf("non-deterministic output:\n%s\n---\n%s", want, got)
		}
	}
	// Same code twice on the same persistent VM, for value computations.
	m := newMachine(t)
	code = `local t={} for i=100,1,-1 do t["s"..i]=i end for k,v in pairs(t) do print(k,v) end`
	if a, b := run(t, m, code), run(t, m, code); a != b {
		t.Fatal("same code on same VM differs")
	}
}
func TestNoWorld(t *testing.T) {
	m := newMachine(t)
	for _, name := range []string{"fs", "ls", "cat", "head", "tail", "lines", "grep", "find", "wc", "stat", "pwd", "cd", "os", "io", "require", "load", "loadfile", "loadstring", "dofile", "sys"} {
		if got := run(t, m, `return `+name); got != "nil\n" {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := run(t, m, `return math.random,math.randomseed,string.dump`); got != "nil\nnil\nnil\n" {
		t.Fatal(got)
	}
}
func TestRunawayAndErrors(t *testing.T) {
	m := newMachine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Run(ctx, `while true do end`); err == nil {
		t.Fatal("endless loop finished")
	}
	r, err := m.Run(context.Background(), `print("before") error("failed")`)
	if err == nil || !strings.Contains(err.Error(), "failed") || r.Output != "before\n" {
		t.Fatalf("%+v %v", r, err)
	}
	for _, code := range []string{`local t={} t.x=t;return json.encode(t)`, `return json.encode({[3]=1})`, `return json.decode("no")`, `return text.match_all("s", "[")`} {
		if _, err := m.Run(context.Background(), code); err == nil {
			t.Errorf("accepted %s", code)
		}
	}
	_, err = m.Run(context.Background(), `sys.bash(cmd="ls")`)
	if err == nil || !strings.Contains(err.Error(), `Lua has no named arguments; use sys.bash{cmd = "ls"}`) {
		t.Fatal(err)
	}
}

func TestPairsDeletion(t *testing.T) {
	m := newMachine(t)
	code := `local t={a=1,b=2,c=3};for k,v in pairs(t) do print(k,v);t[k]=nil end;return next(t)==nil`
	if got := run(t, m, code); got != "a\t1\nb\t2\nc\t3\ntrue\n" {
		t.Fatal(got)
	}
}
