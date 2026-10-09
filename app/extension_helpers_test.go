package app

func (a *App) runCommand(text string) { a.submit(text, nil) }

// within polls cond under the UI lock: extensions reach the UI
// asynchronously.

const demoExtension = `
export default function (atto: any) {
  let mode=""; atto.ui.render({site:"status",id:"mode"},e=>atto.ui.resolve(e).Text({text:mode})); atto.on("session_start",async(e:any)=>{mode="demo:"+e.reason;await atto.ui.open({site:"status",id:"mode"})});
  atto.registerCommand("demo", {
    description: "Demo things",
    handler: async (args: string, ctx: any) => {
      atto.ui.render({site:"band",id:"w"},e=>atto.ui.resolve(e).Text({text:"widget "+args})); await atto.ui.open({site:"band",id:"w"});
      const pick = await ctx.ui.select("Pick one", ["red", "green"]);
      const ok = await ctx.ui.confirm("Sure?");
      const name = await ctx.ui.input("Name?");
      ctx.ui.notify("picked " + pick + " " + ok + " " + name, "warning");
    },
  });
  atto.registerCommand("model", { handler: () => ctx.ui.notify("shadowed") });
}
`
