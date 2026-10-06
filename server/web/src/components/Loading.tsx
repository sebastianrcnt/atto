// Adapted from Beautiful UI's LoadingState (https://www.beautifului.dev,
// MIT, Copyright (c) 2026 Shane Levine; see THIRD_PARTY_NOTICES): the
// "Drive" pixel grid, a shimmering label and an elapsed timer in tabular
// figures.

import { useEffect, useState } from "react";
import { Shimmer } from "./Thinking";

const chevron = Array.from({ length: 9 }, (_, i) => {
  const r = Math.floor(i / 3),
    c = i % 3;
  return (c + Math.abs(r - 1)) * 90;
});

export function LoaderGrid({ color = "var(--ink)" }: { color?: string }) {
  return (
    <span aria-hidden className="grid shrink-0 grid-cols-[repeat(3,4px)] gap-[1.5px]">
      {chevron.map((delay, i) => (
        <span key={i} className="size-[4px] rounded-[1px]" style={{ background: color, opacity: 0.15, animation: `pixel-on 650ms ease-in-out ${delay}ms infinite` }} />
      ))}
    </span>
  );
}

function elapsed(since: number, now: number) {
  const total = Math.max(0, (now - since) / 1000);
  if (total < 60) return `${total.toFixed(1)}s`;
  return `${Math.floor(total / 60)}m ${(total % 60).toFixed(0)}s`;
}

export default function Loading({ label, since }: { label: string; since: number }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 100);
    return () => clearInterval(t);
  }, []);
  return (
    <div role="status" className="flex h-9 w-fit items-center gap-2.5 text-[13.5px]">
      <LoaderGrid />
      <Shimmer>{label}</Shimmer>
      <span className="font-mono text-[12px] text-ink-3 tabular-nums">{elapsed(since, now)}</span>
    </div>
  );
}
