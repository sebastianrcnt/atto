// translate-thinking: shows each reasoning block in Korean, translated by a
// small local model. Display only: the model keeps reading its own words,
// and ctrl+o (or the line under a block) flips back to the original.
//
// To use it, copy this file to ~/.atto/extensions/ and add the translating
// model to ~/.atto/models.json, for example LM Studio on its default port:
//
//   "lmstudio": {
//     "baseUrl": "http://127.0.0.1:1234/v1", "api": "openai-completions",
//     "models": [{"id": "google/gemma-4-e4b", "contextWindow": 131072, "maxTokens": 4096}]
//   }
//
// Then change MODEL and LANGUAGE below as you like and run /reload. A small
// model is enough: gemma-4-e4b takes about 1.5 s per block. Local servers
// answer one request at a time, and batching or parallel requests made them
// slower, so this sends one block at a time.

const MODEL = "lmstudio/google/gemma-4-e4b";
const LANGUAGE = "Korean";

const SYSTEM =
  `Translate the user's text into natural ${LANGUAGE}. Keep code, identifiers, ` +
  `file paths, commands and error messages exactly as they are. Output only the translation.`;

export default function (atto: Atto) {
  atto.setCompleteConcurrency(1); // the local server answers one request at a time

  const translated = new Map<string, string>();
  atto.ui.render({site:"assistantMessage"}, (e,next) => {
    const text = e.props.blockId ? translated.get(e.props.blockId) : undefined;
    return text && e.props.kind === "reasoning"
      ? next({...e,props:{...e.props,text}}) : next(e);
  });
  atto.on("reasoning_end", async (e, ctx) => {
    const text = e.text.trim();
    if (!text) return;
    try {
      const { text: out } = await atto.complete({
        model: MODEL,
        system: SYSTEM,
        prompt: text,
        reasoningEffort: "none", // thinking would cost 4x the tokens
        maxTokens: Math.min(1000, Math.ceil(text.length / 2) + 64),
        timeoutMs: 60000,
      });
      translated.set(e.blockId, out.trim());
 while (translated.size > 200) translated.delete(translated.keys().next().value!);
 atto.ui.invalidate({site:"assistantMessage"});
    } catch (err) {
      ctx.ui.notify("Translation failed", "warning");
      atto.log(`translate-thinking: ${err}`);
    }
  });
}
