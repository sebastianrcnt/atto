// The live session's goal under the header (goal/updated): the status
// line's indicator in the terminal's magenta, the objective, and on a tap
// the summary with the /goal commands that apply to its status.

import { useState } from "react";
import type { GoalInfo } from "../types";
import { Chevron, Target } from "./icons";

export type GoalAction = "pause" | "resume" | "edit" | "clear";

function actions(status: string, held?: boolean): GoalAction[] {
  switch (status) {
    case "active":
      return held ? ["resume", "pause", "edit", "clear"] : ["pause", "edit", "clear"];
    case "paused":
    case "blocked":
    case "usage_limited":
      return ["resume", "edit", "clear"];
  }
  return ["edit", "clear"];
}

const names: Record<GoalAction, string> = { pause: "Pause", resume: "Resume", edit: "Edit", clear: "Clear" };

export default function GoalBar({ goal, onAction }: { goal: GoalInfo; onAction: (a: GoalAction) => void }) {
  const [open, setOpen] = useState(false);
  const tone = "text-magenta"; // the terminal draws the indicator in magenta whatever the status
  return (
    <div className="shrink-0 border-b border-line bg-page">
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="mx-auto flex h-10 w-full max-w-[820px] min-w-0 items-center gap-2 px-4 text-left">
        <Target size={14} className={tone} />
        <span className={`shrink-0 text-[12.5px] font-medium ${tone}`}>{goal.indicator || "Goal " + goal.statusLabel}</span>
        <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-3">{goal.objective}</span>
        <Chevron size={14} className={`text-ink-3 transition-transform duration-200 ${open ? "rotate-180" : ""}`} />
      </button>
      {open && (
        <div className="mx-auto w-full max-w-[820px] px-4 pb-3" style={{ animation: "fade-in 160ms ease-out both" }}>
          <div className="rounded-card bg-surface p-3 shadow-card">
            <div className="flex items-center gap-1.5 text-[12px]">
              <span className="rounded-chip bg-magenta-tint px-1.5 py-0.5 font-medium text-magenta">{goal.statusLabel}</span>
              <span className="text-ink-3">
                {goal.tokens} tokens · {goal.elapsed}
              </span>
            </div>
            <p className="mt-2 text-[13.5px] leading-normal whitespace-pre-wrap text-ink" style={{ overflowWrap: "anywhere" }}>
              {goal.objective}
            </p>
            {goal.note && <p className="mt-1.5 text-[12.5px] leading-snug text-ink-3">{goal.note}</p>}
            <div className="mt-3 flex flex-wrap gap-2">
              {actions(goal.status, goal.held).map((a) => (
                <button
                  key={a}
                  type="button"
                  onClick={() => onAction(a)}
                  className={`h-9 rounded-control px-3.5 text-[13px] font-medium shadow-btn active:scale-[0.97] ${a === "clear" ? "bg-surface text-red" : "bg-surface text-ink"}`}
                >
                  {names[a]}
                </button>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
