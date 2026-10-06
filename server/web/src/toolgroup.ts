// Command groups, as the terminal shows them (app/toolgroup.go): the
// commands the model ran one after another, reasoning between them
// included, are a run (see Transcript). Two or more commands group: the
// ones before the last fold into one summary line, and the last, those
// still running and those that failed stay shown, as does reasoning
// after the last. Opening the group shows every member.

import { duration } from "./format";
import type { Item } from "./types";

const isCommand = (it: Item) => it.type === "commandExecution";

export function done(it: Item): boolean {
  return !it.pending && it.status !== "inProgress";
}

// failed: an error, a non-zero exit or a timeout.
export function failed(it: Item): boolean {
  return done(it) && (it.status === "failed" || it.timedOut === true || (it.exitCode != null && it.exitCode !== 0));
}

// label is what the summary says of a command: its description, or its
// command's first line.
export function label(it: Item): string {
  const d = (it.description || "").split(/\s+/).filter(Boolean).join(" ");
  if (d) return d;
  const c = (it.command || "").split("\n")[0].trim();
  return c || "Preparing command";
}

export type Plan = {
  grouped: boolean;
  // the summary line's parts, when grouped
  count: number;
  ms: number;
  failed: number;
  labels: string[];
  // the members shown, in order
  shown: Item[];
};

export function plan(members: Item[], on: boolean, open: boolean): Plan {
  const calls = members.filter(isCommand);
  const p: Plan = { grouped: on && calls.length >= 2, count: 0, ms: 0, failed: 0, labels: [], shown: members };
  if (!p.grouped) return p;
  const sum = calls.slice(0, -1);
  p.count = sum.length;
  for (const c of sum) {
    p.ms += c.durationMs || 0;
    if (failed(c)) p.failed++;
    p.labels.push(label(c));
  }
  if (open) return p;
  let last = -1;
  members.forEach((m, i) => isCommand(m) && (last = i));
  p.shown = members.filter((m, i) => (isCommand(m) ? i === last || !done(m) || failed(m) : i > last));
  return p;
}

// head is the summary line: "3 commands · 4.2s · 1 failed".
export function head(p: Plan): string {
  let s = `${p.count} ${p.count === 1 ? "command" : "commands"} · ${duration(p.ms)}`;
  if (p.failed) s += ` · ${p.failed} failed`;
  return s;
}
