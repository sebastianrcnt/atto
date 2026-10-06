// A bottom sheet for the client's own panels (jobs, subagents, the
// command menu) and a dialog to confirm what cannot be undone, styled as
// PromptSheet's. Esc and the backdrop close them.

import { useEffect, useRef, type ReactNode } from "react";
import { Cross } from "./icons";

// useEscape calls onEsc on Esc while it is the latest one mounted: a
// dialog over a sheet closes first.
const escapes: { current: () => void }[] = [];
let listening = false;

export function useEscape(onEsc: () => void) {
  const esc = useRef(onEsc);
  esc.current = onEsc;
  useEffect(() => {
    if (!listening) {
      listening = true;
      window.addEventListener("keydown", (e) => {
        if (e.key === "Escape" && escapes.length) {
          e.preventDefault();
          escapes[escapes.length - 1].current();
        }
      });
    }
    escapes.push(esc);
    return () => {
      escapes.splice(escapes.indexOf(esc), 1);
    };
  }, []);
}

function Backdrop({ onClick, z = "z-40" }: { onClick: () => void; z?: string }) {
  return <div className={`fixed inset-0 ${z} bg-black/40`} style={{ animation: "fade-in 160ms ease-out both" }} onClick={onClick} />;
}

export default function Sheet({
  title,
  subtitle,
  onClose,
  actions,
  children,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  onClose: () => void;
  // buttons next to Close (a back arrow, refresh)
  actions?: ReactNode;
  children: ReactNode;
}) {
  useEscape(onClose);
  return (
    <>
      <Backdrop onClick={onClose} />
      <div
        role="dialog"
        aria-modal="true"
        className="fixed inset-x-0 bottom-0 z-50 mx-auto flex max-h-[82vh] w-full max-w-[640px] flex-col rounded-t-window bg-surface shadow-overlay"
        style={{ paddingBottom: "env(safe-area-inset-bottom)", animation: "sheet-up 260ms cubic-bezier(0.23,1,0.32,1) both" }}
      >
        <div className="mx-auto mt-2 h-1 w-9 shrink-0 rounded-full bg-line-strong" />
        <div className="flex shrink-0 items-center gap-1 px-4 pt-2 pb-2.5">
          <div className="min-w-0 flex-1">
            <div className="truncate text-[15px] leading-snug font-semibold text-ink">{title}</div>
            {subtitle && <div className="mt-0.5 truncate text-[12.5px] text-ink-3">{subtitle}</div>}
          </div>
          {actions}
          <button type="button" aria-label="Close" onClick={onClose} className="flex size-9 shrink-0 items-center justify-center rounded-control text-ink-2 transition-colors hover:bg-hover">
            <Cross size={16} />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-2 pb-3">{children}</div>
      </div>
    </>
  );
}

// Confirm asks before something that cannot be undone from here.
export function Confirm({
  title,
  body,
  action,
  danger,
  onConfirm,
  onCancel,
}: {
  title: string;
  body?: string;
  action: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  useEscape(onCancel);
  return (
    <>
      <Backdrop onClick={onCancel} z="z-[60]" />
      <div
        role="alertdialog"
        aria-modal="true"
        aria-label={title}
        className="fixed inset-x-4 top-[18vh] z-[70] mx-auto max-w-[420px] rounded-window bg-surface p-4 shadow-overlay"
        style={{ animation: "pop-in 200ms cubic-bezier(0.23,1,0.32,1) both" }}
      >
        <div className="text-[15px] leading-snug font-semibold text-ink">{title}</div>
        {body && <p className="mt-1.5 text-[13.5px] leading-normal text-ink-2">{body}</p>}
        <div className="mt-4 flex justify-end gap-2">
          <button type="button" onClick={onCancel} className="h-10 rounded-control bg-surface px-4 text-[14px] font-medium text-ink-2 shadow-btn active:scale-[0.97]">
            Cancel
          </button>
          <button
            type="button"
            autoFocus
            onClick={onConfirm}
            className={`h-10 rounded-control px-4 text-[14px] font-medium text-surface active:scale-[0.97] ${danger ? "bg-red" : "bg-ink"}`}
          >
            {action}
          </button>
        </div>
      </div>
    </>
  );
}
