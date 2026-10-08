// Types of the atto extension API. atto writes this file next to your
// extensions; reference it from an extension with
//
//   /// <reference path="./atto.d.ts" />      (or "../atto.d.ts" from a folder)
//
// and write `export default function (atto: Atto) { ... }`.
// `atto extensions types` prints it; `atto extensions docs` prints the guide.

/** What atto passes to handlers and commands as their second argument. */
interface AttoContext {
  ui: AttoUI;
  /** True in the interactive TUI; false in atto -p and the server. */
  hasUI: boolean;
  /** The session's working directory. */
  cwd: string;
  session: AttoSession;
}

interface AttoSession {
  /** The session ID (changes on /clear and /resume). */
  readonly id: string;
  /** provider/id of the model in use, "" without one. */
  readonly model: string;
  readonly cwd: string;
  /** The session's name ("" without one), read from its file. */
  readonly name: string;
  /**
   * The conversation's text so far, oldest first: the user's messages and
   * the model's answers on the current branch, without commands and their
   * output. The last `limit` (default 50).
   */
  messages(limit?: number): { role: "user" | "assistant"; text: string }[];
  /** Names the session, as /name does. Throws in atto -p. */
  setName(name: string): void;
}

interface AttoUI {
  /**
   * A short dim suffix on the header of an assistant block ("translating…"),
   * or null to remove yours. Display only; TUI only (a no-op in atto -p and
   * the server). A block that no longer exists is ignored.
   */
  setBlockStatus(blockId: string, text: string | null): void;
  /**
   * Show text (Markdown) in place of an assistant block's own, or null to
   * restore it. atto adds a line to flip to the original (click or ctrl+o).
   * Display only: the model's context and the session's messages never
   * change. Saved in the session file; TUI only.
   */
  setBlockDisplay(blockId: string, text: string | null): void;
  /**
   * Add a collapsible block to the transcript that shows text under a title.
   * Display only: the model never sees it, and it is saved in the session
   * file, so a resumed session shows it again. Long text shows its first
   * `preview` lines and a "+N lines" row that expands it (click or ctrl+t).
   * The TUI shows a block; atto -p prints the title and text as a notice.
   * See the built-in /diff extension (extensions/builtin/diff.ts).
   */
  showText(title: string, text: string, options?: AttoShowTextOptions): void;
  /** Show a notice in the transcript (stderr in atto -p). */
  notify(text: string, level?: "info" | "warning" | "error"): void;
  /** Set (or with null, remove) an item of the status line. TUI only. */
  setStatus(key: string, text: string | null): void;
  /** Set (or with null, remove) a band of lines above the input. TUI only. */
  setWidget(key: string, lines: string[] | null): void;
  /** Let the user pick one option; undefined when canceled, and always without a UI. */
  select(title: string, options: string[]): Promise<string | undefined>;
  /** Ask yes or no; false when canceled, and always without a UI. */
  confirm(text: string): Promise<boolean>;
  /** Ask for a line of text; undefined when canceled, and always without a UI. */
  input(prompt: string): Promise<string | undefined>;
}

interface AttoShowTextOptions {
  /**
   * How to colour the text: "diff" (+ green, - red, @@ cyan, file headers
   * dim). Anything else, or nothing, is plain text.
   */
  lang?: string;
  /** Lines shown while collapsed (default 10). */
  preview?: number;
}

interface AttoSessionEvent {
  /** session_start: "startup" | "resume" | "clear"; session_end: "exit" | "clear" | "resume" | "other". */
  reason: string;
}

interface AttoTurnStartEvent {
  prompt: string;
}

interface AttoTurnEndEvent {
  /** Why the turn failed, or null. */
  error: string | null;
  /** The user interrupted it. */
  aborted: boolean;
}

interface AttoToolCallEvent {
  /** "bash" or "powershell": atto's one tool. */
  toolName: string;
  command: string;
  description: string;
  /** Seconds the model asked for; 0 is the default. */
  timeout: number;
  background: boolean;
}

/** Block the call (the model is told reason), or replace its command. */
type AttoToolCallResult = { block: true; reason?: string } | { command: string } | void;

interface AttoToolResultEvent {
  toolName: string;
  command: string;
  description: string;
  /** The output as the model would receive it. */
  output: string;
  exitCode: number;
  timedOut: boolean;
  canceled: boolean;
  durationMs: number;
  /** The background job the command became, or 0. */
  job: number;
}

/** A string (or {output}) replaces the output the model receives. */
type AttoToolResultResult = string | { output: string } | void;

interface AttoUserPromptEvent {
  prompt: string;
}

/** A string (or {context}) is added to the prompt; {block: true} rejects it. */
type AttoUserPromptResult = string | { context?: string; block?: boolean; reason?: string } | void;

interface AttoBlockEvent {
  /** Stable for the session's life, also after a resume. */
  blockId: string;
  /** The block's full text. */
  text: string;
  /** provider/id of the model that wrote it. */
  model: string;
}

interface AttoStepEndEvent {
  /** provider/id of the model that answered. */
  model: string;
  /** Tokens as the provider reported them; 0 when it reported none. */
  promptTokens: number;
  cachedTokens: number;
  /** Output tokens, reasoning included. */
  outputTokens: number;
  /** US dollars; 0 for a model without prices. */
  cost: number;
  /** Estimated context size after the response. */
  contextTokens: number;
  /** Request sent → first streamed output (reasoning, text or tool call). */
  ttftMs: number;
  /** First streamed output → end of the stream; 0 when nothing streamed. */
  genMs: number;
}

type Awaitable<T> = T | Promise<T>;

interface AttoEvents {
  session_start: [AttoSessionEvent, void];
  session_end: [AttoSessionEvent, void];
  turn_start: [AttoTurnStartEvent, void];
  turn_end: [AttoTurnEndEvent, void];
  tool_call: [AttoToolCallEvent, AttoToolCallResult];
  tool_result: [AttoToolResultEvent, AttoToolResultResult];
  user_prompt: [AttoUserPromptEvent, AttoUserPromptResult];
  /** An assistant text block finished. Never waited for, no timeout. */
  message_end: [AttoBlockEvent, void];
  /** A reasoning block finished. Never waited for, no timeout. */
  reasoning_end: [AttoBlockEvent, void];
  /**
   * A model response finished (one per model request in a turn). Never
   * waited for, no timeout. outputTokens / genMs * 1000 is tokens/s.
   */
  step_end: [AttoStepEndEvent, void];
}

interface AttoCompleteOptions {
  /** "provider/id" of a model in models.json. */
  model: string;
  prompt: string;
  system?: string;
  maxTokens?: number;
  /**
   * One of the model's effort levels, or sent as reasoning_effort for
   * chat-completions models ("none" turns thinking off on LM Studio).
   */
  reasoningEffort?: string;
  /** Milliseconds from the start of the request; default 30000. */
  timeoutMs?: number;
}

interface AttoExecResult {
  stdout: string;
  stderr: string;
  /** Exit code; -1 when killed at the timeout. */
  code: number;
  killed: boolean;
}

interface AttoResponse {
  status: number;
  ok: boolean;
  /** Lower-case header names. */
  headers: Record<string, string>;
  text(): Promise<string>;
  json(): Promise<any>;
}

interface AttoFetchOptions {
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  /** Milliseconds; default 30000. */
  timeout?: number;
}

interface AttoMcpContent {
  /** "text", "image", "audio", "resource_link" or "resource". */
  type: string;
  /** The text, or a note for what is not text. */
  text?: string;
  mimeType?: string;
  uri?: string;
  bytes?: number;
}

interface AttoMcpResult {
  /** The result as the model reads it: text as is, other content summarized in brackets. */
  text: string;
  /** The tool reported an error. */
  isError?: boolean;
  content?: AttoMcpContent[];
  structured?: any;
}

interface AttoMcpTool {
  server: string;
  name: string;
  description?: string;
  /** The tool's JSON schema for its arguments. */
  inputSchema?: any;
}

interface Atto {
  /** The extension's name (its file or folder name). */
  readonly name: string;
  readonly cwd: string;
  readonly session: AttoSession;
  /** Same as ctx.ui, for use outside handlers (timers, onDispose). */
  readonly ui: AttoUI;

  /**
   * Handle an event. tool_call, tool_result and user_prompt are waited for
   * (5 s by default; settings.json "extensions": {"timeout": seconds}), the
   * others are not (message_end and reasoning_end have no timeout at all).
   */
  on<K extends keyof AttoEvents>(
    event: K,
    handler: (event: AttoEvents[K][0], ctx: AttoContext) => Awaitable<AttoEvents[K][1]>,
  ): void;

  /** Add a slash command: /name args (TUI). */
  registerCommand(
    name: string,
    command: { description?: string; handler: (args: string, ctx: AttoContext) => Awaitable<void> },
  ): void;

  /** Run before the extension is unloaded (/reload, exit). Up to 1 s. */
  onDispose(fn: () => Awaitable<void>): void;

  /** Run a command with the agent's shell. timeout in ms (default 60000). */
  exec(command: string, options?: { cwd?: string; timeout?: number }): Promise<AttoExecResult>;

  /** Synchronous UTF-8 text files; relative paths are against the session's directory. */
  fs: {
    readFile(path: string): string;
    /** Creates missing parent directories. */
    writeFile(path: string, text: string): void;
    exists(path: string): boolean;
    /** Names in a directory, sorted; directories end in "/". */
    list(path: string): string[];
  };

  fetch(url: string, options?: AttoFetchOptions): Promise<AttoResponse>;

  /**
   * One reply from a model of models.json: no tools, no streaming, none of
   * the conversation. Rejects on a server that is down, a timeout (the
   * request is cancelled), an HTTP error or an unknown model, and when the
   * session ends or extensions reload. Counted per model in the Loaded block.
   */
  complete(options: AttoCompleteOptions): Promise<{ text: string }>;
  /** Requests of complete() in flight at once (1 to 16; default 1); others wait. */
  setCompleteConcurrency(n: number): void;

  /**
   * The session's MCP servers, the ones "atto mcp" reaches in the agent's
   * shell (configured in ~/.atto/mcp.json, the project's .mcp.json or
   * a private per-project file under ~/.atto). Servers start on first use and stay for the session.
   */
  mcp: {
    /**
     * Call a tool. Resolves with the result (check isError); rejects when
     * the call could not be made: unknown server or tool, a project server
     * not yet approved, a server that fails to start.
     */
    call(server: string, tool: string, args?: Record<string, any>): Promise<AttoMcpResult>;
    /** The tools of one server, or of every server that starts. */
    tools(server?: string): Promise<AttoMcpTool[]>;
  };

  /** Send text to the model as a user message: steers a running turn, or starts one (TUI). */
  sendMessage(text: string): void;

  /** Append to ~/.atto/extensions.log (console.log does the same). */
  log(...values: any[]): void;
}

declare function fetch(url: string, options?: AttoFetchOptions): Promise<AttoResponse>;
declare function setTimeout(fn: (...args: any[]) => void, ms?: number, ...args: any[]): number;
declare function clearTimeout(id: number | undefined): void;
declare function setInterval(fn: (...args: any[]) => void, ms?: number, ...args: any[]): number;
declare function clearInterval(id: number | undefined): void;
declare const console: { log(...v: any[]): void; info(...v: any[]): void; warn(...v: any[]): void; error(...v: any[]): void; debug(...v: any[]): void };
