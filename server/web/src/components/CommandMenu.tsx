// The command menu (the ⋯ button in the header): what the terminal does
// with /clear, /compact and going back a turn in /tree, the model and
// effort, and the panels for background jobs and subagents. What cannot
// be undone from here asks first.

import { useState, type ReactNode } from "react";
import type { Model } from "../types";
import { Check, Layers, Plus, Undo } from "./icons";
import Sheet, { Confirm } from "./Sheet";

export type Command = "new" | "compact" | "undo";

type Ask = { cmd: Command; title: string; body: string; action: string; danger?: boolean };

function Row({ icon, label, hint, disabled, onClick }: { icon: ReactNode; label: string; hint?: string; disabled?: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className="flex min-h-11 w-full items-center gap-3 rounded-control px-3 py-2 text-left transition-colors enabled:hover:bg-hover enabled:active:bg-hover-2 disabled:opacity-45"
    >
      <span className="flex size-5 shrink-0 items-center justify-center text-ink-2">{icon}</span>
      <span className="min-w-0 flex-1">
        <span className="block text-[14.5px] leading-snug text-ink">{label}</span>
        {hint && <span className="block text-[12.5px] leading-snug text-ink-3">{hint}</span>}
      </span>
    </button>
  );
}

export function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="mt-2">
      <div className="px-3 pt-1 pb-1 text-[11.5px] font-medium tracking-wide text-ink-3 uppercase">{title}</div>
      {children}
    </div>
  );
}

export default function CommandMenu({
  live,
  busy,
  canUndo,
  models,
  model,
  efforts,
  effort,
  panels,
  onCommand,
  onModel,
  onEffort,
  onClose,
}: {
  live: boolean;
  busy: boolean;
  canUndo: boolean;
  models: Model[];
  model: string;
  efforts: string[];
  effort: string;
  // rows that open the jobs and subagents panels, when there are any
  panels?: ReactNode;
  onCommand: (c: Command) => void;
  onModel: (id: string) => void;
  onEffort: (e: string) => void;
  onClose: () => void;
}) {
  const [ask, setAsk] = useState<Ask | null>(null);
  const run = (c: Command) => {
    onClose();
    onCommand(c);
  };
  const busyHint = busy ? "Wait for the turn to end, or stop it" : undefined;
  return (
    <>
      <Sheet title="Commands" onClose={onClose}>
        <Row
          icon={<Plus size={16} />}
          label="New conversation"
          hint={live ? "/clear in the terminal; this one stays saved" : "This one stays in the list"}
          onClick={() =>
            live ? setAsk({ cmd: "new", title: "Start a new conversation?", body: "The terminal clears its conversation (/clear). The current one stays saved; /resume reopens it.", action: "New conversation" }) : run("new")
          }
        />
        <Row
          icon={<Layers size={16} />}
          label="Compact context"
          hint={busyHint || "Replace the conversation with handoff notes"}
          disabled={busy}
          onClick={() =>
            setAsk({ cmd: "compact", title: "Compact the conversation?", body: "The model writes handoff notes that replace the conversation so far, to free context. /tree in the terminal still has the original.", action: "Compact" })
          }
        />
        <Row
          icon={<Undo size={16} />}
          label="Undo last turn"
          hint={busyHint || (canUndo ? "Go back to before your last message" : "Nothing to undo")}
          disabled={busy || !canUndo}
          onClick={() =>
            setAsk({
              cmd: "undo",
              title: "Undo the last turn?",
              body: "The conversation goes back to before your last message, which returns to the input. The session keeps the old branch (/tree), but files commands changed stay changed.",
              action: "Undo",
              danger: true,
            })
          }
        />
        {panels && <Section title="Running alongside">{panels}</Section>}
        {models.length > 0 && (
          <Section title="Model">
            {models.map((m) => (
              <button
                key={m.id}
                type="button"
                onClick={() => onModel(m.id)}
                className={`flex min-h-10 w-full items-center gap-3 rounded-control px-3 py-1.5 text-left transition-colors hover:bg-hover ${m.id === model ? "bg-accent-tint" : ""}`}
              >
                <span className="flex size-5 shrink-0 items-center justify-center text-accent">{m.id === model && <Check size={15} />}</span>
                <span className={`min-w-0 flex-1 truncate text-[14px] ${m.id === model ? "font-medium text-accent-ink" : "text-ink"}`}>{m.name}</span>
                <span className="shrink-0 text-[12px] text-ink-3">{m.id.split("/")[0]}</span>
              </button>
            ))}
          </Section>
        )}
        {efforts.length > 0 && (
          <Section title="Effort">
            <div className="flex flex-wrap gap-1.5 px-3 pt-1 pb-2">
              {efforts.map((e) => (
                <button
                  key={e}
                  type="button"
                  onClick={() => onEffort(e)}
                  className={`h-8 rounded-chip px-3 text-[13px] transition-colors ${e === effort ? "bg-accent-tint font-medium text-accent-ink" : "bg-field text-ink-2 shadow-hairline hover:bg-hover-2"}`}
                >
                  {e}
                </button>
              ))}
            </div>
          </Section>
        )}
      </Sheet>
      {ask && (
        <Confirm
          title={ask.title}
          body={ask.body}
          action={ask.action}
          danger={ask.danger}
          onCancel={() => setAsk(null)}
          onConfirm={() => {
            setAsk(null);
            run(ask.cmd);
          }}
        />
      )}
    </>
  );
}
