//go:build !noext

package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/ui"
)

func pane(h *fakeHost, id string) *ui.Instance {
	for _, i := range h.uiRegistry().Snapshot().Instances {
		if i.ID == id {
			return &i
		}
	}
	return nil
}
func treeText(i *ui.Instance) string {
	if i == nil || i.Tree == nil {
		return ""
	}
	return ui.PlainText(*i.Tree)
}
func TestJSXCounterRoutingAndReload(t *testing.T) {
	dir, cwd := env(t)
	source, err := os.ReadFile("../examples/extensions/counter.tsx")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "counter.tsx")
	write(t, path, string(source))
	h := newHost(true)
	m := load(t, cwd, h)
	if in := info(t, m, "counter"); in.Status != Loaded {
		t.Fatal(in)
	}
	m.SessionStart("startup")
	m.RunCommandFrom("counter", "", "client-a")
	eventually(t, "counter pane", func() bool { return treeText(pane(h, "counter/counter")) == "Count: 0\n[ Add one ]" })
	i := pane(h, "counter/counter")
	if i.Options.FocusClientID != "client-a" {
		t.Fatal("focus not addressed", i.Options)
	}
	raw, _ := json.Marshal(i.Tree)
	if strings.Contains(string(raw), "onPress") || strings.Contains(string(raw), "function") {
		t.Fatal(string(raw))
	}
	action := ui.Action{Site: ui.Pane, ID: i.ID, Key: "counter/more", Type: ui.Press, Rev: i.Rev, ClientID: "client-b", Surface: "terminal"}
	if err := h.uiRegistry().Route(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if err := h.uiRegistry().Route(context.Background(), action); err == nil {
		t.Fatal("duplicate callback accepted")
	}
	eventually(t, "updated counter", func() bool { return strings.Contains(treeText(pane(h, i.ID)), "Count: 1") })
	value, _ := h.store.Do("counter", "get", "count", nil)
	if string(value) != "1" {
		t.Fatal(string(value))
	}
	old := pane(h, i.ID)
	write(t, path, `export default (atto:any)=>{atto.registerCommand("fresh",{handler:()=>atto.ui.notify("fresh")})}`)
	m.Reload()
	eventually(t, "reload closes pane", func() bool { return pane(h, i.ID) == nil })
	if err := h.uiRegistry().Route(context.Background(), ui.Action{Site: ui.Pane, ID: i.ID, Key: "counter/more", Type: ui.Press, Rev: old.Rev}); err == nil {
		t.Fatal("retired callback accepted")
	}
}
func TestUIConstructorsJSXFragmentAndCallbacks(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "catalog.jsx"), `export default function(atto){
 atto.ui.render({site:"pane",id:"catalog"},e=>{
 const {Box,Text,Markdown,Code,Diff,Link,Button,Input,Select,List,Table,Progress,Collapse,Image}=atto.ui.resolve(e);
 return <Box gap={1}><><Text bold text="hello"> world</Text><Markdown text="**md**" /></>
 <Code source="x" language="go"/><Diff source="+yes"/><Link href="https://example.org"/>
 <Button key="press" label="Press" onPress={e=>atto.ui.notify(e.surface+":"+e.clientId)}/>
 <Input key="input" onInput={(v,e)=>atto.ui.notify("input:"+v)} onSubmit={(v,e)=>atto.ui.notify("submit:"+v)}/>
 <Select key="select" options={[{value:"a",label:"A"}]} onSelect={(v,e)=>atto.ui.notify("select:"+v)}/>
 <List rows={[{key:"one",cells:["one"]}]}/><Table columns={[{label:"Col"}]} rows={[{key:"r",cells:["cell"]}]}/>
 <Progress value={0.5}/><Collapse key="details" title="Details"><Text text="inside"/></Collapse><Image resource="img-1" alt="image"/>
 </Box>});atto.registerCommand("catalog",{handler:()=>atto.ui.open({site:"pane",id:"catalog"})});}`)
	h := newHost(true)
	m := load(t, cwd, h)
	m.RunCommand("catalog", "")
	eventually(t, "catalog tree", func() bool { return strings.Contains(treeText(pane(h, "catalog/catalog")), "hello world") })
	i := pane(h, "catalog/catalog")
	if err := ui.Validate(ui.Pane, *i.Tree); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		key   string
		kind  ui.EventType
		value *string
		want  string
	}{{"press", ui.Press, nil, "terminal:c"}, {"input", ui.InputEvent, new("draft"), "input:draft"}, {"input", ui.Submit, new("final"), "submit:final"}, {"select", ui.SelectEvent, new("a"), "select:a"}} {
		i = pane(h, i.ID)
		err := h.uiRegistry().Route(context.Background(), ui.Action{Site: ui.Pane, ID: i.ID, Key: "catalog/" + test.key, Type: test.kind, Value: test.value, Rev: i.Rev, ClientID: "c", Surface: "terminal"})
		if err != nil {
			t.Fatal(err)
		}
		want := test.want
		eventually(t, "callback "+want, func() bool { return strings.Contains(strings.Join(h.snapshot().notices, "\n"), want) })
	}
}

func TestRenderNextOverrideAndIndividualDisposal(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "wrap.ts"), `export default function(atto:any){
 const dispose=atto.ui.render({site:"toolCall"},async(e:any,next:any)=>{const{Box,Text}=atto.ui.resolve(e);const original=await next({...e,props:{...e.props,description:"Checking…"}});if(original.props!==undefined)throw Error("next exposed wire data");return Box({children:[original,Text({text:"reviewed"})]})});
 atto.registerCommand("dispose",{handler:()=>dispose()});}`)
	h := newHost(true)
	m := load(t, cwd, h)
	r := h.uiRegistry()
	eventually(t, "registration", func() bool { return r.HasRenderer(ui.Match{Site: ui.ToolCall}) })
	n, _, err := r.DrawItem(ui.ToolCall, "item-1", map[string]any{"description": "native", "status": "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if n == nil || n.Type != "Box" || n.Children[0].Type != "engine" || n.Children[0].Props["overrides"].(map[string]any)["description"] != "Checking…" {
		t.Fatalf("%+v", n)
	}
	m.RunCommand("dispose", "")
	eventually(t, "dispose", func() bool { return !r.HasRenderer(ui.Match{Site: ui.ToolCall}) })
}
func TestUIOpenCloseInvalidateToastAndStore(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "api.ts"), `export default function(atto:any){let count=0;
 atto.ui.render({site:"pane",id:"p"},async(e:any)=>atto.ui.resolve(e).Text({text:String(await atto.store.get("n"))}));
 atto.registerCommand("go",{handler:async()=>{
 await atto.store.set("n",{array:[null,true,"text",3]});if((await atto.store.keys()).join()!=="n")throw Error("keys");
 await atto.store.set("n",1);await atto.ui.open({site:"pane",id:"p",title:"Pane"});
 await atto.store.set("n",2);atto.ui.invalidate();await atto.ui.toast("hello",{level:"warning",timeoutMs:500});atto.ui.notify("done");}});
 atto.registerCommand("close",{handler:async()=>{await atto.ui.close({site:"pane",id:"p"});await atto.store.delete("n");atto.ui.notify(String(await atto.store.get("n")))}});
 }`)
	h := newHost(true)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(h.snapshot())
			b, _ := os.ReadFile(config.ExtensionLogPath())
			t.Log(string(b))
		}
	})
	m := load(t, cwd, h)
	m.RunCommand("go", "")
	eventually(t, "invalidated store render", func() bool { return treeText(pane(h, "api/p")) == "2" })
	found := false
	for _, i := range h.uiRegistry().Snapshot().Instances {
		if i.Site == ui.Toast {
			found = true
			if i.Options.Level != "warning" || treeText(&i) != "hello" {
				t.Fatal(i)
			}
		}
	}
	if !found {
		t.Fatal("toast missing")
	}
	eventually(t, "toast expiry", func() bool {
		for _, i := range h.uiRegistry().Snapshot().Instances {
			if i.Site == ui.Toast {
				return false
			}
		}
		return true
	})
	m.RunCommand("close", "")
	eventually(t, "closed", func() bool {
		return pane(h, "api/p") == nil && strings.Contains(strings.Join(h.snapshot().notices, "\n"), "undefined")
	})
}
func TestRenderTimeoutAndSideEffectsFallback(t *testing.T) {
	for _, body := range []string{`while(true){}`, `atto.store.set("x",1);return null`, `return Promise.resolve().then(()=>atto.store.set("x",1))`, `atto.fs.readFile("x");return null`, `return {type:"engine",props:{}}`, `const a:any[]=[];a.push(a);return atto.ui.resolve({}).Box({children:a})`, `return new Promise(()=>{})`} {
		t.Run(body, func(t *testing.T) {
			dir, cwd := env(t)
			write(t, filepath.Join(dir, "bad.ts"), `export default (atto:any)=>{atto.ui.render({site:"toolCall"},()=>{`+body+`});atto.registerCommand("alive",{handler:()=>atto.ui.notify("alive")})}`)
			h := newHost(true)
			m := load(t, cwd, h)
			r := h.uiRegistry()
			eventually(t, "registration", func() bool { return r.HasRenderer(ui.Match{Site: ui.ToolCall}) })
			start := time.Now()
			n, _, err := r.DrawItem(ui.ToolCall, "one", map[string]any{"description": "native"})
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > 500*time.Millisecond {
				t.Fatal("render blocked")
			}
			if n != nil {
				t.Fatalf("not native fallback: %+v", n)
			}
			m.RunCommand("alive", "")
			eventually(t, "runtime survives UI timeout", func() bool { return strings.Contains(strings.Join(h.snapshot().notices, "\n"), "alive") })
			value, _ := h.store.Do("bad", "get", "x", nil)
			if len(value) > 0 {
				t.Fatal("render wrote store")
			}
		})
	}
}
func TestStoreLimitsAndJSON(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "limits.ts"), `export default(atto:any)=>atto.registerCommand("limits",{handler:async()=>{
 for(const v of [undefined,()=>0,{f:()=>0},{u:undefined},"x".repeat(65536),NaN]){try{await atto.store.set("x",v);throw Error("accepted invalid")}catch(e){if(String(e).includes("accepted invalid"))throw e}}
 await atto.store.set("valid",null);if(await atto.store.get("valid")!==null)throw Error("null");atto.ui.notify("limits ok")}})`)
	h := newHost(true)
	m := load(t, cwd, h)
	m.RunCommand("limits", "")
	eventually(t, "JSON limits", func() bool { return strings.Contains(strings.Join(h.snapshot().notices, "\n"), "limits ok") })
	var store MemoryStore
	for i := range 16 {
		_, err := store.Do("a", "set", strings.Repeat("k", i+1), json.RawMessage(`"`+strings.Repeat("x", 64000)+`"`))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Do("a", "set", "overflow", json.RawMessage(`"`+strings.Repeat("x", 64000)+`"`)); err == nil {
		t.Fatal("1 MiB limit")
	}
}

func TestAsyncActionFocusContextAndCallbackErrorToast(t *testing.T) {
	dir, cwd := env(t)
	write(t, filepath.Join(dir, "focus.ts"), `export default(atto:any)=>{
 atto.ui.render({site:"pane",id:"p"},e=>atto.ui.resolve(e).Button({key:"fail",label:"Fail",onPress:async()=>{await null;throw Error("button broke")}}));
 atto.registerCommand("focus",{handler:async()=>{await atto.store.set("x",1);await atto.ui.open({site:"pane",id:"p",focus:true})}})}`)
	h := newHost(true)
	m := load(t, cwd, h)
	m.RunCommandFrom("focus", "", "client-a")
	eventually(t, "async focus", func() bool {
		i := pane(h, "focus/p")
		return i != nil && i.Options.FocusClientID == "client-a" && i.Tree != nil
	})
	i := pane(h, "focus/p")
	if err := h.uiRegistry().Route(context.Background(), ui.Action{Site: ui.Pane, ID: i.ID, Key: "focus/fail", Type: ui.Press, Rev: i.Rev}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "callback error toast", func() bool {
		for _, i := range h.uiRegistry().Snapshot().Instances {
			if i.Site == ui.Toast && strings.Contains(treeText(&i), "button broke") {
				return true
			}
		}
		return false
	})
}
