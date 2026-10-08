// token-speed: shows how fast the model generated its last response in the
// status line, e.g. "42 tok/s · 1.3s to first token".
//
// Copy this file to ~/.atto/extensions/ and run /reload. The rate is output
// tokens over the time from the first streamed output to the end of the
// stream, so prompt processing (time to first token) is shown apart. A
// provider that reports no usage shows nothing.

export default function (atto: Atto) {
  atto.on("step_end", (e, ctx) => {
    if (e.outputTokens <= 0 || e.genMs <= 0) return;
    const rate = (e.outputTokens / e.genMs) * 1000;
    const ttft = (e.ttftMs / 1000).toFixed(1);
    ctx.ui.setStatus("token-speed", `${rate.toFixed(rate < 10 ? 1 : 0)} tok/s · ${ttft}s to first token`);
  });
}
