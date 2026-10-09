//go:build !noext

package extensions

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dop251/goja"

	"github.com/sebastianrcnt/atto/mcp"
)

// SetMCP gives extensions the session's MCP servers (atto.mcp): the same
// manager "atto mcp" in the agent's shell reaches, so a server started by
// one is the server the other uses.
func (m *Manager) SetMCP(b mcp.Backend) {
	m.mu.Lock()
	m.mcp = b
	m.mu.Unlock()
}

func (m *Manager) mcpBackend() mcp.Backend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mcp
}

// mcpObject is atto.mcp: call(server, tool, args?) resolves to
// {text, isError, content, structured?} and rejects when the call could
// not be made (no such server or tool, not approved, failed to start);
// tools(server?) resolves to [{server, name, description, inputSchema}].
// Both are Promises. Calls end when the extension is unloaded.
func (e *ext) mcpObject() *goja.Object {
	vm := e.vm
	o := vm.NewObject()
	backend := func() (mcp.Backend, error) {
		b := e.m.mcpBackend()
		if b == nil {
			return nil, errors.New("MCP is not available in this session")
		}
		return b, nil
	}
	ctx := func() (context.Context, context.CancelFunc) {
		c, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-e.stop:
				cancel()
			case <-c.Done():
			}
		}()
		return c, cancel
	}
	// js turns a Go value into a JavaScript one by way of JSON, so the
	// extension gets plain objects and arrays.
	js := func(v any) (goja.Value, error) {
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var plain any
		if err := json.Unmarshal(data, &plain); err != nil {
			return nil, err
		}
		return vm.ToValue(plain), nil
	}
	_ = o.Set("call", func(server, tool string, args goja.Value) goja.Value {
		e.readOnlyRender()
		var raw json.RawMessage
		if args != nil && !goja.IsUndefined(args) && !goja.IsNull(args) {
			data, err := json.Marshal(args.Export())
			if err != nil {
				return e.rejected(err)
			}
			raw = data
		}
		b, err := backend()
		if err != nil {
			return e.rejected(err)
		}
		return e.async(func() (func() goja.Value, error) {
			c, cancel := ctx()
			defer cancel()
			res, err := b.Call(c, server, tool, raw)
			if err != nil {
				return nil, err
			}
			return func() goja.Value {
				v, err := js(res)
				if err != nil {
					panic(vm.NewGoError(err))
				}
				return v
			}, nil
		})
	})
	_ = o.Set("tools", func(c goja.FunctionCall) goja.Value {
		e.readOnlyRender()
		server := ""
		if a := c.Argument(0); !goja.IsUndefined(a) && !goja.IsNull(a) {
			server = a.String()
		}
		b, err := backend()
		if err != nil {
			return e.rejected(err)
		}
		return e.async(func() (func() goja.Value, error) {
			cx, cancel := ctx()
			defer cancel()
			var tools []mcp.ToolInfo
			var err error
			if server == "" {
				tools, _, err = b.AllTools(cx)
			} else {
				tools, err = b.Tools(cx, server)
			}
			if err != nil {
				return nil, err
			}
			return func() goja.Value {
				out := make([]map[string]any, 0, len(tools))
				for _, t := range tools {
					m := map[string]any{"server": t.Server, "name": t.Name, "description": t.Description}
					var schema any
					if json.Unmarshal(t.InputSchema, &schema) == nil {
						m["inputSchema"] = schema
					}
					out = append(out, m)
				}
				v, err := js(out)
				if err != nil {
					panic(vm.NewGoError(err))
				}
				return v
			}, nil
		})
	})
	return o
}
