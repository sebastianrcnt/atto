// The session's background jobs, as the terminal's /jobs lists them: each
// job's label, kind, status and run time; a tap shows the tail of its
// output, followed while it runs, and stops it.

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { duration } from "../format";
import type { Client } from "../rpc";
import type { Job } from "../types";
import { Chevron, Refresh, Stop } from "./icons";
import Sheet, { Confirm } from "./Sheet";

export const active = (j: Job) => j.status === "starting" || j.status === "running";

function runtime(j: Job, now: number) {
  if (active(j)) return duration(Math.max(0, now - j.started));
  return j.runtimeMs < 1000 ? "<1s" : duration(j.runtimeMs); // the job's time is in seconds
}

// statusText is the status as /jobs words it: "exited (2)".
function statusText(j: Job) {
  return j.status + (j.exitCode != null ? ` (${j.exitCode})` : "");
}

function tone(j: Job) {
  if (active(j)) return "bg-green";
  if (j.status === "exited" && j.exitCode === 0) return "bg-line-strong";
  if (j.status === "killed") return "bg-orange";
  return "bg-red";
}

export function Dot({ className }: { className: string }) {
  return <span className={`size-2 shrink-0 rounded-full ${className}`} />;
}

function useNow(on: boolean) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [on]);
  return now;
}

export default function JobsPanel({
  client,
  threadId,
  jobs,
  onRefresh,
  onClose,
  fail,
}: {
  client: Client;
  threadId: string;
  jobs: Job[];
  onRefresh: () => void;
  onClose: () => void;
  fail: (e: unknown) => void;
}) {
  const [open, setOpen] = useState<number | null>(null);
  const job = open == null ? undefined : jobs.find((j) => j.id === open);
  const now = useNow(jobs.some(active));
  if (job) return <JobView client={client} threadId={threadId} job={job} now={now} onBack={() => setOpen(null)} onClose={onClose} onRefresh={onRefresh} fail={fail} />;
  return (
    <Sheet
      title="Background jobs"
      subtitle={jobs.length ? `${jobs.filter(active).length} running · ${jobs.length} in this session` : undefined}
      onClose={onClose}
      actions={
        <button type="button" aria-label="Refresh" onClick={onRefresh} className="flex size-9 items-center justify-center rounded-control text-ink-2 hover:bg-hover">
          <Refresh size={16} />
        </button>
      }
    >
      {jobs.length === 0 && <p className="px-3 py-3 text-[13.5px] text-ink-3">No background jobs. The agent starts them with atto job start.</p>}
      {[...jobs].reverse().map((j) => (
        <button
          key={j.id}
          type="button"
          onClick={() => setOpen(j.id)}
          className="flex min-h-12 w-full items-center gap-3 rounded-control px-3 py-2 text-left transition-colors hover:bg-hover active:bg-hover-2"
        >
          <Dot className={tone(j)} />
          <span className="min-w-0 flex-1">
            <span className="block truncate text-[14px] text-ink">
              <span className="font-mono text-[12.5px] text-ink-3">{j.id}</span> {j.label}
            </span>
            <span className="block truncate text-[12px] text-ink-3">
              {statusText(j)}
              {j.kind !== "job" && " · " + j.kind}
              {j.error && " · " + j.error}
            </span>
          </span>
          <span className="shrink-0 font-mono text-[12px] text-ink-3 tabular-nums">{runtime(j, now)}</span>
          <Chevron size={14} className="shrink-0 -rotate-90 text-ink-3" />
        </button>
      ))}
    </Sheet>
  );
}

function JobView({
  client,
  threadId,
  job,
  now,
  onBack,
  onClose,
  onRefresh,
  fail,
}: {
  client: Client;
  threadId: string;
  job: Job;
  now: number;
  onBack: () => void;
  onClose: () => void;
  onRefresh: () => void;
  fail: (e: unknown) => void;
}) {
  const [output, setOutput] = useState<string | null>(null);
  const [ask, setAsk] = useState(false);
  const running = active(job);
  // The tail stays in view unless the reader scrolled up.
  const pre = useRef<HTMLPreElement>(null);
  const atEnd = useRef(true);
  useLayoutEffect(() => {
    const el = pre.current;
    if (el && atEnd.current) el.scrollTop = el.scrollHeight;
  }, [output]);
  // The tail, again every two seconds while it runs.
  useEffect(() => {
    let stop = false;
    const read = () =>
      client
        .call<{ output: string }>("job/output", { threadId, job: job.id, lines: 200 })
        .then((r) => !stop && setOutput(r.output))
        .catch((e) => !stop && setOutput("(" + String((e as Error).message || e) + ")"));
    read();
    const t = running ? setInterval(read, 2000) : undefined;
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [client, threadId, job.id, running]);

  const stopJob = async () => {
    setAsk(false);
    try {
      await client.call("job/stop", { threadId, job: job.id });
    } catch (e) {
      fail(e);
    }
    onRefresh();
  };

  return (
    <>
      <Sheet
        title={
          <span className="flex min-w-0 items-center gap-1">
            <button type="button" aria-label="Back" onClick={onBack} className="-ml-2 flex size-8 shrink-0 items-center justify-center rounded-control text-ink-2 hover:bg-hover">
              <Chevron size={16} className="rotate-90" />
            </button>
            <span className="truncate">
              <span className="font-mono text-[13px] font-normal text-ink-3">{job.id}</span> {job.label}
            </span>
          </span>
        }
        subtitle={`${statusText(job)} · ${runtime(job, now)}${job.kind !== "job" ? " · " + job.kind : ""}`}
        onClose={onClose}
        actions={
          running && (
            <button type="button" onClick={() => setAsk(true)} className="mr-1 flex h-8 items-center gap-1.5 rounded-control bg-surface px-3 text-[13px] font-medium text-red shadow-btn active:scale-[0.97]">
              <Stop size={11} /> Stop
            </button>
          )
        }
      >
        <div className="px-2">
          <div className="mb-2 rounded-chip bg-field px-2 py-1.5 font-mono text-[12px] break-all whitespace-pre-wrap text-ink-2 shadow-hairline">$ {job.command}</div>
          {job.error && <p className="mb-2 text-[12.5px] text-red">{job.error}</p>}
          <pre
            ref={pre}
            onScroll={(e) => {
              const el = e.currentTarget;
              atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < 8;
            }}
            className="m-0 max-h-[55vh] overflow-auto rounded-card bg-inset px-3 py-2.5 font-mono text-[12px] leading-[1.6] whitespace-pre-wrap text-ink-2 shadow-hairline" style={{ overflowWrap: "anywhere" }}>
            {output == null ? "…" : output || "(no output)"}
          </pre>
          <p className="mt-1.5 px-1 text-[11.5px] text-ink-3">The last 200 lines{running ? ", following" : ""}. All of it: atto job output {job.id}</p>
        </div>
      </Sheet>
      {ask && (
        <Confirm
          title={`Stop job ${job.id}?`}
          body={`${job.label}: its process and everything it started end. The agent is not told.`}
          action="Stop"
          danger
          onCancel={() => setAsk(false)}
          onConfirm={stopJob}
        />
      )}
    </>
  );
}
