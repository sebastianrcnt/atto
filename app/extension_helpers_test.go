package app

func (a *App) runCommand(text string) { a.submit(text, nil) }

// within polls cond under the UI lock: extensions reach the UI
// asynchronously.

const demoExtension = `
export default function (atto: any) {
  atto.on("session_start", (e: any, ctx: any) => ctx.ui.setStatus("mode", "demo:" + e.reason));
  atto.registerCommand("demo", {
    description: "Demo things",
    handler: async (args: string, ctx: any) => {
      ctx.ui.setWidget("w", ["widget " + args]);
      const pick = await ctx.ui.select("Pick one", ["red", "green"]);
      const ok = await ctx.ui.confirm("Sure?");
      const name = await ctx.ui.input("Name?");
      ctx.ui.notify("picked " + pick + " " + ok + " " + name, "warning");
    },
  });
  atto.registerCommand("model", { handler: () => ctx.ui.notify("shadowed") });
}
`
