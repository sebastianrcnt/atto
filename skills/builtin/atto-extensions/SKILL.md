---
name: atto-extensions
description: Write or change atto extensions (TypeScript) - events, slash commands, display-only block changes, side model calls - when the user wants to customize atto's behaviour or UI.
---

# Writing atto extensions

An extension is a TypeScript/TSX or JavaScript/JSX file the full atto build loads into every session. Slim builds cannot run extensions: ask the user to switch with `atto update -variant full` first. Do not guess its API: read it first.

1. Read the API.
   - `atto extensions docs` is the guide (events, ctx, UI, limits, examples).
   - `atto extensions types` prints `atto.d.ts`, the exact signatures.
   - `atto extensions source diff` prints the native Go /diff implementation (not a TypeScript extension). Use the guide and examples/extensions for TypeScript examples.
2. Decide where it goes.
   - User extension: `~/.atto/extensions/<name>.ts` (or `<name>/index.ts` for several files). Needs no approval. This is the default.
   - Project extension: `<project>/.atto/extensions/<name>.ts`, only if the user wants it shared with the repository. It does not run until the user approves it: tell them to run `/extensions approve <name>` (or `atto extensions approve <name>` in their own terminal). Never try to approve it yourself; the shell refuses.
3. Write it: a default export taking `atto`, with `/// <reference path="./atto.d.ts" />` on top.
4. Load it and read the result.
   - Run `atto reload`. The answer comes back as an `[atto event]`: load errors have file:line:col. Fix and reload until it loads.
   - `atto extensions list` shows each extension's status (ok, failed with the error, needs approval, disabled) without running it.
   - Handler errors and `atto.log(...)` go to `~/.atto/extensions.log`.
5. Tell the user what the extension does, where the file is, and (for a command) how to run it.

## Rules

- Use `atto.ui.render`, `resolve` constructors (or JSX in `.tsx`), `next()` and keyed sites for display-only changes. Drawings never change model context. Render hooks must be fast and read-only: no I/O, store writes or UI mutations. Use `atto.store` for session JSON state, then invalidate explicitly. Read the exact types before writing UI code.
- Blocking events (`tool_call`, `tool_result`, `user_prompt`) are waited for and have a timeout (5 s by default). Keep their handlers fast and do slow work elsewhere (`message_end`, `turn_end`, a command). A handler that throws is skipped, not fatal.
- `message_end` and `reasoning_end` never delay anything, so they are the place for model calls. `step_end` gives each response's usage and timing (`outputTokens / genMs` is the generation speed).
- `atto.complete({model, prompt, ...})` makes a side model call: for helpers such as translation or summaries. The model is `provider/id` from models.json. Keep the default concurrency of 1 (local servers slow down or hang when asked in parallel), pass `reasoningEffort: "none"` when thinking is not needed, and set `timeoutMs` if the task is long. Handle rejection (server down, timeout) by showing a status such as "failed".
- No Node.js: no `require`, `process` or npm packages that need them. Use `atto.exec`, `atto.fs`, `fetch`, `atto.mcp`.
- Never put secrets (API keys, tokens) in extension files. Read them from the environment through `atto.exec`, or from a file the user keeps outside the repository.
- Prefer small, single-purpose extensions: one file per behaviour, named for it.
- In full builds, a user or approved project extension named `diff` or `autorename` replaces the native command of that name. Write it against the public TypeScript API; do not copy the Go source into a `.ts` file.
