// Input the running turn has not taken yet, above the field, as the
// terminal lists it (app/queue.go): steers, which go in after the running
// command, and in a live session follow-ups queued for after the turn.
// Edit takes one back into the field; × takes it back for good.

import type { PendingInput } from "../types";
import { Cross, Pencil } from "./icons";

function Entry({ text, onEdit, onCancel }: { text: string; onEdit: () => void; onCancel: () => void }) {
  return (
    <li className="flex min-w-0 items-start gap-1.5">
      <span className="mt-[5px] shrink-0 text-[12px] text-ink-3">↳</span>
      <span className="line-clamp-3 min-w-0 flex-1 pt-1 text-[13px] leading-snug whitespace-pre-wrap text-ink-2" style={{ overflowWrap: "anywhere" }}>
        {text}
      </span>
      <button type="button" aria-label="Edit" title="Edit: back into the input" onClick={onEdit} className="flex size-7 shrink-0 items-center justify-center rounded-chip text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2">
        <Pencil size={14} />
      </button>
      <button type="button" aria-label="Cancel" title="Don't send" onClick={onCancel} className="flex size-7 shrink-0 items-center justify-center rounded-chip text-ink-3 transition-colors hover:bg-hover-2 hover:text-red">
        <Cross size={14} />
      </button>
    </li>
  );
}

// live: the terminal sends steers at once when Stop interrupts (atto
// serve keeps them for the next turn).
export default function PendingList({ pending, live, onTake }: { pending: PendingInput; live: boolean; onTake: (text: string, queued: boolean, edit: boolean) => void }) {
  const queued = pending.queued || [];
  if (!pending.steers.length && !queued.length) return null;
  const group = (title: string, hint: string, list: string[], q: boolean) =>
    list.length > 0 && (
      <div>
        <div className="truncate text-[12px] text-ink-3">
          <span className="font-medium text-ink-2">{title}</span> {hint}
        </div>
        <ul className="mt-0.5">
          {list.map((t, i) => (
            <Entry key={i + t} text={t} onEdit={() => onTake(t, q, true)} onCancel={() => onTake(t, q, false)} />
          ))}
        </ul>
      </div>
    );
  return (
    <div className="mb-2 flex flex-col gap-1.5 rounded-card bg-inset px-3 py-2 shadow-hairline" style={{ animation: "fade-in 160ms ease-out both" }}>
      {group("Sent after the next command", live ? "· Stop sends them now" : "", pending.steers, false)}
      {group("Queued", "· starts when this turn ends", queued, true)}
    </div>
  );
}
