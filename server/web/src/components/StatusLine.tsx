// The status line under the input, as the terminal's built-in one
// (app/statusline.go): model · effort, the context bar with its share and
// size, the cache hit rate of the last request, ↑ uncached input and ↓
// output, cache writes and the cost. On a phone the model and effort
// (the pickers above show them) and the sizes give way.

import type { ReactNode } from "react";
import type { Status } from "../status";

const effortTone: Record<string, string> = { high: "text-orange", xhigh: "font-medium text-magenta", max: "font-medium text-magenta" };

function Dot({ wide }: { wide?: boolean }) {
  return <span className={`text-line-strong ${wide ? "max-sm:hidden" : ""}`}>·</span>;
}

export default function StatusLine({ s, extra }: { s: Status; extra?: ReactNode }) {
  const parts: ReactNode[] = [];
  const add = (key: string, node: ReactNode, wide = false) => {
    if (parts.length) parts.push(<Dot key={key + "-dot"} wide={wide} />);
    parts.push(
      <span key={key} className={`shrink-0 ${wide ? "max-sm:hidden" : ""}`}>
        {node}
      </span>,
    );
  };
  if (s.context) {
    const tone = s.warn ? "var(--orange)" : "var(--ink-3)";
    add(
      "ctx",
      <span className={`inline-flex items-center gap-1.5 ${s.warn ? "text-orange" : ""}`} title="Context used">
        <span className="relative h-[3px] w-10 overflow-hidden rounded-full bg-line-strong">
          <span className="absolute inset-y-0 left-0 rounded-full" style={{ width: Math.min(100, s.pct) + "%", background: tone }} />
        </span>
        {s.pct}%<span className="max-sm:hidden"> {s.context}</span>
      </span>,
    );
  }
  if (s.cache) add("cache", s.cache);
  if (s.io) add("io", s.io);
  if (s.writes) add("w", s.writes, true);
  if (s.cost) add("cost", s.cost);
  return (
    <div className="flex h-6 min-w-0 items-center gap-1.5 overflow-hidden px-1.5 text-[11.5px] whitespace-nowrap text-ink-3 tabular-nums">
      <span className="min-w-0 truncate max-sm:hidden">
        <span className="text-accent">◆</span> {s.model}
      </span>
      {s.effort && (
        <>
          <Dot wide />
          <span className={`shrink-0 max-sm:hidden ${effortTone[s.effort] || ""}`}>{s.effort}</span>
        </>
      )}
      {parts.length > 0 && <span className="w-1.5 shrink-0 max-sm:hidden" />}
      {parts}
      {extra && <span className="ml-auto flex shrink-0 items-center gap-1">{extra}</span>}
    </div>
  );
}
