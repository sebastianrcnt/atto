// A run of commands as the terminal's group (app/toolgroup.go, see
// toolgroup.ts): the calls before the last fold into one summary line,
// "▸ 3 commands · 4.2s · 1 failed  Read the config, Run the tests"; the
// last call, calls still running or failed and reasoning after the last
// stay shown. A tap on the line shows every call on a shaded panel.

import { memo, useState, type ReactNode } from "react";
import { duration } from "../format";
import { plan } from "../toolgroup";
import type { Run } from "../transcript";
import type { Item } from "../types";
import { Chevron } from "./icons";

export default memo(function ToolGroup({ run, on, item }: { run: Run; on: boolean; item: (it: Item) => ReactNode }) {
  const [open, setOpen] = useState(false);
  const p = plan(run.members, on, open);
  const members = <div className="flex flex-col gap-3">{p.shown.map(item)}</div>;
  if (!p.grouped) return members;
  return (
    <div className="flex w-full flex-col">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="-mx-1.5 flex min-h-9 w-[calc(100%+12px)] min-w-0 items-center gap-2 rounded-control px-1.5 text-left text-[13px] transition-colors duration-100 hover:bg-hover-2"
      >
        <Chevron size={13} className="text-ink-3 transition-transform duration-200" style={{ transform: open ? "rotate(0deg)" : "rotate(-90deg)" }} />
        <span className="shrink-0 font-medium text-ink-2 tabular-nums">
          {p.count} {p.count === 1 ? "command" : "commands"} · {duration(p.ms)}
          {p.failed > 0 && <span className="text-red"> · {p.failed} failed</span>}
        </span>
        <span className="min-w-0 truncate text-ink-3">{p.labels.join(", ")}</span>
      </button>
      {open ? (
        <div className="mt-1 rounded-card bg-inset px-3 py-2 shadow-hairline" style={{ animation: "fade-in 160ms ease-out both" }}>
          {members}
        </div>
      ) : (
        <div className="mt-3">{members}</div>
      )}
    </div>
  );
});
