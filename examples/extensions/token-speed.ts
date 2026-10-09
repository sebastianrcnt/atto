// Generation throughput and time to first token, in a portable status slot.
export default function (atto: Atto) {
  let text = "";
  atto.ui.render({ site: "status", id: "token-speed" }, e =>
    atto.ui.resolve(e).Text({ text }));
  atto.on("step_end", async e => {
    if (e.outputTokens <= 0 || e.genMs <= 0) return;
    const rate = (e.outputTokens / e.genMs) * 1000;
    text = `${rate.toFixed(rate < 10 ? 1 : 0)} tok/s · ${(e.ttftMs / 1000).toFixed(1)}s to first token`;
    await atto.ui.open({ site: "status", id: "token-speed" });
  });
}
