# atto

A small terminal coding harness. You bring your own API key or model server.

## Install

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/sebastianrcnt/atto/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/sebastianrcnt/atto/main/install.ps1 | iex
```

**With Go** (1.27+)

```sh
go install github.com/sebastianrcnt/atto/cmd/atto@latest
```

The install scripts:

1. download the binary for your platform from the [latest release](https://github.com/sebastianrcnt/atto/releases/latest);
2. check it against the release's `checksums.txt`;
3. install it to `~/.local/bin` (macOS and Linux) or `%LOCALAPPDATA%\Programs\atto` (Windows).

They don't need root. Three environment variables change what gets installed:

| Variable | Effect |
| --- | --- |
| `ATTO_CHANNEL=edge` | installs the edge build (see below); `stable` is the default |
| `ATTO_VERSION=v0.1.0` | pins that exact release; wins over `ATTO_CHANNEL` |
| `ATTO_INSTALL_DIR=...` | installs somewhere else |

Running the script again is safe. It installs the newest build of the channel over the old one.

**Edge builds.** Once a night, `main` (when its tests pass) replaces the [`edge` prerelease](https://github.com/sebastianrcnt/atto/releases/tag/edge), named like `v0.0.3-dev.14+abc1234` (the next patch version, 14 commits after the last tag, at commit `abc1234`). It's unreleased code and may break. Install it with `curl -fsSL https://raw.githubusercontent.com/sebastianrcnt/atto/main/install.sh | ATTO_CHANNEL=edge sh` (`$env:ATTO_CHANNEL = "edge"` before running `install.ps1` in PowerShell), or build from source with `go install github.com/sebastianrcnt/atto/cmd/atto@main`.

### Update

```sh
atto update          # install the latest release
atto update -check   # only check
atto channel         # show which channel this binary follows
atto channel edge    # switch to edge builds
atto channel stable  # go back to tagged releases
```

The channel is part of the binary: release builds are `stable`, edge builds are `edge`, and `atto -version` shows which (`atto v0.0.3-dev.14+abc1234 (edge)`). `atto update` stays on the channel you're on. `atto channel <name>` installs the latest binary of that channel, which then follows it. Nothing is saved in settings. Going from edge back to stable installs the latest stable release even though its version number is lower, and says so: `Switched to stable: atto v0.0.3-dev.14 → v0.0.2`. Only `atto channel` does that. Builds from `go install` or a local `go build` have no channel (they report `dev`) and get no update notices.

Once a day atto asks the GitHub API whether a newer release exists on your channel, and mentions it when you start atto. It never updates itself on its own. To turn the check off, set `"updateCheck": false` in `~/.atto/settings.json`.

If you installed with `go install` or Homebrew, update that way instead.

## Set up a model

Use a hosted provider:

```sh
atto auth set opencode       # OpenCode Zen API key
atto auth set opencode-go    # OpenCode Go API key
atto login openai            # Sign in with ChatGPT (subscription)
atto models                  # list what's available
```

Or use your own OpenAI-compatible server (llama.cpp, vLLM, Ollama, …) by adding it to `~/.atto/models.json`:

```json
{
  "providers": {
    "local": {
      "baseUrl": "http://localhost:8080/v1",
      "models": [
        { "id": "my-model", "contextWindow": 131072, "maxTokens": 32768 }
      ]
    }
  }
}
```

A provider can also set:

- `apiKey`: a literal key, or `"$ENV_VAR"` to read it from the environment
- `headers`: extra request headers
- `extraBody`: extra fields for the request body
- `api`: `"openai-completions"` (the default) or `"openai-responses"`
- `subscription`: `true` for a flat-rate plan, so the status line marks its cost as an estimate

A model can set:

- `efforts`: the reasoning levels it supports
- `effortMap`: how atto's effort levels translate into what the server expects
- `input`: `["text", "image"]` if it accepts images (default: text only; catalog models know this already)
- `cost`: prices in dollars per million tokens (`input`, `output`, `cacheRead`, `cacheWrite`), shown as the session's cost in the status line. Catalog models take theirs from models.dev; on a subscription (ChatGPT login, OpenCode Go) the figure is what the same usage would cost over the API, shown as `≈$0.123`

## Use

```sh
atto                                  # interactive session
atto "fix the build"                  # interactive, starting with this message
atto -m local/my-model                # pick the model
atto -c                               # continue the last session here
atto -resume                          # pick a saved session
atto -p "fix the failing test"        # one prompt, non-interactive
git diff | atto -p "review this"      # stdin is appended to the prompt
atto -p -image shot.png "why?"        # attach images (repeatable)
pngpaste - | atto -p "what is this?"  # an image on stdin is attached too
atto -p -output-format json "..."     # also: stream-json
atto -p -goal "make the tests pass"
```

### Keys in the session

| Key | Action |
| --- | --- |
| `Enter` | send; while the agent works, steer it after its current step |
| `Tab` | queue a message for when the agent finishes |
| `Esc` | interrupt, or send pending steers now |
| `Ctrl+Enter` | while the agent works, interrupt it and send the prompt (after pending steers) as a new turn at once; an active goal is not paused but waits for you after that turn. `Ctrl+G` does the same where the terminal can't tell `Ctrl+Enter` from `Enter` (atto asks for xterm modifyOtherKeys and the kitty keyboard protocol; Terminal.app, `screen`, the Windows console and tmux without `extended-keys on` don't send it) |
| `Esc` `Esc` | on an empty prompt: open the session tree to go back to an earlier message and edit it |
| `Shift+Tab` | cycle reasoning effort |
| `Ctrl+T` | expand everything: thinking, command groups and every command's full output; again to fold it all back (or click one block) |
| `Ctrl+B` | move the running command to the background: it keeps running as a job (`/jobs`), the agent goes on and gets an `[atto event]` when it exits |
| `Ctrl+C` | copy the selection if there is one; otherwise interrupt, clear the input when idle, or quit when the input is empty |
| `Ctrl+V` / `Alt+V` | attach the image on the clipboard (use `Alt+V` where the terminal pastes text on `Ctrl+V`, as on Windows) |

Shell commands, as in pi: start the prompt with `!` to run a command yourself, in the working directory, with the shell and environment the agent's own commands use and its output cut (the full text is saved to a temp file). It shows in the conversation as a `! command` block, and the command and its output go to the model as a user message the next time it runs (`Ran` followed by the command and its output). `!!command` runs it the same way but keeps it from the model; the block says so. The input turns green while the text starts with `!`. `Esc` or `Ctrl+C` cancels the command. It runs at once even while the agent works, but its result joins the conversation only when the turn ends, so it never lands between a tool call and its result; one command runs at a time (another is refused and stays in the input). `!` alone is an ordinary message. Commands are saved in the session and come back on resume, `/tree` and fork. No hooks run for them, and `atto -p` has no such prefix.

File mentions, as in pi: type `@` at the start of a word to pick a file or folder of the project (fuzzy; a `/` in the query matches the whole path; `.gitignore` is respected). `Tab` or `Enter` inserts `@path`, quoted when it has a space; a folder keeps completing inside it. Only the path is sent: the model reads the file itself.

Pasting the path of an image file, or dropping the file on the terminal, attaches it too. Images show as `[image 1: 1024x768 PNG]` in the input; delete the placeholder to drop the image. Images larger than 2048 pixels are scaled down. Clipboard images need `osascript` (macOS; `pngpaste` is used if installed), `wl-paste` or `xclip` (Linux), or PowerShell (Windows, WSL).

`atto -p` attaches images given with `-image` and an image piped to stdin (PNG, JPEG, GIF or WebP, recognized by its first bytes); the prompt argument is then the text. In the web client (`atto serve`, `/remote`), attach images with the image button, by pasting or by dropping them; over JSON-RPC, `turn/start` takes `images: [{mimeType, data}]` (base64 or a `data:` URL, at most 10 of 10 MB each). Either way the model must accept images.

Pastes over 1000 characters show as `[Pasted Content 1234 chars]` and are sent in full.

Commands the agent runs: while one runs, its block shows the command and the last lines of output (the line above the input then shows a made-up verb, as `Blorping…`, one per turn, and the turn's tokens so far, `↑ 8.1k  ↓ 1.2k tokens` (input the server had not cached; output: thinking, text and the commands being written); see `spinnerVerbs` below). Once it ends it folds to one line, `✓ description · time  $ command`; a failed one (non-zero exit, timeout, canceled) keeps its last two output lines. Click the line for the full command and the output's first and last lines, `… +N lines` for all of it. Commands run one after another, with only the model's thinking between them, group: the ones before the last fold into one dim line, how many and their descriptions, `▸ 5 commands · 12s  Read main.go, Search for TODOs, …`, with `· 1 failed` in red when some failed (those stay shown below it, as does the last command). Click it, or press `Ctrl+T`, to see every command and thought of the group on a shaded background. Set `"toolGroups": false` in `settings.json` to show each command on its own.

### Mouse and selection

In the fullscreen renderer atto handles the mouse itself: the wheel scrolls the conversation, and a click on a block's header or its `+ N lines` line expands or collapses it.

- Drag to select text anywhere in the conversation; it is copied when you let go. Dragging past the top or bottom scrolls.
- Double-click selects a word (a file path or a URL is one word), triple-click the line.
- With a selection showing, `Ctrl+C` copies it instead of interrupting and `Shift+arrows` extend it. `Esc`, `PageUp` and `PageDown` keep it; other keys and clicks clear it.
- Copying uses every way that applies: the system clipboard (`pbcopy`, `wl-copy`/`xclip`/`xsel` and the PRIMARY selection, PowerShell), the tmux paste buffer inside tmux, and OSC 52, which is what reaches your clipboard over SSH. A short note says which worked.

Inside tmux, atto only gets the mouse with `set -g mouse on` (it says so at startup when it is off). OSC 52 through tmux needs `set -g set-clipboard on` or `set -g allow-passthrough on`.

To use the terminal's own selection instead, hold the key that bypasses mouse reporting: `Shift` in most terminals (Windows Terminal, GNOME Terminal, Konsole, kitty, WezTerm, Alacritty), `Option` in iTerm2, `Fn` in Terminal.app. Over SSH or in tmux it is the key of the terminal you are sitting at. Or turn mouse handling off with `"mouse": false` in `settings.json` or `ATTO_NO_MOUSE=1`; scrolling then works with `PageUp`/`PageDown`.

### Slash commands

| Command | What it does |
| --- | --- |
| `/model` | switch model mid-session |
| `/effort` | set reasoning effort |
| `/tui [auto\|fullscreen\|inline]` | show or change the renderer; saved to `settings.json` and applied at once |
| `/compact` | compact the conversation now |
| `/copy` | copy the last answer; works over SSH in terminals with OSC 52 |
| `/context` | show what fills the context and how much is cached |
| `/reload` | read AGENTS.md, skills, hooks, extensions, MCP servers, `settings.json` and `models.json` again, keeping the conversation |
| `/extensions [approve <name>]` | list extensions, or approve a project extension |
| `/diff [--staged] [path]` | show what changed in the working tree: a summary, then the diff (a built-in extension, see `extensions/builtin/diff.ts`) |
| `/resume` | resume a saved session |
| `/tree` | go back to any point of the session; earlier branches are kept |
| `/fork` | start a new session from an earlier message |
| `/name` | name the session |
| `/autorename` | have the current model name the session from what it is about |
| `/archive` | archive the session and start a new one |
| `/clear` | start a new session |
| `/goal [<objective>\|clear\|edit\|pause\|resume]` | set or view the goal for a long-running task, as in codex: bare `/goal` (or `status`) shows it with the time and tokens used, `help` shows the usage, `edit` opens a prompt, a new objective asks before replacing an unfinished goal. The words help and status alone never become an objective. Clearing or pausing while a turn runs is told to the model. A message sent while the goal is waiting, paused, stalled or usage limited carries a short note saying so, so the model answers instead of resuming goal work. The status shows at the right of the status line ("Pursuing goal (14m)"), Esc pauses it, and opening a session with a paused or stalled goal asks whether to resume |
| `/remote [on [port]\|off]` | control this session from a phone or browser: serves atto's web client on port 7879 (or `"remote": {"port": N}` in `settings.json`), prints its link and a QR code, and marks messages sent from there "from remote"; `off` closes every connection and revokes the link |
| `/jobs`, `/stop` | list or stop background jobs |
| `/timer`, `/timers` | wake the agent later, or list pending timers |
| `/quit` | exit atto |

## How it works

**One tool.** The model works through a single shell tool: bash on macOS and Linux, PowerShell on Windows. Everything else is a command it can run:

- `atto history grep` searches the session transcript, including turns that were compacted away.
- `atto job start` runs a command in the background. With `-notify REGEXP` (and `-notify-limit N`, default 50) each matching output line wakes the agent while the job keeps running; matches within a second are batched.
- `atto monitor` and `atto timer` wake the agent when something happens. `atto timer every 30m [-count N] [-until HH:MM|duration] <message>` repeats (minimum 1m, no drift; missed intervals fire once).
- `atto goal [status]` shows the goal; `atto goal complete|blocked|pause "<why>"` reports on the goal (done; stalled on the same blocker for three goal turns; paused at the user's request). A turn that fails stalls the goal, and one that hits the provider's usage limit marks it usage limited; `/goal resume` continues either.
- `atto reload` reloads the session's AGENTS.md files, skills, hooks, extensions, MCP servers and settings after the agent edited them; the result comes back as an `[atto event]`.
- `atto view <image>...` lets the model see an image file it made, a screenshot or a rendered plot: the image is attached to the result of the command that ran it, the way your own images are (PNG, JPEG, GIF or WebP, scaled to fit 2048 pixels, at most 8 per command). It works only in a foreground command of the agent, including a subagent's, not in a background job or your own shell. The command's block shows `▣ shot.png 1136×1038` per image. Chat completions servers get the images in a user message after the tool results, as pi does; the Responses APIs in the tool result itself. A model without image input gets a note instead and nothing is attached; images are saved with the session like yours, and compaction drops them.

**Nothing loads unseen.** When a session starts, resumes or forks, the conversation opens with a dim "Loaded" block: the AGENTS.md (or AGENTS.override.md, CLAUDE.md) files in the system prompt with their sizes (and whether the 32 KiB cap cut them), files that were found but skipped and why, the skills and where they came from, the hooks, the extensions, the settings and models files read, and the model and effort with where each came from (`-m`, the session, `settings.json`). Click its header or press `Ctrl+T` for the full list. `/reload` shows it again with what changed. The same report:

- `atto context` prints it for the current directory (`-json` for the data).
- `atto -p -v` prints the one-line-per-kind summary to stderr at the start, and stream-json's `init` event has it as `context`.
- The server's `thread/start` and `thread/resume` results include it as `context`.

**Prefix-cache friendly.**

- The history is append-only.
- The system prompt and the tool schema don't change during a session, unless `/reload` (or `atto reload`) finds that AGENTS.md files or skills changed; the next request then reads the new prompt in full, and the reload says so.
- Compaction keeps the latest user messages plus a summary, the way codex does it. Run `/context` to see the cache hit rate.

**Sessions** are JSONL files under `~/.atto/sessions/`. As in pi, entries form a tree: going back with `/tree` starts a new branch in the same file and keeps the old one. When that leaves work behind, atto asks whether to summarize the branch being left (optionally with your own instructions); the current model writes the summary, `Esc` cancels it, and the model sees it on the new branch. `"branchSummary": {"skipPrompt": true}` in `settings.json` never asks. `atto history grep` searches every branch and marks entries on other branches; `-active` limits it to the current one.

Manage sessions from the shell, without the TUI:

```
atto resume [id]                     resume a session (no id opens the picker; an id may be a unique prefix)
atto sessions [-all] [-archived] [-json] [-n N]   list this directory's sessions (-all: every directory)
atto sessions show <id>              details and the last user messages
atto sessions rename <id> <name>
atto sessions archive|unarchive <id>
atto sessions delete [-y] <id>       permanent: also removes its jobs, inbox, goal and images no other session uses
```

`delete` asks first on a terminal and refuses without `-y` elsewhere. Inside an atto agent only `list` and `show` work, so a model can't destroy session history.

A session is open in one atto at a time: while a terminal has it, resuming it in another (or with `atto -p`, or from `atto serve`) is refused with the pid that holds it, since two writers would undo each other's work and goal.

**Hooks** use the same format as Claude Code: `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `Stop`, `PreCompact`, `SessionStart`, `SessionEnd` and `Notification`.

- Put them in `~/.atto/settings.json` or in the project's `.atto/settings.json`.
- A hook that exits with code 2, or returns `{"decision": "block"}`, stops the action.
- `Stop` runs when the agent is done answering (in the TUI, `-p` and the servers). Blocking it sends the reason to the model as a user message and the turn continues. The input has `stop_hook_active`, true once a Stop hook has already kept this turn going, so a hook can let it finish. After 8 blocks in a row atto stops anyway. An interrupted turn (Esc) runs no Stop hook. With `/goal`, the Stop hook runs at the end of every turn, before the goal decides whether to continue.
- `SessionEnd` runs when a session ends, with `reason`: `exit` (quitting the TUI), `clear` (`/clear`), `resume` (switching to another session), or `other` (`-p` finishing, the server shutting down, a conversation archived). It cannot block and has a 5 second default timeout (set `timeout` on the hook to change it), so exiting stays quick. The matcher is tested against `reason`.
- `Notification` runs when atto wants your attention, with `message` and `notification_type`; the matcher is tested against the type. It cannot block. The TUI sends `idle_prompt` when a turn that took 15 seconds or more is done and atto waits for your input (not when a queued message or a goal turn follows), `background_event` when a job or timer event arrives while atto is idle, and `goal_blocked` when a goal becomes blocked. There are no permission prompts, so no such notification. `-p` and the servers send none.
- Hook messages, such as the reason of a blocked Stop, appear in the transcript, and as `hook` events in `-p --output-format stream-json`.

**Extensions** are TypeScript or JavaScript files, often written by the agent itself, that atto runs in an embedded engine (no Node.js needed). They can block or rewrite the agent's commands, rewrite what the model sees of their output (to redact secrets, say), add to prompts, add slash commands, and show status items, widgets and dialogs in the TUI.

```ts
// ~/.atto/extensions/no-force-push.ts
export default function (atto) {
  atto.on("tool_call", (e) => /git push.*--force/.test(e.command) ? { block: true, reason: "no force-push" } : undefined);
  atto.registerCommand("todo", {
    description: "Count TODOs",
    handler: async (args, ctx) => ctx.ui.notify((await atto.exec("git grep -c TODO || true")).stdout || "none"),
  });
}
```

- Put them in `~/.atto/extensions/` (`name.ts`, or `name/index.ts` with files it imports) or the project's `.atto/extensions/`. Project extensions run only after you approve them (`/extensions approve <name>` or `atto extensions approve <name>`); a change needs approval again.
- `/reload` (or `atto reload`) loads changes; the Loaded block shows each extension's status, commands and events, and errors with `file:line`.
- Events: `session_start`, `session_end`, `turn_start`, `turn_end`, `user_prompt`, `tool_call`, `tool_result`. They run inside the hooks: `PreToolUse` hooks, then `tool_call`, the command, `tool_result`, then `PostToolUse` hooks.
- Also `atto.exec`, `atto.fs`, `fetch`, timers, `atto.sendMessage`; dialogs and widgets are TUI-only (in `-p` and the server, dialogs get default answers).
- `atto extensions docs` prints the guide ([docs/extensions.md](docs/extensions.md)), `atto extensions types` the type declarations, `atto extensions source diff` the source of a built-in one, `atto extensions` the list. [examples/extensions](examples/extensions) has examples to copy, such as one that shows reasoning translated by a small local model. A handler that hangs is skipped after 5 seconds and a runaway script is stopped and its extension disabled; atto goes on.

**Skills** are read from `~/.atto/skills` and the project's `.atto/skills` (the first of a name wins), and from nowhere else: directories other tools share, such as `.agents/skills` and `.claude/skills`, hold what was installed for those tools.

**Built-in skills.** atto ships skills of its own (Agent Skills, one `SKILL.md` each), listed in the system prompt like yours and shown as `builtin` in the Loaded block and `atto context`. Today there is one, `atto-extensions`: when you ask to customize atto, the model reads the extension API with `atto extensions docs` and `types`, writes the extension, runs `atto reload` and fixes load errors. Because the model reads skills as files, they are written to `~/.atto/cache/skills/<hash>/<name>/SKILL.md` (the hash changes when the text does). A skill of the same name in `~/.atto/skills` or the project's `.atto/skills` overrides a built-in one; `/skill:atto-extensions` runs it by hand; to turn one off: `{ "skills": { "disabled": ["atto-extensions"] } }` in `settings.json`. `atto extensions source diff` prints the source of the built-in `/diff` extension, the example the skill points to.

**MCP.** atto has one tool, the shell, and keeps it that way: MCP servers are reached through `atto mcp` subcommands that the model runs in the shell, not through tools of their own. The tool schema and the system prompt stay the same whatever you configure (when at least one server exists the prompt gets one line naming them, in sorted order, so it changes only when your configuration does), and the prompt cache survives.

```
atto mcp add github -scope user -e GITHUB_TOKEN='${GITHUB_TOKEN}' -- npx -y @modelcontextprotocol/server-github
atto mcp add docs -url https://mcp.example.com/mcp -H 'Authorization: Bearer ${DOCS_TOKEN}'
atto mcp list                                 # servers, scope, status, tool count
atto mcp tools                                # every tool, one line each
atto mcp tools github create_issue            # one tool's full JSON schema
atto mcp call github create_issue '{"repo": "me/x", "title": "Bug"}'
echo '{"query": "atto"}' | atto mcp call docs search -    # arguments from stdin
```

- **Configuration** is Claude Code's `.mcp.json` format, `{"mcpServers": {"name": {"command", "args", "env"} | {"type": "http", "url", "headers"}}}`, in three places: `~/.atto/mcp.json` (user), `<project>/.mcp.json` (shared, checked in) and a private per-project file, `~/.atto/projects/<project name>-<hash>/mcp.json` (local, written by `atto mcp add -scope local`, outside the repository). The shared, checked-in file is `.mcp.json` and needs approval; a `<project>/.atto/mcp.json` is not read at all (the Loaded block says so and where to move it), because a repository must not be able to start commands unapproved. The later wins by name (local over project over user). Strings may use `${VAR}` and `${VAR:-default}`, which is how tokens stay out of the files. A project's `.mcp.json` works as it does in Claude Code; `~/.claude.json` is not read. Transports are stdio and streamable HTTP (`"type": "http"`; `"sse"` also works). Remote servers that need OAuth are not supported; use a header token.
- **Approval.** A server from a project's `.mcp.json` runs a command the repository brought, so it needs your approval once, as project extensions do. At session start the TUI asks for each: allow, deny, or allow all for this project. Or run `atto mcp approve <name>`. Approvals are kept in `~/.atto/mcp-approvals.json` by file and server name with a hash of the entry, so a changed entry needs approval again ("allow all" covers a file's servers whatever they say later). Unapproved servers are listed but never started. `atto mcp approve` refuses when run by the agent (`ATTO_AGENT` is set). Servers in your own user and local files need no approval.
- **Servers live in the session.** They start on first use and stay until the session ends, so a stateful server is not restarted per call. `atto mcp call` and `tools` run by the agent talk to the running atto over a Unix domain socket (`~/.atto/mcp/<session id>.json` holds its path and a random token, mode 0600; it works on Windows 10+ too). Run from a normal terminal, a server is started for that one command and stopped after. `/reload` (or `atto reload`) re-reads the files, keeps servers whose entry did not change and restarts those that did.
- **Transparency.** The Loaded block and `atto context` list every server with its scope, transport, command or URL, and status (not started, running with N tools, failed with the reason, needs approval). The calls are ordinary shell commands, so they appear as normal tool blocks and a `PreToolUse` hook with matcher `Bash` can gate them (for example by looking for `atto mcp call github`). Extensions can use the same servers: `await atto.mcp.call(server, tool, args)` and `atto.mcp.tools(server?)`.

**Subagents** (off by default) let the model hand self-contained work to a background child session, through its shell like everything else. Turn them on with `"subagents": {"enabled": true}` in `settings.json`; the system prompt then tells the model about them and to start them only when you ask.

```
atto agent start NAME PRESET "<task>"   start one in the background; returns at once
atto agent steer NAME "<message>"       add instructions to its running turn
atto agent next NAME "<message>"        a follow-up turn when it is idle
atto agent wait NAME [-timeout 10m]     block until its turn ends and print its report (exit 124 on timeout)
atto agent wait-any [NAME...]           the first running one to finish
atto agent report NAME                  its last message, status, duration, tokens (and ≈cost when the model has prices)
atto agent list | stop NAME | presets
atto agent rm NAME... | rm -done        remove finished ones; their sessions are archived
```

- A subagent is its own session (in the parent's directory) that sees only the messages it is given, and the parent sees only its last message. Each turn runs headless as a job of the parent (`atto job list` shows `agent NAME`); when it ends the parent gets an `[atto event]` saying so. Its session is hidden from `atto resume` and `atto sessions`.
- **Presets** fix a subagent's model, effort and instructions; the model can't choose them otherwise. The built-in `general` uses the parent's model and effort (or `subagents.model` / `subagents.effort` from `settings.json`) with generic worker instructions. Add presets as Markdown files in `~/.atto/agents/` or the project's `.atto/agents/` (the project wins on the same name, and either replaces the built-in `general`):

  ```markdown
  ---
  name: reviewer
  description: reviews a diff for bugs
  model: anthropic/claude-sonnet-4-5
  effort: high
  ---
  Review the change you are given. Report bugs with file:line, most serious first.
  ```

  The body is added to the subagent's system prompt; the names and descriptions are listed in the parent's. `atto context` shows them too.
- `subagents.maxConcurrent` (default 3) caps the turns one session runs at once; the rest wait in a queue (`list` shows them `queued`). Subagents can't start subagents of their own (`ATTO_SUBAGENT` is set in their commands' environment).

**Front end and back end are separate.** Both servers speak the same JSON-RPC protocol, built around threads, turns and items:

- `atto serve` serves it over HTTP + SSE and includes a web client, so you can use atto from a phone. Listening beyond this machine (`-listen 0.0.0.0:7878`), it prints the link with a QR code to scan.
- `atto app-server` serves it over stdio.
- `/remote` in the TUI serves the session you are in, with the same protocol and web client: the browser shows the conversation as it streams, and what you send from it goes in as if typed (a turn, or a steer while one runs); Stop, Background, model and effort work too. `/clear` and `/resume` take the browser along. Each `/remote on` makes a new token, so `/remote off` revokes the link; quitting atto stops it. There is no TLS: use it on a network you trust (or Tailscale).

The web client shows what the terminal does: commands the model ran one after another fold into one line (`toolGroups` in `settings.json` applies), the status line sits under the input (model, context, cache hit rate, ↑/↓ tokens, cost) with the activity line above it while a turn runs (its time and tokens, orange when the model has been quiet for a while), and steers or queued messages not taken yet are listed above the input, where they can be edited or dropped. The ⋯ menu starts a new conversation, compacts, undoes the last turn (its message returns to the input), switches model and effort, and opens the session's background jobs (their output, and Stop) and subagents (their reports and, read only, their transcripts). The protocol is documented in `server/protocol.go`.

## Safety

atto has **no permission prompts**. The model's commands run with your user's permissions. The only filters are hooks you configure.

For untrusted repositories or long unattended runs, run atto in a VM or container.

Commands that atto runs get `ATTO_AGENT=1` in their environment. With it set, atto refuses to start another agent, change credentials, or replace itself. This guards against accidents. It is not a sandbox.

## Configuration

Everything lives in `~/.atto`. Set `ATTO_DIR` to move it.

| Path | Contents |
| --- | --- |
| `settings.json` | default model and effort, renderer, `mouse`, `toolGroups` (`false`: no command groups), `spinnerVerbs` (the word the activity line shows while commands run, drawn once per turn: `en`, the default, made-up English verbs; `ko`, made-up Korean words, as `글벅거리는 중…`; `ko-literary`, Korean verbs; `off`, just `Working…`), `spinnerScanner` (`true`: a sweeping `▰▱` scanner before that word), status line, hooks, `updateCheck`, `doubleEscapeAction` (`tree`, `fork` or `none`), `branchSummary.skipPrompt`, `toolOutputTokenLimit` (how much of a command's output the model gets, default 10000 tokens; the middle is cut and the full output saved to a file, as in codex), `backgroundExit` (experimental: `false` turns off the exit menu that offers "Run in background" while a turn runs), `remote.port` (`/remote`'s port, default 7879), `extensions` (`disabled` names, handler `timeout` in seconds), `skills.disabled` (built-in skills to turn off), `subagents` (`enabled`, `maxConcurrent`, `model`, `effort`) |
| `agents/` | subagent presets (`<name>.md`); `subagents/` holds the state of the subagents each session started |
| `mcp.json` | MCP servers (Claude Code's `.mcp.json` format); `mcp-approvals.json` holds approved project servers, `mcp/` the endpoints of running sessions |
| `extensions/` | your extensions; `extension-approvals.json` holds approved project extensions, `extensions.log` their logs |
| `models.json` | your providers and models |
| `auth.json` | keys and logins (mode 0600) |
| `sessions/` | saved sessions |
| `images/` | images sent in sessions, by content hash |

### Troubleshooting

If text looks garbled, doubled or leaves fragments behind (seen with wide characters such as Korean in Windows Terminal and other ConPTY hosts), atto can repaint every visible row on each frame instead of only the changed ones. This is on by default on Windows. Set `ATTO_FULL_REPAINT=1` to force it on, or `ATTO_FULL_REPAINT=0` to force it off, on any OS. While atto works, the line above the input animates at about 30 frames a second (only that line is rewritten); full repaint animates it every 250ms instead.

The activity line's colors blend in 24-bit color when the terminal says it can (`COLORTERM=truecolor` or `24bit`, Windows Terminal, iTerm2, WezTerm, VS Code, Ghostty) and are rounded to the 256-color palette otherwise; on the Linux console and other 16-color terminals it is plain ASCII. Its teal turns amber when the model has sent nothing for 15 seconds (while no command runs), and back when output arrives.

## Development

```sh
go install ./cmd/atto          # this machine
scripts/deploy.sh win linux    # other machines over ssh, no GitHub involved
```

Enable the pre-commit checks (gofmt, vet, modernize, tidy, web dist) with `git config core.hooksPath .githooks`.

The web client (`server/web`) is Preact and TypeScript styled with Tailwind; its built bundle in `server/web/dist` is committed, so `go build` needs nothing else. After changing `server/web/src`, run `go generate ./server/web` (it downloads the pinned Tailwind standalone CLI and Preact once, checking their SHA-256, and bundles with esbuild; no Node) and commit `dist/`; a test fails while `dist/` is stale.

`scripts/deploy.sh` builds an edge binary of this checkout for each host's system and installs it where the install scripts would (`%LOCALAPPDATA%\Programs\atto` on Windows, `~/.local/bin` elsewhere), so `atto update` there keeps following edge. Its version ends in `.local`.

## License

[MIT](LICENSE)
