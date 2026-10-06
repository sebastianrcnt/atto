// The session's subagents (atto agent): each one's name, status, preset
// and model, its latest turn's time and its task, as atto agent list
// shows them. A tap shows its report (its last message, with the turn's
// tokens and cost, as atto agent report prints it) and, read only, its
// own transcript.

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { compact, duration } from "../format";
import { Markdown } from "../markdown";
import type { Client } from "../rpc";
import { Transcript, type Row } from "../transcript";
import type { Item, Subagent } from "../types";
import { Chevron, Refresh } from "./icons";
import Sheet, { useEscape } from "./Sheet";
import { Shimmer } from "./Thinking";

export const busy = (a: Subagent) => a.status === "running" || a.status === "queued";

const tones: Record<Subagent["status"], string> = {
  idle: "bg-field text-ink-3",
  queued: "bg-field text-ink-2",
  running: "bg-accent-tint text-accent-ink",
  done: "bg-green-tint text-green",
  failed: "bg-red-tint text-red",
  stopped: "bg-orange-tint text-orange",
};

function Badge({ a }: { a: Subagent }) {
  return <span className={`shrink-0 rounded-chip px-1.5 py-0.5 text-[11.5px] font-medium ${tones[a.status] || tones.idle}`}>{a.status === "running" ? <Shimmer base="var(--accent-ink)" hi="var(--ink)">running</Shimmer> : a.status}</span>;
}

// took is the latest turn's time (so far, while it runs).
function took(a: Subagent) {
  return a.durationMs ? duration(a.durationMs) : "";
}

function firstLine(s: string) {
  const i = s.indexOf("\n");
  return i < 0 ? s : s.slice(0, i) + " …";
}

export default function SubagentsPanel({
  client,
  threadId,
  agents,
  view,
  onRefresh,
  onClose,
}: {
  client: Client;
  threadId: string;
  agents: Subagent[];
  // renders transcript rows as the main view does
  view: (rows: Row[]) => ReactNode;
  onRefresh: () => void;
  onClose: () => void;
}) {
  const [open, setOpen] = useState<string | null>(null);
  const agent = open == null ? undefined : agents.find((a) => a.name === open);
  if (agent) return <AgentView client={client} threadId={threadId} agent={agent} view={view} onBack={() => setOpen(null)} onClose={onClose} />;
  const running = agents.filter(busy).length;
  return (
    <Sheet
      title="Subagents"
      subtitle={`${running ? running + " working · " : ""}${agents.length} in this session`}
      onClose={onClose}
      actions={
        <button type="button" aria-label="Refresh" onClick={onRefresh} className="flex size-9 items-center justify-center rounded-control text-ink-2 hover:bg-hover">
          <Refresh size={16} />
        </button>
      }
    >
      {agents.length === 0 && <p className="px-3 py-3 text-[13.5px] text-ink-3">No subagents. The agent starts them with atto agent start.</p>}
      {agents.map((a) => (
        <button
          key={a.name}
          type="button"
          onClick={() => setOpen(a.name)}
          className="flex w-full items-start gap-3 rounded-control px-3 py-2.5 text-left transition-colors hover:bg-hover active:bg-hover-2"
        >
          <span className="min-w-0 flex-1">
            <span className="flex min-w-0 items-center gap-2">
              <span className="truncate text-[14.5px] font-medium text-ink">{a.name}</span>
              <Badge a={a} />
              <span className="ml-auto shrink-0 font-mono text-[12px] text-ink-3 tabular-nums">{took(a)}</span>
            </span>
            <span className="mt-0.5 block truncate text-[12px] text-ink-3">
              {a.preset} · {a.model}
              {a.effort ? " · " + a.effort : ""}
            </span>
            <span className="mt-0.5 block truncate text-[13px] text-ink-2">{firstLine(a.task)}</span>
          </span>
          <Chevron size={14} className="mt-1 shrink-0 -rotate-90 text-ink-3" />
        </button>
      ))}
    </Sheet>
  );
}

type Read = { message: string; items: Item[] };

function AgentView({
  client,
  threadId,
  agent,
  view,
  onBack,
  onClose,
}: {
  client: Client;
  threadId: string;
  agent: Subagent;
  view: (rows: Row[]) => ReactNode;
  onBack: () => void;
  onClose: () => void;
}) {
  const [read, setRead] = useState<Read | null>(null);
  const [error, setError] = useState("");
  const [full, setFull] = useState(false);
  const running = busy(agent);
  // Its session is written as it works: read again while it runs, and
  // once more when its turn ends.
  useEffect(() => {
    let stop = false;
    const load = () =>
      client
        .call<Read>("subagent/read", { threadId, name: agent.name })
        .then((r) => !stop && (setRead(r), setError("")))
        .catch((e) => !stop && setError(String((e as Error).message || e)));
    load();
    const t = running ? setInterval(load, 3000) : undefined;
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [client, threadId, agent.name, running, agent.turn]);

  const rows = useMemo(() => {
    const s = new Transcript();
    s.reset(read?.items || []);
    return s.blocks().flat();
  }, [read]);

  const usage = (agent.inputTokens || 0) + (agent.outputTokens || 0) > 0 && (
    <>
      {" · "}↑{compact(agent.inputTokens || 0)} ({compact(agent.cachedInputTokens || 0)} cached) ↓{compact(agent.outputTokens || 0)}
    </>
  );

  return (
    <>
      <Sheet
        title={
          <span className="flex min-w-0 items-center gap-2">
            <button type="button" aria-label="Back" onClick={onBack} className="-ml-2 flex size-8 shrink-0 items-center justify-center rounded-control text-ink-2 hover:bg-hover">
              <Chevron size={16} className="rotate-90" />
            </button>
            <span className="truncate">{agent.name}</span>
            <Badge a={agent} />
          </span>
        }
        subtitle={
          <>
            turn {agent.turn}
            {took(agent) && " · " + took(agent)}
            {usage}
            {agent.cost ? ` · ≈$${agent.cost.toFixed(3)}` : ""}
          </>
        }
        onClose={onClose}
      >
        <div className="flex flex-col gap-3 px-2 pb-2">
          <div className="text-[12.5px] text-ink-3">
            {agent.preset} · {agent.model}
            {agent.effort ? " · " + agent.effort : ""} · session <span className="font-mono">{agent.threadId.slice(0, 8)}</span>
          </div>
          {agent.error && <p className="rounded-control bg-red-tint px-3 py-2 text-[13px] text-red">{agent.error}</p>}
          <Field title={agent.turn > 1 ? "Latest message" : "Task"}>
            <p className="text-[13.5px] leading-normal whitespace-pre-wrap text-ink-2" style={{ overflowWrap: "anywhere" }}>
              {agent.turn > 1 ? agent.prompt : agent.task}
            </p>
          </Field>
          <Field title="Report">
            {error ? (
              <p className="text-[13px] text-red">{error}</p>
            ) : read == null ? (
              <p className="text-[13px] text-ink-3">…</p>
            ) : read.message ? (
              <div className="prose-atto text-ink">
                <Markdown text={read.message} />
              </div>
            ) : (
              <p className="text-[13px] text-ink-3">{running ? "No message yet." : "No message."}</p>
            )}
          </Field>
          {rows.length > 0 && (
            <button
              type="button"
              onClick={() => setFull(true)}
              className="flex h-10 items-center justify-center gap-1.5 rounded-control bg-surface text-[13.5px] font-medium text-ink-2 shadow-btn active:scale-[0.98]"
            >
              Transcript · {read?.items.length} items
            </button>
          )}
        </div>
      </Sheet>
      {full && <TranscriptView title={agent.name + "'s transcript"} rows={rows} view={view} onClose={() => setFull(false)} />}
    </>
  );
}

function Field({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <div className="mb-1 text-[11.5px] font-medium tracking-wide text-ink-3 uppercase">{title}</div>
      {children}
    </div>
  );
}

// TranscriptView shows a subagent's transcript full screen, read only.
function TranscriptView({ title, rows, view, onClose }: { title: string; rows: Row[]; view: (rows: Row[]) => ReactNode; onClose: () => void }) {
  useEscape(onClose);
  return (
    <div className="fixed inset-0 z-[55] flex flex-col bg-page" style={{ paddingTop: "env(safe-area-inset-top)", animation: "fade-in 160ms ease-out both" }}>
      <div className="flex h-12 shrink-0 items-center gap-2 border-b border-line px-3">
        <button type="button" aria-label="Back" onClick={onClose} className="-ml-1 flex size-10 items-center justify-center rounded-control text-ink-2 hover:bg-hover">
          <Chevron size={16} className="rotate-90" />
        </button>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[14px] font-medium text-ink">{title}</div>
          <div className="truncate text-[11.5px] text-ink-3">read only</div>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain" style={{ paddingBottom: "env(safe-area-inset-bottom)" }}>
        <div className="transcript mx-auto flex w-full max-w-[820px] flex-col gap-3 px-4 pt-4 pb-8">{view(rows)}</div>
      </div>
    </div>
  );
}
