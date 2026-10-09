# Writing atto extensions

An extension is a TypeScript/TSX or JavaScript/JSX file that atto loads into every
session. It can watch and steer the agent's shell commands, add to prompts,
add slash commands, and draw portable panes, status slots, dialogs and transcript overlays. atto compiles it with esbuild
and runs it in an embedded JavaScript engine (goja); there is no Node.js, so
`require`, `process` and npm modules that need Node are not available.

`atto extensions docs` prints this file; `atto extensions source diff` prints the Go source of the native `/diff` command; `atto extensions types` prints
`atto.d.ts`, the API's type declarations.

## Where they live

| Path | Source |
| --- | --- |
| `~/.atto/extensions/<name>.ts`, `.tsx`, `.js` or `.jsx` | user |
| `~/.atto/extensions/<name>/index.ts` or `index.tsx`, `index.js` or `index.jsx` | user (a folder: other files in it can be imported) |
| `<project>/.atto/extensions/...` (same shapes) | project |
| native Go commands inside atto (full builds list these with extensions) | builtin |

`<project>` is the nearest directory above the working directory that holds
`.git`. The name is the file or folder name. Files ending in `.d.ts` and
names starting with `.` are ignored.

**Project extensions need approval**, since a repository brings them: run
`/extensions approve <name>` in the TUI or `atto extensions approve <name>`.
The approval covers the code as it is (the bundle's hash, including the
files it imports); any change needs approval again. An agent cannot approve
from its shell. User extensions need no approval.

**Slim builds** (`-tags noext`, `atto --version` shows `(slim)`) have no
JavaScript or TypeScript extension engine. User and project files are ignored,
not approved or run; the Loaded block, `atto extensions`, and `/extensions`
report their count. Install with `ATTO_VARIANT=slim`, or switch with
`atto update -variant slim|full`. Updates otherwise preserve your variant.

**Native commands** `/diff [--staged] [path]` and `/autorename` ship in both
variants. They were formerly TypeScript built-ins and are now implemented in
Go ([diff](../extensions/native_diff.go),
[autorename](../extensions/native_autorename.go)). `/autorename` runs only when
you invoke it: it asks the session's current model for a title from the latest
30 user/assistant text messages (not tool output), with a 64-token budget,
60-second timeout and reasoning effort `none` (retrying at the model's default
if necessary). It respects the current `/name` by including it in the prompt,
but replaces it when you explicitly request a new title. Neither command
requires approval. The full build lists them as `builtin`; both builds allow
`"extensions": {"disabled": ["diff"]}`. In the full build, a user or project
extension with the same name still replaces the native command, subject to
normal project approval. The full build exposes `atto.exec`, `atto.complete`, `ctx.session` and the
portable `atto.ui` API to user extensions. See [examples/extensions](../examples/extensions)
for JavaScript/TypeScript examples.

To turn one off, add its name to `settings.json`:

```json
{ "extensions": { "disabled": ["noisy"], "timeout": 5 } }
```

## Load, reload, debug

- Extensions load when a session starts. After editing one, run `/reload`
  (or `atto reload` from the agent's shell): the old runtime is disposed of
  (`onDispose` callbacks run, timers stop, its UI registrations and callbacks retire, dialogs cancel)
  and the new code receives `session_start` with reason `reload` to rehydrate
  session-backed state. The reload report lists `changed extension <name>`.
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

Display-only changes use `atto.ui.render({site:"assistantMessage"}, fn)`:
`e.props.kind` distinguishes `answer` from `reasoning`; `e.props.blockId`
correlates a saved response with these observer events. Do slow model calls
in observers or commands, cache their results, then invalidate the item site.
Render hooks must not perform model calls, file/network I/O or state writes.
The translation examples do exactly this with bounded ephemeral caches.

A render may wrap `await next(e)` or pass whitelisted display overrides,
for example `next({...e,props:{...e.props,text:translated}})`. The model's
conversation, typed transcript truth and copied answer stay unchanged.
Completed overlays are saved as `ui_item_display`; replay never runs old
extension code. Clients offer “show original” outside the extension drawing.

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

## The context and portable UI

`ctx` has `ui`, `session`, `cwd` and `hasUI` (true with an interactive client,
false in standalone `atto -p`). `atto.ui` is the same API outside handlers.
`session.id/model/name`, `session.messages(limit?)` and `session.setName(name)`
retain their existing meaning; messages returns user/assistant text from the
active branch, not commands or display trees.

Register before opening: `atto.ui.render({site,id?}, (e,next)=>tree)` returns a
disposer. Matching renderers compose in loader order; `next()` reaches the
unmodified built-in drawing. Return it to leave it alone, put it in a Box to
wrap, or return a replacement. `null` removes a contribution (native item
rendering resumes). Only text display fields are overridable through `next`;
identity, status and provenance are immutable. `next()` on item sites is an
opaque native-engine reference, not a constructor or a text approximation.

Sites: `pane`, `band`, `status`, `toast`, `transcript`, `dialog`, `userMessage`,
`assistantMessage`, `toolCall`, `notice`. IDs and control keys are provider-local,
then namespaced by atto. Keys must be stable and unique in the composed tree.
Render events have `surface:"shared"`: one drawing per session, not per client.
Callbacks receive actual `clientId`, `surface`, `rev`; only key/event names,
never functions or source, cross the wire. Old revisions cannot act again.

`atto.ui.resolve(e)` supplies the v1 factories: Box, Text, Markdown, Code, Diff,
Link, Button, Input, Select, List (table mode), Table (alias), Progress, Collapse,
Image. All props are documented in [ui.md](ui.md#2-element-contract-v1) and
`atto.d.ts`. Call factories with props and children in plain `.ts`, or use JSX:
esbuild targets CommonJS/ES2017 with `atto.ui.jsx` and `atto.ui.Fragment`.
Strings become Text; fragments are grouping Boxes, without React or a DOM.
Only Box/Collapse accept arbitrary element children; Text accepts spans.

- `open({site,id,title?,focus?,...})`: shared existence/visibility. Pane placement
  is client-owned (`auto`, `side`, `abovePrompt`). Defaults: title=id, 40 columns,
  8 rows, no focus/close-on-Escape; status priority=0 and align=start.
  Focus hints only address the invoking command/press client, with an empty editor.
  Open/close never target item sites. Duplicate open updates metadata; transcript
  open appends once and subsequent drawings replace that saved block.
- `close({site,id})`: shared removal. Closing a transcript block saves a tombstone.
- `invalidate(match?)`: coalesced redraw (10/site/sec, 60/provider/sec); omitted
  means this provider's live sites. It does not change visibility.
- `toast(text,{level?,timeoutMs?})`: info/warning/error, default 4000 ms, range
  500..30000; expiry is shared, transient, not replayed.
- `notify(text,level?)`: typed transcript notice, not a toast.
- `select(title,options)`, `confirm(text)`, `input(prompt)`: existing asynchronous
  broker helpers. Wrapping a helper dialog must include `next()` exactly once;
  required Go controls cannot be replaced. First valid answer wins across clients.
  Reload/session close cancels questions. Workers wait without attached clients;
  standalone `-p` returns undefined/false defaults. User waits pause handler limits.

`atto.store.get<T>(key)`, `set(key,JSONValue)`, `delete(key)`, `keys()` are async,
extension+session-scoped JSON storage: 64 KiB/value, 1 MiB total, keys 1..128
bytes. Session-writer updates survive reload/resume and inherit at a fork point;
branch replay restores only reachable updates, `/clear` starts fresh. Module
variables are ephemeral. Store writes do **not** redraw: invalidate explicitly.
In standalone `-p -no-save`, storage is intentionally in-memory only.

Render hooks are fast, read-only drawings (100 ms/handler, 250 ms/site).
Store writes, dialogs, UI mutations, execution, file/network and model APIs
throw during render. Invalid trees, exceptions or timeouts show the built-in
default, not a stale extension tree, and log a bounded reason. Trees are limited
to 256 KiB, 2048 nodes, depth 32 and 128 KiB aggregate text. Catalog validation
rejects unknown props, unsafe links, duplicate keys/hotkeys and bad callbacks.
Theme colors are names, not ANSI/CSS. See [ui.md](ui.md) for the whole contract.

Saved old `block_display` and `ext_text` entries are converted **on read** into
passive Markdown/Text/Collapse trees. Files are not rewritten, and the old
string setters and notifications are gone; there is no second rendering API.
Archived actions stay disabled until a current provider explicitly rebinds.

### Example: a shared counter pane (`counter.tsx`)

```tsx
export default function (atto: Atto) {
  let count = 0;
  atto.on("session_start", async () => {
    count = (await atto.store.get<number>("count")) ?? 0;
    atto.ui.invalidate({ site: "pane", id: "counter" });
  });
  atto.ui.render({ site: "pane", id: "counter" }, e => {
    const { Box, Text, Button } = atto.ui.resolve(e);
    return <Box gap={1}>
      <Text text={`Count: ${count}`} />
      <Button key="more" label="Add one" hotkey="a" onPress={async () => {
        count++;
        await atto.store.set("count", count);
        atto.ui.invalidate({ site: "pane", id: "counter" });
      }} />
    </Box>;
  });
  atto.registerCommand("counter", { handler: () =>
    atto.ui.open({ site: "pane", id: "counter", title: "Counter", focus: true }) });
}
```

### Example: wrap native tool rows (`review-tools.ts`)

```ts
export default function (atto: Atto) {
  atto.ui.render({ site: "toolCall" }, async (e, next) => {
    const { Box, Text } = atto.ui.resolve(e);
    const original = await next(e);
    return Box({ children: [original, Text({
      text: "Reviewed by my extension", color: "muted",
    })].filter(x => x !== null) });
  });
}
```

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

See [token-speed.ts](../examples/extensions/token-speed.ts): keep text in a
module variable, register a `status` renderer, and open/invalidate its keyed
slot after `step_end`. This uses the same trees as built-in status items.
