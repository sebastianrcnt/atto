// /autorename: names the conversation from what it is about, as /name
// would with a name you typed. The current model writes the name, from the
// conversation's latest messages (not the commands and their output), so
// nothing goes to another provider. Run it again for another name, or use
// /name to pick one yourself.

const SYSTEM =
  "You name conversations between a user and a coding assistant. Reply with a title of 3 to 6 words " +
  "that says what the conversation is about, in the language the user writes in. " +
  "No quotes, no punctuation at the end, nothing else.";

// clip keeps a message short enough that many fit in a small request.
const clip = (text: string, n: number) => (text.length > n ? text.slice(0, n) + "…" : text);

// clean turns the model's reply into a name: its first line, without
// quotes, Markdown or a trailing period.
export function clean(reply: string): string {
  let name = reply.trim().split("\n")[0].trim();
  name = name.replace(/^(title|name)\s*:\s*/i, "");
  name = name.replace(/^[#*_`"'“”‘’\s]+|[*_`"'“”‘’.\s]+$/g, "");
  return name.length > 80 ? name.slice(0, 80).trimEnd() : name;
}

export default function (atto: Atto) {
  atto.registerCommand("autorename", {
    description: "Name this conversation from what it is about (the current model writes the name)",
    handler: async (_args, ctx) => {
      const model = ctx.session.model;
      const msgs = ctx.session.messages(30);
      if (!model) return ctx.ui.notify("autorename: no model to ask", "warning");
      if (!msgs.some((m) => m.role === "user")) return ctx.ui.notify("autorename: nothing to name yet", "warning");
      const convo = msgs.map((m) => `${m.role === "user" ? "User" : "Assistant"}: ${clip(m.text, m.role === "user" ? 600 : 300)}`).join("\n\n");
      const current = ctx.session.name;
      const prompt =
        (current ? `The conversation is named "${current}" now.\n\n` : "") +
        `The conversation so far (latest last):\n\n${convo}\n\nTitle:`;
      ctx.ui.setStatus("autorename", "naming…");
      try {
        const ask = (reasoningEffort?: string) =>
          atto.complete({ model, system: SYSTEM, prompt, maxTokens: 64, timeoutMs: 60000, reasoningEffort });
        let reply: { text: string };
        try {
          reply = await ask("none"); // a title needs no thinking
        } catch {
          reply = await ask(); // a model that takes no "none"
        }
        const name = clean(reply.text);
        if (!name) return ctx.ui.notify("autorename: the model gave no name", "warning");
        ctx.session.setName(name);
        ctx.ui.notify(`Named this conversation "${name}" (by ${model}).`);
      } catch (err) {
        ctx.ui.notify(`autorename: ${err instanceof Error ? err.message : err}`, "error");
      } finally {
        ctx.ui.setStatus("autorename", null);
      }
    },
  });
}
