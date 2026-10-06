// The activity line while a turn runs, as the terminal's (app/activity.go):
// what the turn is doing, its time, and its tokens so far: ↑ input the
// server had not cached, ↓ output, the response in progress estimated
// from what it streamed. When the model has sent nothing for a while (no
// command running) it turns toward orange, and back as soon as something
// arrives.

import { useEffect, useState } from "react";
import { compact, duration } from "../format";
import type { Meter } from "../status";
import { LoaderGrid } from "./Loading";
import { Shimmer } from "./Thinking";

const mix = (a: string, b: string, t: number) => `color-mix(in oklch, ${a}, ${b} ${Math.round(t * 100)}%)`;

export default function Activity({ label, meter, running }: { label: string; meter: Meter; running: boolean }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 100);
    return () => clearInterval(t);
  }, []);
  const stall = meter.stall(now, running);
  const out = meter.out();
  return (
    <div role="status" className="flex h-9 min-w-0 items-center gap-2.5 text-[13.5px]" title={stall > 0 ? "No output from the model for a while" : undefined}>
      <LoaderGrid color={mix("var(--ink)", "var(--orange)", stall)} />
      <Shimmer base={mix("var(--ink-3)", "var(--orange)", stall)} hi={mix("var(--ink)", "var(--orange)", stall)}>
        {label}…
      </Shimmer>
      <span className="min-w-0 truncate font-mono text-[12px] text-ink-3 tabular-nums">
        {duration(Math.floor((now - meter.startedAt) / 100) * 100)}
        {(out > 0 || meter.input > 0) && (
          <>
            {" · "}
            {meter.input > 0 && "↑ " + compact(meter.input) + "  "}
            {"↓ " + compact(out)}
            <span className="max-sm:hidden"> tokens</span>
          </>
        )}
      </span>
    </div>
  );
}
