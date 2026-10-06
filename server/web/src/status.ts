// The status line (app/statusline.go's builtinStatus) and the activity
// line (app/activity.go), from what the server says of the thread
// (tested from Go: server/web/transcript_test.go).

import { compact, tokens } from "./format";
import type { Item, ThreadInfo, TurnInfo, Usage } from "./types";

export type Status = {
  model: string;
  effort: string;
  // context: percent of the window, "31k/262k", and whether it nears
  // auto-compaction (80% of its limit)
  pct: number;
  context: string;
  warn: boolean;
  cache: string; // "cache 93%", for the latest request
  io: string; // "↑12k ↓3.4k": input the cache did not serve, output
  writes: string; // "W2k": tokens written to the cache
  cost: string; // "$0.123", "≈$0.123" on a subscription
};

export function status(t: ThreadInfo): Status {
  const u: Usage = t.usage || { inputTokens: 0, cachedInputTokens: 0, outputTokens: 0 };
  const s: Status = { model: t.modelName || t.model, effort: "", pct: 0, context: "", warn: false, cache: "", io: "", writes: "", cost: "" };
  if (t.effort && t.efforts?.length) s.effort = t.effort;
  const cw = t.contextWindow || 0;
  if (cw > 0) {
    s.pct = Math.floor((t.contextTokens * 100) / cw);
    s.context = `${tokens(t.contextTokens)}/${tokens(cw)}`;
    const limit = t.autoCompactLimit || 0;
    s.warn = limit > 0 && (t.contextTokens * 100) / limit >= 80;
  }
  if (u.lastInputTokens) s.cache = `cache ${Math.floor(((u.lastCachedInputTokens || 0) * 100) / u.lastInputTokens)}%`;
  const fresh = Math.max(0, u.inputTokens - u.cachedInputTokens - (u.cacheWriteTokens || 0));
  const io: string[] = [];
  if (fresh > 0) io.push("↑" + compact(fresh));
  if (u.outputTokens > 0) io.push("↓" + compact(u.outputTokens));
  s.io = io.join(" ");
  if (u.cacheWriteTokens) s.writes = "W" + compact(u.cacheWriteTokens);
  // Only a model with prices has a cost; on a subscription it only
  // estimates what the usage would cost over the API.
  if (t.priced || (u.cost || 0) > 0) s.cost = (t.subscription ? "≈" : "") + "$" + (u.cost || 0).toFixed(3);
  return s;
}

// The line turns toward the stall colour when the model has sent nothing
// for STALL_AFTER ms (no command running), over STALL_RAMP.
export const STALL_AFTER = 15000;
export const STALL_RAMP = 5000;

// Meter counts a turn's tokens for the activity line: what the server
// reported after each response, plus what the response in progress has
// streamed so far at about 4 characters a token (thinking, text and the
// commands being written).
export class Meter {
  startedAt = 0;
  verb = "";
  input = 0;
  output = 0;
  lastEvent = 0;
  private chars = 0;
  private drafts = new Map<string, number>();

  start(t: TurnInfo | undefined, now: number) {
    this.startedAt = t?.startedAt || now;
    this.verb = t?.verb || "";
    this.input = t?.inputTokens || 0;
    this.output = t?.outputTokens || 0;
    this.chars = 0;
    this.drafts.clear();
    this.lastEvent = now;
  }

  // step: a response ended (thread/usage's step).
  step(u: Usage) {
    this.input += Math.max(0, u.inputTokens - u.cachedInputTokens - (u.cacheWriteTokens || 0));
    this.output += u.outputTokens;
    this.chars = 0;
    this.drafts.clear();
  }

  // streamed text of reasoning or an answer
  text(n: number) {
    this.chars += n;
  }

  // a command the model is still writing, as it stands
  draft(it: Item) {
    this.drafts.set(it.id, (it.description || "").length + (it.command || "").length);
  }

  // out is the turn's output so far, the response in progress estimated.
  out(): number {
    let n = this.chars;
    this.drafts.forEach((c) => (n += c));
    return this.output + Math.floor(n / 4);
  }

  // stall is how far toward the stall colour, 0 to 1.
  stall(now: number, running: boolean): number {
    if (running) return 0;
    return Math.max(0, Math.min(1, (now - this.lastEvent - STALL_AFTER) / STALL_RAMP));
  }
}

// activity is what the turn is doing, as the terminal words it: the
// turn's verb (or "Working") while a command is written or runs.
export function activity(last: Item | undefined, running: boolean, verb: string): string {
  if (last?.type === "compaction" && last.status === "inProgress") return "Compacting context";
  if (running || (last?.type === "commandExecution" && last.pending)) return verb || "Working";
  return "Thinking";
}
