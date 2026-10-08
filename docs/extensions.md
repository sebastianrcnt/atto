# Writing atto extensions

An extension is a TypeScript or JavaScript file that atto loads into every
session. It can watch and steer the agent's shell commands, add to prompts,
add slash commands, and show things in the TUI. atto compiles it with esbuild
and runs it in an embedded JavaScript engine (goja); there is no Node.js, so
`require`, `process` and npm modules that need Node are not available.

`atto extensions docs` prints this file; `atto extensions source diff` prints the source of the built-in `/diff`, a small example; `atto extensions types` prints
`atto.d.ts`, the API's type declarations.

## Where they live

| Path | Source |
| --- | --- |
| `~/.atto/extensions/<name>.ts` or `.js` | user |
| `~/.atto/extensions/<name>/index.ts` or `index.js` | user (a folder: other files in it can be imported) |
| `<project>/.atto/extensions/...` (same shapes) | project |
| inside atto (`extensions/builtin/<name>.ts` in the source) | builtin |

`<project>` is the nearest directory above the working directory that holds
`.git`. The name is the file or folder name. Files ending in `.d.ts` and
names starting with `.` are ignored.

**Project extensions need approval**, since a repository brings them: run
`/extensions approve <name>` in the TUI or `atto extensions approve <name>`.
The approval covers the code as it is (the bundle's hash, including the
files it imports); any change needs approval again. An agent cannot approve
from its shell. User extensions need no approval.

**Built-in extensions** ship inside the atto binary and are written against
this same public API. There are two: `/diff`, which shows what changed
in the session's working tree (`/diff [--staged] [path]`), and `/autorename`,
which has the session's own model name the conversation from its latest
messages
([`extensions/builtin/autorename.ts`](../extensions/builtin/autorename.ts),
an example of `atto.complete` and `ctx.session`). They need no
approval, are listed as `builtin` in the Loaded block and in `atto extensions`,
and can be turned off like any other (`"disabled": ["diff"]`). A user or
project extension with the same name replaces it, so the way to change
`/diff` is to copy its source,
[`extensions/builtin/diff.ts`](../extensions/builtin/diff.ts), to
`~/.atto/extensions/diff.ts`. It is a good small example: it runs `git` with
`atto.exec`, parses the output, and shows it with `ctx.ui.showText`.

To turn one off, add its name to `settings.json`:

```json
{ "extensions": { "disabled": ["noisy"], "timeout": 5 } }
```

## Load, reload, debug

- Extensions load when a session starts. After editing one, run `/reload`
  (or `atto reload` from the agent's shell): the old runtime is disposed of
  (`onDispose` callbacks run, timers stop, its status items and widgets go)
  and the new code runs. The reload report lists `changed extension <name>`.
- The "Loaded" block lists every extension: loaded (with its commands and
  events), failed (with the error, `file:line:col` for syntax errors, the
  source line for exceptions), needs approval, or disabled. After `atto
  reload`, failures also come back to the agent in the `[atto event]`.
- `atto extensions` lists them without running any; `atto context` too.
- `atto.log(...)` and `console.log(...)` append to `~/.atto/extensions.log`,
  as do errors from handlers.

## The shape

```ts
/// <reference path="./atto.d.ts" />
export default function (atto: Atto) {
  atto.on("tool_call", (e) => {
    if (/\bgit\s+push\b.*--force/.test(e.command)) {
      return { block: true, reason: "force-push is not allowed here" };
    }
  });

  atto.registerCommand("todo", {
    description: "Count TODOs in the project",
    handler: async (args, ctx) => {
      const r = await atto.exec("git grep -c TODO || true");
      ctx.ui.notify(r.stdout.trim() || "no TODOs");
    },
  });
}
```

atto writes `atto.d.ts` next to your extensions when it loads them; the
`reference` line gives editors the types. Types are erased, not checked.
The default export may be `async`; atto waits for it (up to the timeout).
Modern syntax works: esbuild lowers it to what the engine runs (ES2017,
plus async/await). Relative imports are bundled into one script.

## Events

`atto.on(event, handler)`; the handler gets `(event, ctx)` and may return a
Promise. Several handlers of an event run in registration order, and
extensions in load order (user before project, then by name).

| Event | Payload | Return |
| --- | --- | --- |
| `session_start` | `{reason}`: `startup`, `resume`, `clear` | ignored |
| `session_end` | `{reason}`: `exit`, `clear`, `resume`, `other` | ignored; atto waits up to 2 s in all |
| `turn_start` | `{prompt}` | ignored |
| `turn_end` | `{error: string \| null, aborted}` | ignored |
| `user_prompt` | `{prompt}` | a string (or `{context}`) to add to the prompt; `{block: true, reason}` rejects it |
| `tool_call` | `{toolName, command, description, timeout, background}` | `{block: true, reason}` (the model gets the reason), `{command}` to run another command |
| `tool_result` | `{toolName, command, description, output, exitCode, timedOut, canceled, durationMs, job}` | a string (or `{output}`) to replace what the model receives |
| `message_end` | `{blockId, text, model}`: an assistant text block finished | ignored |
| `reasoning_end` | `{blockId, text, model}`: a reasoning block finished | ignored |
| `step_end` | `{model, promptTokens, cachedTokens, outputTokens, cost, contextTokens, ttftMs, genMs}`: a model response finished (one per request in a turn); `ttftMs` runs from sending the request to the first streamed output, `genMs` from there to the end | ignored |

`tool_call`, `tool_result` and `user_prompt` are waited for; the others are
not. `message_end`, `reasoning_end` and `step_end` never delay anything, not even a
handler that takes minutes (a model call, say): they have no timeout, and
a handler that throws or rejects is reported like any other. A later handler sees what an earlier one changed (a rewritten command,
a redacted output).

**Order with Claude Code hooks.** Extensions run inside the hooks:
`UserPromptSubmit` hooks, then `user_prompt`; `PreToolUse` hooks, then
`tool_call`, the command, `tool_result`, then `PostToolUse` hooks (which
see the rewritten output, so a redaction reaches them too). A hook that
blocks wins before extensions see the call.

## Blocks: events, display-only changes and side model calls

When the model's response is complete and saved, atto fires `message_end`
for its text (one block) and `reasoning_end` for its thinking (another),
in the TUI, in `atto -p` and in the server alike. `model` is `provider/id`.
`blockId` names the block for as long as the session exists: it is built
from the session entry of the response, so it is the same after a resume,
and it cannot clash with a block of another session. (Events fire when the
response ends, so `reasoning_end` comes with `message_end`, not when the
thinking stops.) A response with no text, or no thinking, fires nothing
for it.

An extension can change how a block is shown, never what the model sees:

- `ctx.ui.setBlockStatus(blockId, text | null)`: a short dim suffix on the
  block's header ("translating…", "failed"). Each extension has its own.
- `ctx.ui.setBlockDisplay(blockId, text | null)`: shows `text` (Markdown)
  in place of the block's own text; `null` restores it. atto adds a line
  under the block, `· shown: <extension> (click or ctrl+o to show original)`:
  a click on that line flips that block between the replacement and the
  original, and ctrl+o flips all of them. (Clicking the header still
  expands and collapses a reasoning block.) The latest extension to set a
  text owns it; only the owner can restore the original.

The model's context and the session's messages are never changed:
requests are built from what the model wrote, and "copy last answer"
copies the original. What the extension showed is saved in the session
file as `block_display` entries (the status and text as last set), so a
resumed session shows it without the extension running again; a result for
a block that no longer exists (the session was switched meanwhile) is
ignored, and the view does not jump when a block above it changes height.
The web client (`atto serve`, and `/remote` from the TUI) shows them too,
with a "show original" link under a replaced block; the server saves them
the same way. In `atto -p` they do nothing and nothing is saved. Example,
in a few lines:

```ts
export default function (atto: Atto) {
  atto.on("reasoning_end", (e, ctx) => {
    ctx.ui.setBlockDisplay(e.blockId, e.text.toUpperCase());
    ctx.ui.setBlockStatus(e.blockId, "uppercased");
  });
}
```

**`atto.complete({model, prompt, system?, maxTokens?, reasoningEffort?, timeoutMs?})`**
asks a model for one reply and resolves to `{text}`: no tools, no
streaming, nothing of the conversation. `model` is `provider/id` of a model
in `models.json`; it uses that provider's URL, key and headers. The request
is cancelled (the connection closed) after `timeoutMs` (default 30000) and
when the session ends or extensions reload. It rejects with a plain message
for a server that is down ("connection refused"), a timeout, an HTTP error,
an unknown model. `reasoningEffort` sets the reasoning effort: one of the
model's levels, or else sent as `reasoning_effort` (`"none"` turns thinking
off on LM Studio) for chat-completions models. An extension has one request
in flight at a time (local servers get slower, and may hang, when asked in
parallel); the others wait their turn, and the timeout counts from when a
request starts. `atto.setCompleteConcurrency(n)` (1 to 16) raises that.

atto lists these requests, only counts and model names, never prompts: the
Loaded block (after a `/reload`) and `/extensions` show "model calls: 3 to
p/m (1 failed)" per extension, and every call is a line in
`~/.atto/extensions.log` with the model, the outcome and the time.

## The context and the UI

`ctx` (also `atto.ui`, `atto.session`, `atto.cwd` outside handlers):

- `ctx.hasUI`: true in the TUI, false in `atto -p` and the server.
- `ctx.cwd`, `ctx.session.id`, `ctx.session.model` (`provider/id`).
- `ctx.session.name`: the session's name, `""` without one.
- `ctx.session.messages(limit?)`: the conversation's text so far, oldest
  first, as `[{role, text}]`: the user's messages and the model's answers on
  the current branch, without commands and their output; the last `limit`
  (default 50). Read from the session file, so a reply still streaming is
  not in it.
- `ctx.session.setName(name)`: names the session, as `/name` does (the
  terminal and the server; throws in `atto -p`).
- `ctx.ui.notify(text, level?)`: `info` (default), `warning`, `error`.
- `ctx.ui.setStatus(key, text | null)`: an item in the status line.
- `ctx.ui.setWidget(key, lines[] | null)`: lines shown above the input.
- `ctx.ui.setBlockStatus(blockId, text | null)` and
  `ctx.ui.setBlockDisplay(blockId, text | null)`: see above.
- `ctx.ui.showText(title, text, {lang?, preview?})`: a collapsible block in
  the transcript for longer output, such as a diff or a report. It is display
  only (the model never sees it) and saved in the session, so a resumed
  session shows it again. While collapsed it shows the first `preview` lines
  (default 10) and a "+N lines" row; click it or press ctrl+t to expand.
  `lang: "diff"` colours added lines green, removed lines red, `@@` lines
  cyan and file headers dim; any other value is plain text. The web client
  shows the same block; in `atto -p` the title and text arrive as a notice.
- `ctx.ui.select(title, options)`: `Promise<string | undefined>`.
- `ctx.ui.confirm(text)`: `Promise<boolean>`.
- `ctx.ui.input(prompt)`: `Promise<string | undefined>`.

Without a UI (`atto -p`, the server), `notify` goes to stderr (`-p`) or to
the client as an `extension/notify` notification (server); status items and
widgets are dropped (`-p`) or shown above the web client's input (server,
as `/remote` shows the TUI's); `select` and `input` resolve to `undefined` and
`confirm` to `false` at once. In the TUI, a dialog asked while another
dialog is open gets that default answer too. Time the user spends on a
dialog does not count against the handler timeout.

## Other APIs

- `atto.registerCommand(name, {description?, handler(args, ctx)})`: a
  slash command (`/name args`) in the TUI's command list, marked as the
  extension's. A built-in command of the same name wins.
- `atto.exec(command, {cwd?, timeout?})`: runs `command` with the agent's
  shell (bash, PowerShell on Windows); resolves to `{stdout, stderr, code,
  killed}`. `timeout` is in ms (default 60000); at it the command and its
  children are killed (`code` -1). The environment has `ATTO_SESSION_ID`
  and `ATTO_EXTENSION`.
- `atto.fs.readFile(path)`, `writeFile(path, text)` (creates parent
  directories), `exists(path)`, `list(dir)` (sorted names, directories end
  in `/`): synchronous, UTF-8, relative to the session's directory; errors
  throw.
- `atto.mcp.call(server, tool, args?)` and `atto.mcp.tools(server?)`: the
  session's MCP servers (see `atto mcp -h`), the same ones the agent reaches
  with `atto mcp` in its shell, started on first use and shared, so a
  stateful server keeps its state. `call` resolves to `{text, isError,
  content, structured?}` (`text` is what the model would read: text content
  as is, other content summarized in brackets) and rejects when the call
  could not be made: unknown server or tool, a project server the user has
  not approved, a server that fails to start. `tools` resolves to
  `[{server, name, description, inputSchema}]` for one server, or for every
  server that starts. Calls made through `atto mcp call` are shell commands,
  so `tool_call` handlers and hooks see them as such; `atto.mcp` calls are
  not shell commands and are not seen by them.
- `fetch(url, {method?, headers?, body?, timeout?})` (also `atto.fetch`):
  resolves to `{status, ok, headers, text(), json()}`; rejects on network
  errors only. Bodies over 10 MiB are cut.
- `atto.sendMessage(text)`: a user message to the model. In the TUI it
  steers a running turn or starts one; in `-p` and the server it steers the
  running turn.
- `atto.onDispose(fn)`: runs before the extension is unloaded (reload,
  exit); up to 1 s.
- `setTimeout`, `setInterval`, `clearTimeout`, `clearInterval`.
- `atto.log(...)`, `console.log(...)`: to `~/.atto/extensions.log`.
- `atto.name`: the extension's name.

## Limits

- Each extension has its own engine and goroutine; its code never runs in
  parallel with itself, so no locking is needed.
- A waited-for handler that does not answer within the timeout (5 s;
  `"extensions": {"timeout": seconds}`) is skipped with a notice.
- Script that runs longer than the timeout without yielding (a busy loop)
  is interrupted and the extension is disabled until the next reload.
  An exception in a handler is reported and that handler skipped; the
  extension stays.
- Nothing an extension does can crash atto.

## Example: redact secrets from command output

```ts
/// <reference path="./atto.d.ts" />
export default function (atto: Atto) {
  const secret = /\b(sk-[A-Za-z0-9]{20,}|ghp_[A-Za-z0-9]{36})\b/g;
  atto.on("tool_result", (e) => e.output.replace(secret, "[redacted]"));
}
```

## Example: a status item

```ts
/// <reference path="./atto.d.ts" />
export default function (atto: Atto) {
  const update = async () => {
    const r = await atto.exec("git status --porcelain");
    const n = r.stdout.split("\n").filter(Boolean).length;
    atto.ui.setStatus("dirty", n ? `${n} changed` : null);
  };
  atto.on("session_start", update);
  atto.on("turn_end", update);
}
```
