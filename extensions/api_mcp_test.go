//go:build !noext

package extensions

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sebastianrcnt/atto/mcp"
	"github.com/sebastianrcnt/atto/mcp/mcptest"
)

func TestMCPAPI(t *testing.T) {
	dir, cwd := env(t)
	url := mcptest.HTTP(t, nil)
	cfg, _ := json.Marshal(mcp.ServerConfig{Type: "http", URL: url})
	write(t, mcp.Path(mcp.ScopeLocal, cwd), `{"mcpServers": {"fake": `+string(cfg)+`}}`)
	mm := mcp.New(mcp.Options{Cwd: cwd, Root: cwd})
	t.Cleanup(func() { mm.Close() })

	write(t, filepath.Join(dir, "m.ts"), `
export default function (atto: any) {
  atto.on("user_prompt", async () => {
    const out: string[] = [];
    const tools = await atto.mcp.tools("fake");
    out.push(tools.length + " " + tools.some((t: any) => t.name === "echo" && t.server === "fake" && t.description.startsWith("Echo") && t.inputSchema.properties.text));
    const all = await atto.mcp.tools();
    out.push(String(all.length));
    const echo = await atto.mcp.call("fake", "echo", { text: "héllo" });
    out.push(echo.text, String(!!echo.isError), echo.content[0].type);
    out.push((await atto.mcp.call("fake", "count")).text, (await atto.mcp.call("fake", "count", {})).text);
    const fail = await atto.mcp.call("fake", "fail");
    out.push(fail.text, String(fail.isError));
    const img = await atto.mcp.call("fake", "image");
    out.push(img.text.split("\n")[1], img.content[1].mimeType);
    for (const bad of [() => atto.mcp.call("nope", "echo"), () => atto.mcp.call("fake", "nope"), () => atto.mcp.tools("nope")]) {
      try { await bad(); out.push("resolved"); } catch (e) { out.push("rejected"); }
    }
    return out.join("|");
  });
}
`)
	m := load(t, cwd, newHost(false))
	m.SetMCP(mm)
	o := m.UserPrompt(context.Background(), "go")
	if want := "6 true|6|héllo|false|text|1|2|it broke|true|[image: image/png, 16 bytes]|image/png|rejected|rejected|rejected"; o.Context != want {
		t.Fatalf("got %q (%q), want %q", o.Context, o.Notices, want)
	}
	// The extension used the session's manager: its server's state is the session's.
	res, err := mm.Call(context.Background(), "fake", "count", nil)
	if err != nil || res.Text != "3" {
		t.Fatalf("count after the extension's calls: %+v %v", res, err)
	}
}

func TestMCPAPIWithoutAManagerRejects(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "m.ts"), `
export default function (atto: any) {
  atto.on("user_prompt", async () => {
    try { await atto.mcp.call("a", "b"); return "resolved"; } catch (e) { return String(e.message); }
  });
}
`)
	m := load(t, cwd, newHost(false))
	if o := m.UserPrompt(context.Background(), "go"); o.Context != "MCP is not available in this session" {
		t.Fatalf("got %q", o.Context)
	}
}
