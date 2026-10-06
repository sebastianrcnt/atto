// Adapted from Beautiful UI's ThinkingState (https://www.beautifului.dev,
// MIT, Copyright (c) 2026 Shane Levine; see THIRD_PARTY_NOTICES): the
// sparkle header with a shimmering label while working, a chevron, and
// a trace that expands on a grid-rows transition. Here the trace is the
// model's reasoning (or a compaction's notes) instead of scripted rows,
// and it opens only when tapped.

import { useState, type ReactNode } from "react";
import { Chevron, Sparkle } from "./icons";

// Shimmer sweeps a lighter band (hi) across text in base.
export function Shimmer({ children, base = "var(--ink-3)", hi = "var(--ink)" }: { children: ReactNode; base?: string; hi?: string }) {
  return (
    <span
      className="bg-clip-text font-medium whitespace-nowrap text-transparent"
      style={{
        backgroundImage: `linear-gradient(90deg, ${base} 35%, ${hi} 50%, ${base} 65%)`,
        backgroundSize: "200% 100%",
        animation: "shimmer-text 1.4s linear infinite",
      }}
    >
      {children}
    </span>
  );
}

export function Expand({ open, children }: { open: boolean; children: ReactNode }) {
  return (
    <div
      className="grid transition-[grid-template-rows,opacity] duration-400"
      style={{
        gridTemplateRows: open ? "1fr" : "0fr",
        opacity: open ? 1 : 0,
        transitionTimingFunction: "cubic-bezier(0.23, 1, 0.32, 1)",
      }}
    >
      <div className="min-h-0 overflow-hidden">{open ? children : null}</div>
    </div>
  );
}

export default function Thinking({
  working,
  active = "Thinking",
  done,
  icon,
  status,
  meta,
  children,
}: {
  working: boolean;
  active?: string;
  done: string;
  icon?: ReactNode;
  // status follows the label (extensions' statuses); meta is a line under
  // the header (the toggle to the original text).
  status?: ReactNode;
  meta?: ReactNode;
  children?: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const hasBody = children != null && children !== "";
  return (
    <div className="flex w-full flex-col">
      <button
        type="button"
        aria-expanded={open}
        disabled={!hasBody}
        onClick={() => setOpen((o) => !o)}
        className="-mx-1.5 flex min-h-9 w-fit max-w-full items-center gap-2 rounded-control px-1.5 py-1 text-[13.5px] transition-colors duration-100 enabled:hover:bg-hover-2"
      >
        <span className="flex shrink-0 transition-colors duration-200" style={{ color: working ? "var(--ink-2)" : "var(--ink-3)" }}>
          {icon ?? <Sparkle size={15} />}
        </span>
        <span role="status" className="min-w-0 truncate">
          {working ? (
            <Shimmer>{active}</Shimmer>
          ) : (
            <span className="font-medium text-ink-2" style={{ animation: "fade-in 350ms ease-out both" }}>
              {done}
            </span>
          )}
          {status}
        </span>
        {hasBody && (
          <Chevron
            size={14}
            className="text-ink-3 transition-transform duration-300"
            style={{ transform: open ? "rotate(180deg)" : "rotate(0)" }}
          />
        )}
      </button>
      {meta}
      <Expand open={open}>
        <div className="relative mt-1 ml-[7px] border-l border-line py-1 pl-4">
          <div className="text-[13.5px] leading-relaxed whitespace-pre-wrap text-ink-2" style={{ overflowWrap: "anywhere" }}>
            {children}
          </div>
        </div>
      </Expand>
    </div>
  );
}
