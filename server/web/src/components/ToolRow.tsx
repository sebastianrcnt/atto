// Adapted from Beautiful UI's ToolChips (https://www.beautifului.dev, MIT,
// Copyright (c) 2026 Shane Levine; see THIRD_PARTY_NOTICES): a compact
// row with an icon that turns into a chevron on hover, the label, a
// chip with the detail, and an expanding panel. Here one row is one
// shell command: its description, the command as the chip, its status,
// and the command and output in the panel.

import { useState } from "react";
import type { Item } from "../types";
import { CopyButton } from "./CodeBlock";
import { Chevron, Cross, Terminal } from "./icons";
import { Expand, Shimmer } from "./Thinking";

function seconds(ms?: number) {
  if (!ms) return "";
  return ms < 1000 ? "<1s" : (ms / 1000).toFixed(1) + "s";
}

function Status({ it }: { it: Item }) {
  if (it.pending || it.status === "inProgress") {
    return <span className="size-3 shrink-0 rounded-full border-[1.5px] border-line-strong border-t-ink-2" style={{ animation: "spin 700ms linear infinite" }} />;
  }
  if (it.job) {
    return <span className="shrink-0 rounded-chip bg-accent-tint px-1.5 py-0.5 font-mono text-[11px] text-accent-ink">job {it.job}</span>;
  }
  if (it.status === "failed") {
    return (
      <span className="flex shrink-0 items-center gap-1 font-mono text-[11.5px] text-red tabular-nums">
        <Cross size={11} />
        {it.timedOut ? "timeout" : it.exitCode != null && it.exitCode >= 0 ? it.exitCode : ""}
      </span>
    );
  }
  return <span className="shrink-0 font-mono text-[11.5px] text-ink-3 tabular-nums">{seconds(it.durationMs)}</span>;
}

// Output previews show the tail: what a command printed last matters most.
const PREVIEW = 4000;

export default function ToolRow({ it }: { it: Item }) {
  const [open, setOpen] = useState(false);
  const [all, setAll] = useState(false);
  const running = it.pending || it.status === "inProgress";
  const label = it.description || (it.pending ? "Writing command" : "Command");
  const first = (it.command || "").split("\n")[0];
  const out = it.output || "";
  const shown = all || out.length <= PREVIEW ? out : out.slice(-PREVIEW);
  return (
    <div className="w-full" style={{ animation: "fade-up 300ms cubic-bezier(0.23,1,0.32,1) both" }}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="group/row -mx-1.5 flex min-h-10 w-[calc(100%+12px)] min-w-0 items-center gap-2 rounded-control px-1.5 text-left transition-colors duration-100 hover:bg-hover-2"
      >
        <span className="relative flex size-4 shrink-0 items-center justify-center text-ink-3">
          <Terminal size={14} className={`transition-opacity duration-100 group-hover/row:opacity-0 ${open ? "opacity-0" : ""}`} />
          <Chevron
            size={13}
            className={`absolute transition-[opacity,transform] duration-150 group-hover/row:opacity-100 ${open ? "opacity-100" : "opacity-0"}`}
            style={{ transform: open ? "rotate(0deg)" : "rotate(-90deg)" }}
          />
        </span>
        <span className="max-w-[45%] shrink-0 truncate text-[13.5px] font-medium text-ink">{running ? <Shimmer>{label}</Shimmer> : label}</span>
        <span className="inline-flex h-6 min-w-0 flex-1 items-center truncate rounded-chip bg-field px-1.5 font-mono text-[12px] text-ink-2 shadow-hairline">
          <span className="truncate">{first || "…"}</span>
        </span>
        <Status it={it} />
      </button>
      {it.images?.map((im, i) => (
        <div key={i} className="ml-6 truncate font-mono text-[11.5px] text-ink-3">
          ▣ {im.name || "image"} {im.width}×{im.height}
        </div>
      ))}
      <Expand open={open}>
        <div className="mt-1 mb-2 ml-[7px] border-l border-line pl-3.5">
          <div className="overflow-hidden rounded-card bg-surface shadow-card">
            <div className="flex h-9 items-center gap-2 border-b border-line px-3 text-[12px] text-ink-3">
              <span className="truncate">
                {it.job ? `moved to the background as job ${it.job}` : it.exitCode != null ? `exit ${it.exitCode}` : running ? "running" : ""}
                {it.durationMs ? " · " + seconds(it.durationMs) : ""}
              </span>
              <CopyButton text={(it.command || "") + (out ? "\n\n" + out : "")} />
            </div>
            <pre className="m-0 max-h-[55vh] overflow-auto px-3 py-2.5 font-mono text-[12px] leading-[1.6] whitespace-pre-wrap text-ink-2" style={{ overflowWrap: "anywhere" }}>
              <span className="text-ink">$ {it.command}</span>
              {shown !== out && (
                <button type="button" onClick={() => setAll(true)} className="my-1 block rounded-chip px-1.5 py-1 text-[11.5px] text-accent-ink hover:bg-hover">
                  … {out.length - shown.length} earlier bytes, show all
                </button>
              )}
              {shown ? "\n" + shown : ""}
            </pre>
          </div>
        </div>
      </Expand>
    </div>
  );
}
