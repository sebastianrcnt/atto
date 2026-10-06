// Adapted from the composer of Beautiful UI's ChatComposer
// (https://www.beautifului.dev, MIT, Copyright (c) 2026 Shane Levine; see
// THIRD_PARTY_NOTICES): the rounded field that focuses on a click
// anywhere, and the ink send button that lights up once there is
// something to send. Here it also attaches images, and while a turn runs
// it steers, stops and moves a command to the background, beside the
// activity line; the status line goes under it.

import { useEffect, useRef, useState, type ReactNode } from "react";
import { loadDraft, saveDraft } from "../storage";
import { ArrowUp, Image as ImageIcon, MoveDown, Stop } from "./icons";

export type Pending = { mimeType: string; data: string; url: string; name: string };

const TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];
const MAX_BYTES = 10 << 20; // the server's limit

// Touch keyboards have no Shift+Enter: Enter makes a new line there.
const coarse = typeof matchMedia !== "undefined" && matchMedia("(pointer: coarse)").matches;

export function readImages(files: Iterable<File>, add: (p: Pending) => void, warn: (text: string) => void) {
  for (const f of files) {
    if (!TYPES.includes(f.type)) {
      warn(`${f.name || "file"}: not a PNG, JPEG, GIF or WebP image`);
      continue;
    }
    if (f.size > MAX_BYTES) {
      warn(`${f.name || "image"}: larger than 10 MB`);
      continue;
    }
    const r = new FileReader();
    r.onload = () => add({ mimeType: f.type, data: String(r.result), url: URL.createObjectURL(f), name: f.name });
    r.readAsDataURL(f);
  }
}

export default function PromptBar({
  busy,
  canBackground,
  imagesOK,
  disabled,
  placeholder,
  toolbar,
  activity,
  footer,
  fill,
  onSend,
  onStop,
  onBackground,
  warn,
}: {
  busy: boolean;
  canBackground: boolean;
  imagesOK: boolean;
  disabled?: boolean;
  placeholder: string;
  // the model and effort pickers, next to the attach button
  toolbar?: ReactNode;
  // the activity line, left of Stop; the status line under the field
  activity?: ReactNode;
  footer?: ReactNode;
  // text to put back into the field, before what is there (n: a new one)
  fill?: { text: string; n: number } | null;
  onSend: (text: string, images: Pending[]) => Promise<boolean>;
  onStop: () => void;
  onBackground: () => void;
  warn: (text: string) => void;
}) {
  // The draft outlives a reload or a new login.
  const [text, setText] = useState(loadDraft);
  const [images, setImages] = useState<Pending[]>([]);
  const [drop, setDrop] = useState(false);
  const ref = useRef<HTMLTextAreaElement>(null);
  const file = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const ta = ref.current;
    if (!ta) return;
    ta.style.height = "auto";
    ta.style.height = Math.min(ta.scrollHeight, window.innerHeight * 0.4) + "px";
    saveDraft(text);
  }, [text]);

  useEffect(() => {
    if (!fill) return;
    setText((cur) => (cur.trim() ? fill.text + "\n" + cur : fill.text));
    ref.current?.focus();
  }, [fill?.n]);

  const add = (files: Iterable<File>) => {
    if (!imagesOK) {
      warn("This model does not take images. Switch models to send them.");
      return;
    }
    readImages(files, (p) => setImages((xs) => [...xs, p]), warn);
  };
  const canSend = (text.trim() !== "" || images.length > 0) && !disabled;
  const send = async () => {
    if (!canSend) return;
    const t = text.trim();
    setText("");
    const ok = await onSend(t, images);
    if (ok) {
      images.forEach((im) => URL.revokeObjectURL(im.url));
      setImages([]);
    } else setText((cur) => (cur.trim() ? t + "\n" + cur : t)); // back, with what was typed since
  };

  return (
    <div
      className="mx-auto w-full max-w-[820px] px-3 pt-2"
      style={{ paddingBottom: "max(10px, env(safe-area-inset-bottom))" }}
      onDragOver={(e) => {
        e.preventDefault();
        setDrop(true);
      }}
      onDragLeave={() => setDrop(false)}
      onDrop={(e) => {
        e.preventDefault();
        setDrop(false);
        if (e.dataTransfer?.files) add(e.dataTransfer.files);
      }}
    >
      {(busy || canBackground || activity) && (
        <div className="mb-2 flex items-center justify-end gap-2">
          <div className="min-w-0 flex-1 pl-1">{activity}</div>
          {canBackground && (
            <button
              type="button"
              aria-label="Background"
              title="Move the running command to the background"
              onClick={onBackground}
              className="flex h-9 shrink-0 items-center gap-1.5 rounded-control bg-surface px-3 text-[13px] font-medium text-ink-2 shadow-btn active:scale-[0.97]"
            >
              <MoveDown size={14} />
              <span className="max-sm:hidden">Background</span>
            </button>
          )}
          {busy && (
            <button
              type="button"
              onClick={onStop}
              className="flex h-9 shrink-0 items-center gap-1.5 rounded-control bg-surface px-3 text-[13px] font-medium text-ink shadow-btn active:scale-[0.97]"
            >
              <Stop size={12} />
              Stop
            </button>
          )}
        </div>
      )}
      <div
        role="presentation"
        onClick={() => ref.current?.focus()}
        className={`flex cursor-text flex-col gap-2 rounded-[14px] border p-2.5 shadow-[0_1px_2px_rgba(0,0,0,0.035)] transition-[border-color,box-shadow,background-color] duration-150 focus-within:border-line-strong ${drop ? "border-accent bg-accent-tint" : "border-line bg-field"}`}
      >
        {images.length > 0 && (
          <div className="flex flex-wrap gap-2">
            {images.map((im, i) => (
              <div key={im.url} className="relative" style={{ animation: "pop-in 200ms cubic-bezier(0.23,1,0.32,1) both" }}>
                <img src={im.url} alt={`image ${i + 1}`} className="h-16 rounded-control object-cover shadow-hairline" />
                <button
                  type="button"
                  aria-label="Remove image"
                  onClick={(e) => {
                    e.stopPropagation();
                    URL.revokeObjectURL(im.url);
                    setImages((xs) => xs.filter((x) => x !== im));
                  }}
                  className="absolute -top-2 -right-2 flex size-6 items-center justify-center rounded-full bg-ink text-[13px] leading-none text-surface"
                >
                  ×
                </button>
              </div>
            ))}
          </div>
        )}
        <textarea
          ref={ref}
          rows={1}
          value={text}
          enterKeyHint={coarse ? "enter" : "send"}
          aria-label="Message"
          placeholder={placeholder}
          onInput={(e) => setText((e.target as HTMLTextAreaElement).value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !coarse && !(e as any).isComposing) {
              e.preventDefault();
              send();
            }
          }}
          onPaste={(e) => {
            const files = [...(e.clipboardData?.files || [])].filter((f) => f.type.startsWith("image/"));
            if (files.length) {
              e.preventDefault();
              add(files);
            }
          }}
          className="max-h-[40vh] min-h-6 w-full resize-none bg-transparent px-1 text-[16px] leading-normal text-ink outline-none placeholder:text-ink-3"
        />
        <div className="flex items-center gap-1">
          {imagesOK && (
            <>
              <button
                type="button"
                aria-label="Attach images"
                title="Attach images (or paste or drop them)"
                onClick={(e) => {
                  e.stopPropagation();
                  file.current?.click();
                }}
                className="flex size-9 items-center justify-center rounded-control text-ink-3 transition-colors duration-100 hover:bg-hover hover:text-ink-2"
              >
                <ImageIcon size={18} />
              </button>
              <input
                ref={file}
                type="file"
                accept={TYPES.join(",")}
                multiple
                hidden
                onChange={(e) => {
                  const el = e.target as HTMLInputElement;
                  if (el.files) add(el.files);
                  el.value = "";
                }}
              />
            </>
          )}
          <div className="flex min-w-0 items-center" onClick={(e) => e.stopPropagation()}>
            {toolbar}
          </div>
          <span className="ml-auto pr-1 text-[12px] whitespace-nowrap text-ink-3 max-sm:hidden">{busy && canSend ? "steers the running turn" : ""}</span>
          <button
            type="button"
            aria-label={busy ? "Steer" : "Send"}
            disabled={!canSend}
            onClick={(e) => {
              e.stopPropagation();
              send();
            }}
            className="ml-auto flex size-9 shrink-0 items-center justify-center rounded-[10px] transition-[background-color,color,transform] duration-200 enabled:active:scale-[0.96]"
            style={{ background: canSend ? "var(--ink)" : "var(--line-strong)", color: canSend ? "var(--surface)" : "var(--ink-2)" }}
          >
            <ArrowUp size={18} />
          </button>
        </div>
      </div>
      {footer}
    </div>
  );
}
