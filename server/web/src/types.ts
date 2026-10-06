// The protocol's shapes (server/protocol.go).

export type ItemType =
  | "userMessage"
  | "reasoning"
  | "agentMessage"
  | "commandExecution"
  | "compaction"
  | "event"
  | "goal"
  | "hook"
  | "notice"
  | "goalStatus"
  | "branchSummary"
  | "extText"
  | "note"; // made by this client: errors and status lines

// What extensions show on a reasoning or agentMessage item: statuses for
// its header and a text shown in place of its own (by extension ext).
export type BlockDisplay = {
  statuses?: { ext: string; text: string }[];
  ext?: string;
  text?: string;
};

// What extensions show around the input: status items and widgets.
export type ExtensionUI = {
  status: { key: string; text: string }[];
  widgets: { key: string; lines: string[] }[];
};

export type Item = {
  id: string;
  type: ItemType;
  text?: string;
  status?: "inProgress" | "completed" | "failed";
  description?: string;
  command?: string;
  output?: string;
  exitCode?: number;
  durationMs?: number;
  timedOut?: boolean;
  job?: number;
  background?: string;
  pending?: boolean;
  images?: { name?: string; width?: number; height?: number }[]; // attached by atto view
  auto?: boolean;
  tokensBefore?: number;
  tokensAfter?: number;
  hookEvent?: string;
  blocked?: boolean;
  goalStatus?: string;
  blockId?: string;
  display?: BlockDisplay | null;
  // extText
  title?: string;
  ext?: string;
  lang?: string;
  preview?: number;
  tone?: "error" | "info"; // notes
};

export type ThreadInfo = {
  threadId: string;
  cwd: string;
  name?: string;
  model: string;
  effort: string;
  efforts?: string[];
  contextWindow?: number;
  contextTokens: number;
  busy: boolean;
  turnId?: string;
  items?: Item[];
  eventId?: number;
  live?: boolean;
  prompt?: Prompt;
  goal?: GoalInfo;
  extensionUi?: ExtensionUI;
  // what the status line shows (see Usage, TurnInfo, PendingInput)
  modelName?: string;
  autoCompactLimit?: number;
  priced?: boolean;
  subscription?: boolean;
  usage?: Usage;
  turn?: TurnInfo;
  pending?: PendingInput | null;
};

// Token usage: a session's totals, or one model response's. Input
// includes the cached and written tokens.
export type Usage = {
  inputTokens: number;
  cachedInputTokens: number;
  cacheWriteTokens?: number;
  outputTokens: number;
  cost?: number;
  lastInputTokens?: number;
  lastCachedInputTokens?: number;
};

// The running turn, as the activity line shows it.
export type TurnInfo = {
  startedAt: number;
  verb?: string;
  inputTokens: number;
  outputTokens: number;
};

// Input the running turn has not taken: steers, and in a live session
// follow-ups queued for after it.
export type PendingInput = { steers: string[]; queued?: string[] };

// A background job of the session (job/list).
export type Job = {
  id: number;
  label: string;
  kind: string;
  command: string;
  status: "starting" | "running" | "exited" | "killed" | "failed" | "lost";
  exitCode?: number;
  error?: string;
  started: number;
  runtimeMs: number;
};

// A subagent of the session (subagent/list).
export type Subagent = {
  name: string;
  preset: string;
  model: string;
  effort?: string;
  threadId: string;
  task: string;
  prompt: string;
  turn: number;
  status: "idle" | "queued" | "running" | "done" | "failed" | "stopped";
  durationMs?: number;
  error?: string;
  inputTokens?: number;
  cachedInputTokens?: number;
  outputTokens?: number;
  cost?: number;
  created: number;
};

// A picker or input open in the live session's terminal.
export type Prompt = {
  id: string;
  kind: "select" | "input";
  title: string;
  subtitle?: string;
  options?: { label: string; description?: string }[];
  selected: number;
  filterable?: boolean;
  total?: number;
  note?: string;
  text?: string;
  placeholder?: string;
};

// The live session's goal, worded as the terminal's status line.
export type GoalInfo = {
  objective: string;
  status: string;
  statusLabel: string;
  indicator: string;
  summary: string;
  note?: string;
  tokens: string;
  tokensUsed: number;
  budget?: number;
  elapsed: string;
  seconds: number;
};

export type ThreadSummary = {
  threadId: string;
  name?: string;
  preview?: string;
  cwd: string;
  updatedAt?: string;
  messages?: number;
  loaded?: boolean;
  live?: boolean;
};

export type Model = {
  id: string;
  name: string;
  contextWindow?: number;
  efforts?: string[];
  hasKey: boolean;
  images?: boolean;
};

export type Notification = { method: string; params: Record<string, any> };
