package server

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
)

func TestProtocolReferenceMethods(t *testing.T) {
	doc, err := os.ReadFile("../docs/protocol.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Protocol revision **3**", "`hasMore`", "`before`", "Revision 3: lazy transcript loading", "includeAgents", "includeClosedAgents", "includeArchived", "parentThreadId", "goalWaiting", "durationMs"} {
		if !strings.Contains(string(doc), required) {
			t.Errorf("protocol paging contract missing from docs: %s", required)
		}
	}
	methods := map[string]bool{}
	for name := range threadMethods {
		methods[name] = true
	}
	file, err := parser.ParseFile(token.NewFileSet(), "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Check the top-level dispatcher as well as the actual runtime registry.
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "call" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if clause, ok := n.(*ast.CaseClause); ok {
				for _, expr := range clause.List {
					if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						name, _ := strconv.Unquote(lit.Value)
						methods[name] = true
					}
				}
			}
			return true
		})
	}
	for name := range methods {
		if !strings.Contains(string(doc), "| `"+name+"` |") {
			t.Errorf("registered method %s missing from docs/protocol.md table", name)
		}
	}
	// Also cover notifications emitted by the runtime, not only requests.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := c.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			index := 0
			if sel.Sel.Name == "notify" {
				receiver, ok := sel.X.(*ast.Ident)
				if !ok || receiver.Name != "s" {
					return true
				}
				index = 1
			} else if sel.Sel.Name != "publish" && sel.Sel.Name != "Publish" {
				return true
			}
			if len(c.Args) <= index {
				return true
			}
			lit, ok := c.Args[index].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			method, _ := strconv.Unquote(lit.Value)
			if !strings.Contains(string(doc), "| `"+method+"` |") {
				t.Errorf("notification %s (%s) missing from docs/protocol.md table", method, name)
			}
			return true
		})
	}
	// Every JSON example is valid, independently of prose and formatting.
	for i, block := range strings.Split(string(doc), "```json\n")[1:] {
		raw, _, _ := strings.Cut(block, "```")
		if !json.Valid([]byte(raw)) {
			t.Errorf("invalid JSON example %d: %s", i+1, raw)
		}
	}
}

// These logs are real wire messages, used by docs/protocol.md's lifecycle
// example. Keep the model scripted: no provider credentials or network API.
func TestProtocolExampleTrace(t *testing.T) {
	s, _ := testServer(t, providertest.Reply{Text: "Hello", Words: 2, Delay: 5 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := Connect(ctx, s)
	defer c.Close()
	var init map[string]any
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{2}, "clientInfo": ClientInfo{Name: "example", Version: "1"}}, &init); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": init})
	t.Log(string(raw))
	if err := c.Call(ctx, "initialized", nil, nil); err != nil {
		t.Fatal(err)
	}
	var info ThreadInfo
	if err := c.Call(ctx, "thread/start", nil, &info); err != nil {
		t.Fatal(err)
	}
	var started map[string]any
	if err := c.Call(ctx, "turn/start", map[string]any{"threadId": info.ID, "input": "Say hello"}, &started); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 3, "result": started})
	t.Log(string(raw))
	methods := map[string]bool{}
	for {
		select {
		case n := <-c.Events():
			switch n.Method {
			case "turn/started", "item/started", "item/delta", "item/completed", "turn/completed":
				raw, _ := json.Marshal(n)
				t.Log(string(raw))
				methods[n.Method] = true
			}
			if n.Method == "turn/completed" {
				for _, name := range []string{"turn/started", "item/started", "item/delta", "item/completed"} {
					if !methods[name] {
						t.Errorf("missing example event %s", name)
					}
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("trace timed out")
		}
	}
}
