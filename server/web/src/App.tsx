// atto's web client: atto serve's threads, or with /remote the terminal's
// own live session. See server/protocol.go for the protocol.

import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { OriginalToggle, Statuses, useDisplay } from "./components/BlockMeta";
import ExtensionBar from "./components/ExtensionBar";
import Activity from "./components/Activity";
import ExtText from "./components/ExtText";
import GoalBar, { type GoalAction } from "./components/GoalBar";
import CommandMenu, { type Command } from "./components/CommandMenu";
import { ArrowDown, Bolt, Branch, Flag, Info, Layers, Menu, More, Plus, Radio, Target, Terminal } from "./components/icons";
import JobsPanel, { active as jobActive } from "./components/JobsPanel";
import Loading from "./components/Loading";
import PendingList from "./components/PendingList";
import PromptBar, { type Pending } from "./components/PromptBar";
import PromptSheet, { type Answer } from "./components/PromptSheet";
import Thinking from "./components/Thinking";
import StatusLine from "./components/StatusLine";
import ToolGroup from "./components/ToolGroup";
import ToolRow from "./components/ToolRow";
import { Markdown } from "./markdown";
import { Client, initialToken, saveToken, Unauthorized } from "./rpc";
import { loadThreadId, saveThreadId } from "./storage";
import { anchorShift, atBottom, firstBelow, nextFollow } from "./scroll";
import { activity, Meter, status } from "./status";
import { isRun, Transcript, type Row } from "./transcript";
import type { ExtensionUI, GoalInfo, Item, Job, Model, Notification, Prompt, ThreadInfo, ThreadSummary } from "./types";

// --- items ---

function secs(ms?: number) {
  if (!ms) return "";
  const s = ms / 1000;
  return s < 1 ? "<1s" : s < 60 ? `${Math.round(s)}s` : `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
}

function Line({ icon, children, tone = "text-ink-3" }: { icon: any; children: any; tone?: string }) {
  return (
    <div className={`flex min-h-7 items-start gap-2 text-[13px] leading-[1.5] ${tone}`}>
      <span className="mt-[3px] flex shrink-0">{icon}</span>
      <span className="min-w-0 whitespace-pre-wrap" style={{ overflowWrap: "anywhere" }}>
        {children}
      </span>
    </div>
  );
}

const Caret = () => (
  <span className="ml-0.5 inline-block h-[1em] w-0.5 translate-y-[3px] rounded-full bg-ink" style={{ animation: "caret-blink 1s step-end infinite" }} />
);

// Answers and reasoning show what extensions put on them (BlockMeta.tsx).
function Answer({ it, working }: { it: Item; working: boolean }) {
  const d = it.display;
  const shown = useDisplay(it.text, d);
  const meta = (!!d?.statuses?.length || shown.replaced) && (
    <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-1 text-[12px]">
      <Statuses d={d} lead={false} />
      {!!d?.statuses?.length && shown.replaced && <span className="text-ink-3">·</span>}
      <OriginalToggle d={d} original={shown.original} onToggle={shown.toggle} />
    </div>
  );
  return (
    <div className="flex flex-col">
      <div className="prose-atto text-ink">
        <Markdown text={shown.text} />
        {working && <Caret />}
      </div>
      {meta}
    </div>
  );
}

function Reasoning({ it, working }: { it: Item; working: boolean }) {
  const d = it.display;
  const shown = useDisplay(it.text, d);
  return (
    <Thinking
      working={working}
      done={it.durationMs ? `Thought for ${secs(it.durationMs)}` : "Thought"}
      status={<Statuses d={d} />}
      meta={<OriginalToggle d={d} original={shown.original} onToggle={shown.toggle} />}
    >
      {shown.text}
    </Thinking>
  );
}

export const ItemView = memo(function ItemView({ it }: { it: Item }) {
  const working = it.status === "inProgress";
  switch (it.type) {
    case "userMessage":
      return (
        <div className="flex justify-end pl-10" style={{ animation: "fade-up 300ms cubic-bezier(0.23,1,0.32,1) both" }}>
          <div className="max-w-full rounded-[18px] bg-field px-3.5 py-2 text-[15px] leading-normal whitespace-pre-wrap text-ink shadow-hairline" style={{ overflowWrap: "anywhere" }}>
            {it.text}
          </div>
        </div>
      );
    case "agentMessage":
      return <Answer it={it} working={working} />;
    case "reasoning":
      return <Reasoning it={it} working={working} />;
    case "extText":
      return <ExtText it={it} />;
    case "commandExecution":
      return <ToolRow it={it} />;
    case "compaction":
      return (
        <Thinking
          working={working}
          icon={<Layers size={15} />}
          active="Compacting context"
          done={"Context compacted" + (it.tokensBefore ? ` · ${it.tokensBefore.toLocaleString()} → ~${(it.tokensAfter || 0).toLocaleString()} tokens` : "")}
        >
          {it.text}
        </Thinking>
      );
    case "branchSummary":
      return (
        <Thinking working={working} icon={<Branch size={15} />} active="Summarizing branch" done="Branch summary">
          {it.text}
        </Thinking>
      );
    case "event":
      return <Line icon={<Bolt size={13} />}>{(it.text || "").replace(/^\[atto event\] ?/, "").split("\n")[0]}</Line>;
    case "goal":
      return <Line icon={<Target size={13} />}>{/<objective>/.test(it.text || "") ? "Continuing goal" : (it.text || "").replace(/^\[atto goal\] ?/, "").split("\n")[0]}</Line>;
    case "goalStatus":
      return <Line icon={<Target size={13} />}>{"Goal " + (it.goalStatus || "") + (it.text ? ": " + it.text : "")}</Line>;
    case "hook":
      return (
        <Line icon={<Flag size={13} />} tone={it.blocked ? "text-orange" : "text-ink-3"}>
          {(it.hookEvent || "hook") + ": " + (it.text || "")}
        </Line>
      );
    case "notice":
      return <Line icon={<Info size={13} />}>{it.text}</Line>;
    case "note":
      return (
        <Line icon={<Info size={13} />} tone={it.tone === "error" ? "text-red" : "text-ink-3"}>
          {it.text}
        </Line>
      );
  }
  return null;
});

const itemView = (it: Item) => <ItemView key={it.id} it={it} />;

// Rows in blocks (see transcript.ts): a frame re-renders the block that
// changed. A run of commands shows as a group unless settings.json says
// "toolGroups": false.
export const Block = memo(function Block({ rows, groups }: { rows: Row[]; groups: boolean }) {
  return (
    <>
      {rows.map((r) => (isRun(r) ? <ToolGroup key={r.id} run={r} on={groups} item={itemView} /> : itemView(r)))}
    </>
  );
});

// --- app ---

type Phase = "login" | "loading" | "ready";

function shortPath(p: string) {
  return p.replace(/^\/(Users|home)\/[^/]+/, "~");
}

function baseName(p: string) {
  return p.split(/[\\/]/).filter(Boolean).pop() || p;
}

export default function App() {
  const client = useMemo(() => new Client(initialToken()), []);
  const [phase, setPhase] = useState<Phase>(client.token ? "loading" : "login");
  const [loginError, setLoginError] = useState("");
  const [live, setLive] = useState(false);
  // settings.json's "toolGroups" (initialize's settings)
  const [groups, setGroups] = useState(true);
  const [models, setModels] = useState<Model[]>([]);
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [info, setInfo] = useState<ThreadInfo | null>(null);
  // The live session's open prompt and goal.
  const [prompt, setPrompt] = useState<Prompt | null>(null);
  const [goal, setGoal] = useState<GoalInfo | null>(null);
  // What the thread's extensions show around the input.
  const [extUi, setExtUi] = useState<ExtensionUI | null>(null);
  const [connected, setConnected] = useState(true);
  const [drawer, setDrawer] = useState(false);
  // the running turn's time and tokens, for the activity line
  const meter = useRef(new Meter()).current;
  const [newModel, setNewModel] = useState(""); // for the next thread/start
  const [menu, setMenu] = useState(false);
  // What runs beside the turns (job/list), and the panel open.
  const [jobs, setJobs] = useState<Job[]>([]);
  const [panel, setPanel] = useState<"jobs" | null>(null);
  // text put back into the input (an undone turn's message)
  const [fill, setFill] = useState<{ text: string; n: number } | null>(null);
  const [, setTick] = useState(0);
  const store = useRef(new Transcript()).current;
  const infoRef = useRef<ThreadInfo | null>(null);
  infoRef.current = info;
  const logRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  // Scrolling (see scroll.ts): follow is whether new output scrolls into
  // view; lastTop the last scroll position seen; ours the position the
  // view itself scrolled to (its scroll event is not the reader's);
  // anchor the item kept in place while the reader is scrolled up;
  // touching is a finger on the transcript, which the view never fights.
  const followRef = useRef(true);
  const lastTop = useRef(0);
  const ours = useRef(-1);
  const anchor = useRef<{ el: HTMLElement; top: number } | null>(null);
  const touching = useRef(false);
  const [away, setAway] = useState(false);

  // One render per frame for the transcript.
  const frame = useRef(0);
  const redraw = useCallback(() => {
    if (frame.current) return;
    frame.current = requestAnimationFrame(() => {
      frame.current = 0;
      setTick((t) => t + 1);
    });
  }, []);

  const note = useCallback(
    (text: string, tone: "error" | "info" = "info") => {
      store.note(text, tone);
      redraw();
    },
    [store, redraw],
  );

  const fail = useCallback(
    (e: unknown) => {
      if (e instanceof Unauthorized) return;
      note(String((e as Error)?.message || e), "error");
    },
    [note],
  );

  client.onUnauthorized = () => {
    client.close();
    saveToken("");
    setLoginError(live ? "This link is no longer valid: /remote was turned off or restarted. Scan the new code." : "The token was not accepted.");
    setPhase("login");
  };

  // Notifications.
  const onNote = useCallback(
    (n: Notification) => {
      const p = n.params || {};
      const cur = infoRef.current;
      if (n.method === "thread/switched") {
        if (live) openLive();
        return;
      }
      if (n.method === "events/reset") {
        // The server restarted or we were away too long: read again.
        if (live) openLive();
        else if (cur) reopen(cur.threadId);
        else {
          follow(p.eventId || 0);
          loadThreads();
        }
        return;
      }
      if (!cur || p.threadId !== cur.threadId) {
        if (n.method === "turn/completed" && !live) loadThreads();
        return;
      }
      meter.lastEvent = Date.now();
      // A command ended (it may have started a job), an event came (a job
      // ended) or the turn did: see what runs beside it now.
      if (n.method === "turn/completed" || n.method === "event" || (n.method === "item/completed" && (p.item?.type === "commandExecution" || p.item?.type === "event")))
        refreshBg();
      switch (n.method) {
        case "turn/started":
          setInfo((i) => (i ? { ...i, busy: true, turnId: p.turnId } : i));
          meter.start({ startedAt: p.startedAt, verb: p.verb, inputTokens: 0, outputTokens: 0 }, Date.now());
          break;
        case "item/started":
        case "item/updated":
        case "item/completed":
          store.upsert(p.item);
          if (p.item?.pending) meter.draft(p.item);
          redraw();
          break;
        case "item/delta": {
          const t = store.get(p.itemId)?.type;
          if (t === "reasoning" || t === "agentMessage") meter.text(p.delta.length);
          store.delta(p.itemId, p.delta);
          redraw();
          break;
        }
        case "turn/pending":
          setInfo((i) => (i ? { ...i, pending: p.pending } : i));
          break;
        case "thread/usage":
          if (p.step) meter.step(p.step);
          setInfo((i) => (i ? { ...i, usage: p.usage, contextTokens: p.contextTokens ?? i.contextTokens } : i));
          break;
        case "item/display":
          store.display(p.itemId, p.display || null);
          redraw();
          break;
        case "extension/ui":
          setExtUi(p.ui || null);
          break;
        case "turn/completed":
          setInfo((i) => (i ? { ...i, busy: false, turnId: "", contextTokens: p.contextTokens ?? i.contextTokens } : i));
          // The terminal says so itself in a live session (as notices).
          if (!live && p.status === "failed") note("Failed: " + (p.error || "unknown error"), "error");
          else if (!live && p.status === "interrupted") note("Interrupted.");
          if (!live) loadThreads();
          break;
        case "thread/updated":
          if (p.thread) setInfo((i) => (i ? { ...i, ...p.thread, items: undefined } : i));
          break;
        case "hook":
          if (!p.turnId) note("⚑ " + p.event + ": " + p.message);
          break;
        case "extension/notify":
          note((p.extension ? p.extension + ": " : "") + p.message, p.level === "error" ? "error" : "info");
          break;
        case "thread/reloaded":
          note(p.error ? "Reload failed: " + p.error : "Reloaded configuration.", p.error ? "error" : "info");
          break;
        case "prompt/open":
          setPrompt(p.prompt);
          break;
        case "prompt/closed":
          setPrompt((cur) => (cur && cur.id === p.id ? null : cur));
          break;
        case "goal/updated":
          setGoal(p.goal || null);
          break;
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [live],
  );
  // refreshBg reads the thread's background jobs again.
  const refreshBg = useCallback(() => {
    const id = infoRef.current?.threadId;
    if (!id) return;
    client
      .call<{ jobs: Job[] }>("job/list", { threadId: id })
      .then((r) => infoRef.current?.threadId === id && setJobs(r.jobs || []))
      .catch(() => {});
  }, [client]);
  const threadId = info?.threadId;
  useEffect(() => {
    setJobs([]);
    refreshBg();
  }, [threadId, refreshBg]);
  // While something runs, or a panel is open, every few seconds.
  const running = jobs.some(jobActive);
  useEffect(() => {
    if (!running && !panel) return;
    const t = setInterval(refreshBg, 3000);
    return () => clearInterval(t);
  }, [running, panel, refreshBg]);

  const onNoteRef = useRef(onNote);
  onNoteRef.current = onNote;

  const follow = useCallback(
    (from: number) => client.follow(from, (n) => onNoteRef.current(n), setConnected),
    [client],
  );

  const show = useCallback(
    (t: ThreadInfo) => {
      store.reset(t.items || []);
      setInfo({ ...t, items: undefined, prompt: undefined, goal: undefined, extensionUi: undefined });
      setPrompt(t.prompt || null);
      setGoal(t.goal || null);
      setExtUi(t.extensionUi || null);
      if (t.busy) meter.start(t.turn, Date.now());
      if (!t.live) saveThreadId(t.threadId);
      followRef.current = true;
      setAway(false);
      setDrawer(false);
      follow(t.eventId || 0);
      redraw();
    },
    [store, follow, redraw, meter],
  );

  const loadThreads = useCallback(() => {
    client
      .call<{ threads: ThreadSummary[] }>("thread/list")
      .then((r) => setThreads(r.threads || []))
      .catch(() => {});
  }, [client]);

  const openLive = useCallback(() => {
    client.call<ThreadInfo>("thread/read").then(show).catch(fail);
  }, [client, show, fail]);

  // reopen reads a thread of atto serve again (thread/resume loads it if
  // the server restarted since).
  const reopen = useCallback(
    (threadId: string) => {
      client.call<ThreadInfo>("thread/resume", { threadId }).then(show).catch(fail);
    },
    [client, show, fail],
  );

  // Start: check the token, then load models and the thread(s).
  const start = useCallback(async () => {
    setPhase("loading");
    try {
      const init = await client.call<{ live?: boolean; eventId?: number; settings?: { toolGroups?: boolean } }>("initialize");
      saveToken(client.token);
      history.replaceState(null, "", location.pathname);
      setLive(!!init.live);
      setGroups(init.settings?.toolGroups !== false);
      const ms = await client.call<{ models: Model[] }>("models/list").catch(() => ({ models: [] as Model[] }));
      setModels(ms.models || []);
      setPhase("ready");
      if (init.live) {
        const t = await client.call<ThreadInfo>("thread/read");
        show(t);
      } else {
        follow(init.eventId || 0);
        loadThreads();
        // Back where this tab was before a reload.
        const was = loadThreadId();
        if (was) client.call<ThreadInfo>("thread/resume", { threadId: was }).then(show, () => saveThreadId(""));
      }
    } catch (e) {
      if (!(e instanceof Unauthorized)) {
        setLoginError(String((e as Error).message || e));
        setPhase("login");
      }
    }
  }, [client, show, follow, loadThreads]);

  useEffect(() => {
    let hiddenAt = 0;
    const onVis = () => {
      if (document.hidden) hiddenAt = Date.now();
      else if (hiddenAt) client.wake(Date.now() - hiddenAt);
    };
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, [client]);

  useEffect(() => {
    if (client.token) start();
    return () => client.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Scrolling: follow new output only while at the bottom.
  const scrollTo = (el: HTMLElement, top: number) => {
    el.scrollTop = top;
    ours.current = el.scrollTop; // as the browser clamped it
    lastTop.current = el.scrollTop;
  };
  const setAnchor = (el: HTMLElement) => {
    const kids = bodyRef.current?.children;
    if (!kids) return;
    const at = (i: number) => kids[i] as HTMLElement;
    const i = firstBelow(kids.length, (i) => at(i).offsetTop + at(i).offsetHeight, el.scrollTop);
    anchor.current = i >= 0 ? { el: at(i), top: at(i).offsetTop } : null;
  };
  // settle runs after every layout change: to the bottom while
  // following, else the anchor stays put when what is above it changes
  // height (a thinking block opens, a display block is replaced).
  const settle = useCallback(() => {
    const el = logRef.current;
    if (!el || touching.current) return;
    if (followRef.current) {
      if (!atBottom(el.scrollTop, el.scrollHeight, el.clientHeight)) scrollTo(el, el.scrollHeight);
      return;
    }
    const a = anchor.current;
    if (!a || !a.el.isConnected) {
      setAnchor(el);
      return;
    }
    const d = anchorShift(a.top, a.el.offsetTop);
    a.top = a.el.offsetTop;
    if (d) scrollTo(el, el.scrollTop + d);
  }, []);
  const onScroll = () => {
    const el = logRef.current;
    if (!el) return;
    if (ours.current >= 0 && Math.abs(el.scrollTop - ours.current) < 1) {
      ours.current = -1;
      return;
    }
    ours.current = -1;
    const f = nextFollow(followRef.current, lastTop.current, el.scrollTop, el.scrollHeight, el.clientHeight);
    lastTop.current = el.scrollTop;
    followRef.current = f;
    if (!f) setAnchor(el);
    setAway(!f);
  };
  const unfollow = () => {
    const el = logRef.current;
    if (followRef.current && el && el.scrollHeight > el.clientHeight + 1) {
      followRef.current = false;
      setAway(true);
      if (logRef.current) setAnchor(logRef.current);
    }
  };
  // A finger lifted: take in where it left the view before settling, as
  // its scroll event may not have arrived yet (a quick flick up must not
  // snap back to the bottom).
  const endTouch = () => {
    touching.current = false;
    const el = logRef.current;
    if (el && Math.abs(el.scrollTop - lastTop.current) >= 1) onScroll();
    settle();
  };
  useLayoutEffect(settle);
  useEffect(() => {
    const el = logRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => settle());
    ro.observe(el); // the composer grew, the keyboard opened
    if (bodyRef.current) ro.observe(bodyRef.current);
    return () => ro.disconnect();
  }, [settle, phase]);
  const toBottom = (smooth = true) => {
    const el = logRef.current;
    followRef.current = true;
    anchor.current = null;
    setAway(false);
    if (!el) return;
    if (smooth) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
    else scrollTo(el, el.scrollHeight);
  };

  // Actions.
  const fallback = models.find((m) => m.hasKey)?.id || "";
  const startModel = newModel || fallback;
  const current = models.find((m) => m.id === (info ? info.model : startModel));
  const imagesOK = !!current?.images;

  const newThread = async () => {
    try {
      const id = info?.model || startModel;
      show(await client.call<ThreadInfo>("thread/start", id ? { model: id } : {}));
      loadThreads();
    } catch (e) {
      fail(e);
    }
  };

  // The menu's commands. The live session runs /clear itself (thread/start
  // is atto serve's); compaction and rollback are the same call for both.
  const command = async (c: Command) => {
    if (c === "new") {
      if (live) send("/clear", []);
      else newThread();
      return;
    }
    if (!info) return;
    try {
      if (c === "compact") {
        await client.call("thread/compact", { threadId: info.threadId });
        return;
      }
      const r = await client.call<ThreadInfo & { input?: string }>("thread/rollback", { threadId: info.threadId });
      show(r);
      if (r.input) setFill({ text: r.input, n: Date.now() });
    } catch (e) {
      fail(e);
    }
  };

  const send = async (text: string, images: Pending[]): Promise<boolean> => {
    const imgs = images.map(({ mimeType, data }) => ({ mimeType, data }));
    // atto serve has no terminal to run these: do what it would.
    if (!live && !images.length && /^\/(compact|clear|new)$/.test(text.trim())) {
      command(text.trim() === "/compact" ? "compact" : "new");
      return true;
    }
    try {
      let t = info;
      if (!t) {
        t = await client.call<ThreadInfo>("thread/start", startModel ? { model: startModel } : {});
        show(t);
        loadThreads();
      }
      toBottom(false);
      if (!live && t.busy) {
        if (imgs.length) {
          note("Images go with a new turn: send them once this one finishes.");
          return false;
        }
        // Shown as pending until the turn takes it (turn/pending).
        await client.call("turn/steer", { threadId: t.threadId, input: text });
        return true;
      }
      await client.call("turn/start", { threadId: t.threadId, input: text, images: imgs });
      return true;
    } catch (e) {
      fail(e);
      return false;
    }
  };

  const setModel = async (id: string) => {
    if (!info) return;
    try {
      const t = await client.call<ThreadInfo>("thread/setModel", { threadId: info.threadId, model: id });
      setInfo((i) => (i ? { ...i, ...t, items: undefined } : i));
    } catch (e) {
      fail(e);
    }
  };
  const setEffort = async (effort: string) => {
    if (!info) return;
    try {
      const t = await client.call<ThreadInfo>("thread/setEffort", { threadId: info.threadId, effort });
      setInfo((i) => (i ? { ...i, ...t, items: undefined } : i));
    } catch (e) {
      fail(e);
    }
  };

  // take takes back pending input: into the field to edit, or for good.
  const take = async (text: string, queued: boolean, edit: boolean) => {
    if (!info) return;
    try {
      await client.call("turn/unsteer", { threadId: info.threadId, input: text, queued });
      if (edit) setFill({ text, n: Date.now() });
    } catch (e) {
      fail(e);
    }
  };

  const answer = async (a: Answer) => {
    const p = prompt;
    if (!p || !info) return;
    setPrompt((cur) => (cur && cur.id === p.id ? null : cur));
    try {
      await client.call("prompt/answer", { threadId: info.threadId, id: p.id, ...a });
    } catch (e) {
      fail(e);
    }
  };

  const goalAction = (a: GoalAction) => {
    send("/goal " + a, []);
  };

  if (phase === "login") return <Login error={loginError} onToken={(t) => ((client.token = t), start())} />;
  if (phase === "loading")
    return (
      <div className="flex h-full items-center justify-center">
        <Loading label="Connecting" since={Date.now()} />
      </div>
    );

  const blocks = store.blocks();
  const busy = !!info?.busy;
  const last = store.last();
  const pickable = models.filter((m) => m.hasKey || m.id === info?.model);
  const title = info ? info.name || baseName(info.cwd) : "New conversation";

  // What runs beside the turns: on the status line while it runs (as the
  // terminal's "● 2 jobs running"), in the menu while there is any.
  const nJobs = jobs.filter(jobActive).length;
  const bgChips = nJobs > 0 && (
    <button type="button" onClick={() => setPanel("jobs")} className="flex h-6 items-center gap-1 rounded-chip px-1.5 text-green hover:bg-hover">
      <span className="size-1.5 rounded-full bg-green" /> {nJobs} {nJobs === 1 ? "job" : "jobs"}
    </button>
  );
  const panels = jobs.length > 0 && (
    <button
      type="button"
      onClick={() => {
        setMenu(false);
        setPanel("jobs");
      }}
      className="flex min-h-11 w-full items-center gap-3 rounded-control px-3 py-2 text-left transition-colors hover:bg-hover"
    >
      <span className="flex size-5 shrink-0 items-center justify-center text-ink-2">
        <Terminal size={16} />
      </span>
      <span className="min-w-0 flex-1 text-[14.5px] text-ink">Background jobs</span>
      <span className="shrink-0 text-[12.5px] text-ink-3">{nJobs ? `${nJobs} running` : jobs.length}</span>
    </button>
  );

  const toolbar = (
    <>
      {pickable.length > 0 && (
        <select
          aria-label="Model"
          value={info ? info.model : startModel}
          onChange={(e) => {
            const id = (e.target as HTMLSelectElement).value;
            if (info) setModel(id);
            else setNewModel(id);
          }}
          className="h-9 max-w-[42vw] min-w-0 appearance-none truncate rounded-chip bg-transparent px-2 text-[12.5px] font-medium text-ink-2 transition-colors hover:bg-hover disabled:opacity-60 sm:max-w-[260px]"
        >
          {pickable.map((m) => (
            <option key={m.id} value={m.id}>
              {m.name} ({m.id.split("/")[0]})
            </option>
          ))}
        </select>
      )}
      {info?.efforts && info.efforts.length > 0 && (
        <select
          aria-label="Effort"
          value={info.effort}
          onChange={(e) => setEffort((e.target as HTMLSelectElement).value)}
          className="h-9 min-w-0 appearance-none rounded-chip bg-transparent px-2 text-[12.5px] text-ink-3 transition-colors hover:bg-hover"
        >
          {info.efforts.map((x) => (
            <option key={x}>{x}</option>
          ))}
        </select>
      )}
    </>
  );

  return (
    <div className="flex h-full">
      {!live && (
        <>
          {drawer && <div className="fixed inset-0 z-20 bg-black/30 md:hidden" onClick={() => setDrawer(false)} />}
          <aside
            className={`fixed inset-y-0 left-0 z-30 flex w-[84vw] max-w-[300px] flex-col border-r border-line bg-canvas transition-transform duration-200 md:static md:w-[280px] md:translate-x-0 ${drawer ? "translate-x-0" : "-translate-x-full"}`}
            style={{ paddingTop: "env(safe-area-inset-top)" }}
          >
            <div className="flex h-14 items-center gap-2 px-4">
              <span className="flex-1 text-[15px] font-semibold tracking-wide">atto</span>
              <button
                type="button"
                onClick={newThread}
                className="flex h-9 items-center gap-1.5 rounded-control bg-surface px-3 text-[13px] font-medium shadow-btn active:scale-[0.97]"
              >
                <Plus size={14} /> New
              </button>
            </div>
            <div className="flex-1 overflow-y-auto px-2 pb-4">
              {threads.length === 0 && <p className="px-3 py-2 text-[13px] text-ink-3">No conversations yet.</p>}
              {threads.map((t) => (
                <button
                  key={t.threadId}
                  type="button"
                  onClick={() =>
                    client
                      .call<ThreadInfo>("thread/resume", { threadId: t.threadId })
                      .then(show)
                      .catch(fail)
                  }
                  className={`mb-0.5 block w-full rounded-control px-3 py-2.5 text-left transition-colors hover:bg-hover-2 ${t.threadId === info?.threadId ? "bg-hover-2" : ""}`}
                >
                  <div className="truncate text-[14px] text-ink">{t.name || t.preview || "(empty)"}</div>
                  <div className="truncate text-[12px] text-ink-3">
                    {t.updatedAt ? new Date(t.updatedAt).toLocaleString() + " · " : ""}
                    {shortPath(t.cwd)}
                  </div>
                </button>
              ))}
            </div>
          </aside>
        </>
      )}

      <main className="flex min-w-0 flex-1 flex-col">
        <header className="flex shrink-0 items-center gap-2 border-b border-line bg-page/90 px-3 backdrop-blur" style={{ paddingTop: "env(safe-area-inset-top)" }}>
          <div className="flex h-12 w-full min-w-0 items-center gap-2">
            {!live && (
              <button type="button" aria-label="Conversations" onClick={() => setDrawer(true)} className="-ml-1 flex size-10 items-center justify-center rounded-control text-ink-2 hover:bg-hover md:hidden">
                <Menu size={18} />
              </button>
            )}
            <div className="min-w-0 flex-1">
              <div className="truncate text-[14px] font-medium text-ink">{title}</div>
              {info && (
                <div className="truncate text-[11.5px] text-ink-3">
                  {(live ? "terminal session · " : "") + shortPath(info.cwd)}
                </div>
              )}
            </div>
            {live && (
              <span className="flex shrink-0 items-center gap-1 rounded-chip bg-accent-tint px-2 py-1 text-[11.5px] font-medium text-accent-ink">
                <Radio size={12} /> live
              </span>
            )}
            <button type="button" aria-label="Commands" title="Commands" onClick={() => setMenu(true)} className="-mr-1 flex size-10 shrink-0 items-center justify-center rounded-control text-ink-2 hover:bg-hover">
              <More size={18} />
            </button>
            <span title={connected ? "connected" : "reconnecting"} className={`size-2 shrink-0 rounded-full ${connected ? "bg-green" : "bg-orange"}`} style={connected ? undefined : { animation: "caret-blink 1s step-end infinite" }} />
          </div>
        </header>
        {live && goal && <GoalBar goal={goal} onAction={goalAction} />}

        <div
          ref={logRef}
          onScroll={onScroll}
          onWheel={(e) => e.deltaY < 0 && unfollow()}
          onTouchStart={() => (touching.current = true)}
          onTouchEnd={endTouch}
          onTouchCancel={endTouch}
          className="relative min-h-0 flex-1 overflow-y-auto overscroll-contain"
          style={{ WebkitOverflowScrolling: "touch", overflowAnchor: "none" }}
        >
          <div ref={bodyRef} className="transcript mx-auto flex w-full max-w-[820px] flex-col gap-3 px-4 pt-4 pb-6">
            {!info && (
              <div className="py-16 text-center text-[14px] text-ink-3">{live ? "Waiting for the terminal session…" : "Pick a conversation, or write below to start one."}</div>
            )}
            {blocks.map((b, j) => (
              <Block key={j} rows={b} groups={groups} />
            ))}
          </div>
        </div>

        <div className="relative shrink-0">
          {away && (
            <button
              type="button"
              onClick={() => toBottom()}
              className="absolute -top-12 left-1/2 z-10 flex h-9 -translate-x-1/2 items-center gap-1.5 rounded-full bg-surface px-3.5 text-[13px] font-medium whitespace-nowrap text-ink-2 shadow-raised active:scale-[0.97]"
              style={{ animation: "fade-in 160ms ease-out both" }}
            >
              <ArrowDown size={14} /> Jump to latest
            </button>
          )}
          <ExtensionBar ui={extUi} />
          <PromptBar
            busy={busy}
            canBackground={busy && store.running()}
            imagesOK={imagesOK}
            placeholder={live ? "Message the terminal session" : "Message atto"}
            toolbar={toolbar}
            fill={fill}
            above={info?.pending && <PendingList pending={info.pending} live={live} onTake={take} />}
            activity={busy && <Activity label={activity(last, store.running(), meter.verb)} meter={meter} running={store.running()} />}
            footer={info && <StatusLine s={status(info)} extra={bgChips} />}
            onSend={send}
            onStop={() => info && client.call("turn/interrupt", { threadId: info.threadId }).catch(fail)}
            onBackground={() => info && client.call("turn/background", { threadId: info.threadId }).catch(fail)}
            warn={(t) => note(t, "error")}
          />
        </div>
      </main>
      {menu && (
        <CommandMenu
          live={live}
          busy={busy}
          canUndo={store.list().some((it) => it.type === "userMessage")}
          models={pickable}
          model={info ? info.model : startModel}
          efforts={info?.efforts || []}
          effort={info?.effort || ""}
          panels={panels || undefined}
          onCommand={command}
          onModel={(id) => (info ? setModel(id) : setNewModel(id))}
          onEffort={setEffort}
          onClose={() => setMenu(false)}
        />
      )}
      {panel === "jobs" && info && <JobsPanel client={client} threadId={info.threadId} jobs={jobs} onRefresh={refreshBg} onClose={() => setPanel(null)} fail={fail} />}
      {live && prompt && <PromptSheet key={prompt.id} prompt={prompt} onAnswer={answer} />}
    </div>
  );
}

function Login({ error, onToken }: { error: string; onToken: (t: string) => void }) {
  const [tok, setTok] = useState("");
  return (
    <div className="mx-auto flex h-full max-w-[420px] flex-col justify-center px-5">
      <h1 className="mb-1 text-[22px] font-semibold">atto</h1>
      <p className="mb-5 text-[14px] leading-relaxed text-ink-2">
        Open the link that <code className="font-mono text-[13px]">atto serve</code> or <code className="font-mono text-[13px]">/remote</code> printed, scan its QR code, or paste the token.
      </p>
      {error && <p className="mb-4 rounded-control bg-red-tint px-3 py-2 text-[13px] text-red">{error}</p>}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (tok.trim()) onToken(tok.trim());
        }}
        className="flex gap-2"
      >
        <input
          value={tok}
          onInput={(e) => setTok((e.target as HTMLInputElement).value)}
          placeholder="token"
          autoComplete="off"
          autoCapitalize="off"
          spellcheck={false}
          className="h-11 min-w-0 flex-1 rounded-control border border-line bg-field px-3 font-mono text-[16px] outline-none focus:border-line-strong"
        />
        <button type="submit" className="h-11 rounded-control bg-ink px-4 text-[14px] font-medium text-surface active:scale-[0.97]">
          Open
        </button>
      </form>
    </div>
  );
}
