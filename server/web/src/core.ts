// Revision 3 only. No uncertain request is retried after a disconnect.
export type Data = Record<string, any>;
export type Tree = {
  type: string;
  key?: string;
  props: Data;
  children?: Tree[];
  events?: string[];
};
export const catalog = [
  'Box',
  'Text',
  'Markdown',
  'Code',
  'Diff',
  'Link',
  'Button',
  'Input',
  'Select',
  'List',
  'Progress',
  'Collapse',
  'Image',
];
export class RPC {
  next = 0;
  pending = new Map<
    number,
    { resolve: (v: any) => void; reject: (e: any) => void }
  >();
  constructor(
    public send: (s: string) => void,
    public event: (m: Data) => void,
  ) {}
  call(method: string, params: Data = {}): Promise<any> {
    const id = ++this.next;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      try {
        this.send(JSON.stringify({ id, method, params }));
      } catch (e) {
        this.pending.delete(id);
        reject(e);
      }
    });
  }
  notify(method: string, params: Data = {}) {
    this.send(JSON.stringify({ method, params }));
  }
  receive(raw: string) {
    const m = JSON.parse(raw);
    if (m.id != null) {
      const p = this.pending.get(m.id);
      if (!p) return;
      this.pending.delete(m.id);
      m.error
        ? p.reject(
            Object.assign(new Error(m.error.message), { data: m.error.data }),
          )
        : p.resolve(m.result);
    } else if (m.method) {
      this.event(m);
    }
  }
  close() {
    for (const p of this.pending.values())
      p.reject(
        new Error('Disconnected: request outcome unknown; not retried.'),
      );
    this.pending.clear();
  }
}
export class View {
  info: Data = {};
  items: Data[] = [];
  cursor = 0;
  generation = 0;
  reset = false;
  revs = new Map<string, number>();
  // Dirty item IDs are consumed by the attached thread view, not by events.
  // Multiple deltas coalesce into one body update in the next animation frame.
  dirtyItems = new Set<string>();
  deltaItems = new Set<string>();
  listVersion = 0;
  inventoryVersion = 0;
  uiVersion = 0;
  tabVersion = 0;
  dirtyItem(id: string, delta = false) {
    if (delta && !this.dirtyItems.has(id)) this.deltaItems.add(id);
    if (!delta) this.deltaItems.delete(id);
    this.dirtyItems.add(id);
  }
  snapshot(s: Data) {
    this.info = { ...s };
    const previous = new Map(this.items.map((i) => [i.id, i]));
    this.items = (s.items || []).map((i: Data) => {
      const old = previous.get(i.id);
      // Snapshot-only comparison: keep identical loaded rows across reconnect.
      // Never fingerprint the whole transcript during a streaming frame.
      if (old && JSON.stringify(old) === JSON.stringify(i)) return old;
      this.dirtyItem(i.id);
      return i;
    });
    this.cursor = s.eventId || 0;
    this.reset = false;
    this.generation++;
    this.listVersion++;
    this.inventoryVersion++;
    this.tabVersion++;
    this.uiVersion++;
    this.deltaItems.clear();
    this.revs.clear();
    for (const i of s.ui?.instances || [])
      this.revs.set(i.site + '\0' + i.id, i.rev);
  }
  prepend(p: Data, generation: number) {
    if (generation !== this.generation || this.reset) return false;
    const seen = new Set(this.items.map((i) => i.id));
    this.items = [
      ...p.items.filter((i: Data) => !seen.has(i.id)),
      ...this.items,
    ];
    this.listVersion++;
    this.inventoryVersion++;
    this.tabVersion++;
    this.info.hasMore = p.hasMore;
    this.info.before = p.before;
    return true;
  }
  apply(m: Data) {
    const p = m.params || {};
    if (p.threadId && p.threadId !== this.info.threadId) return false;
    if (
      m.method === 'events/reset' ||
      (m.serverInstanceId && m.serverInstanceId !== this.info.serverInstanceId)
    ) {
      this.reset = true;
      this.generation++;
      return true;
    }
    if (this.reset || (m.eventId && m.eventId <= this.cursor)) return false;
    const find = () => this.items.find((i) => i.id === p.itemId);
    switch (m.method) {
      case 'item/started':
      case 'item/updated':
      case 'item/completed': {
        const n = this.items.findIndex((i) => i.id === p.item.id);
        if (n < 0) {
          this.items.push(p.item);
          this.listVersion++;
        } else this.items[n] = p.item;
        this.dirtyItem(p.item.id);
        if (p.item.type === 'userMessage') this.tabVersion++;
        if (
          ['userMessage', 'agentMessage', 'commandExecution'].includes(
            p.item.type,
          )
        )
          this.inventoryVersion++;
        if (p.item.type === 'uiBlock' || p.item.uiDisplay) this.uiVersion++;
        break;
      }
      case 'item/delta': {
        const i = find();
        if (!i) return false;
        this.dirtyItem(i.id, true);
        const k = i.type === 'commandExecution' ? 'output' : 'text';
        i[k] = (i[k] || '') + p.delta;
        if (k === 'output' && i[k].length > 131072) i[k] = i[k].slice(-65536);
        break;
      }
      case 'thread/updated':
        this.info = {
          ...this.info,
          ...p.thread,
          ui: this.info.ui,
          before: this.info.before,
          hasMore: this.info.hasMore,
        };
        break;
      case 'turn/started':
        Object.assign(this.info, {
          busy: true,
          turnId: p.turnId,
          runKind: p.runKind,
          turn: { startedAt: p.startedAt, verb: p.verb },
        });
        break;
      case 'turn/completed':
        Object.assign(this.info, {
          busy: false,
          turnId: '',
          activity: null,
          turn: null,
          contextTokens: p.contextTokens ?? this.info.contextTokens,
        });
        break;
      case 'turn/activity':
        this.info.activity = p.activity;
        break;
      case 'turn/pending':
        this.info.pending = p.pending;
        break;
      case 'thread/usage':
        this.info.usage = p.usage;
        this.info.contextTokens = p.contextTokens ?? this.info.contextTokens;
        break;
      case 'goal/updated':
        this.info.goal = p.goal;
        break;
      case 'prompt/open':
        this.info.prompt = p.prompt;
        break;
      case 'prompt/closed':
        if (this.info.prompt?.id === p.id) this.info.prompt = null;
        break;
      case 'thread/branchChanged':
      case 'thread/closed':
        this.reset = true;
        this.generation++;
        break;
      case 'ui/open':
      case 'ui/render':
      case 'ui/close': {
        const key = p.site + '\0' + p.id;
        if (p.rev <= (this.revs.get(key) || 0)) return false;
        this.revs.set(key, p.rev);
        this.uiVersion++;
        if (!this.info.ui) this.info.ui = { version: 1, instances: [] };
        const a = this.info.ui.instances;
        const n = a.findIndex((i: Data) => i.site === p.site && i.id === p.id);
        if (m.method === 'ui/close') {
          if (n >= 0) a.splice(n, 1);
        } else if (n >= 0) {
          a[n] = {
            ...a[n],
            rev: p.rev,
            ...(m.method === 'ui/open'
              ? { options: p.options }
              : { tree: p.tree }),
          };
        } else if (
          ![
            'userMessage',
            'assistantMessage',
            'toolCall',
            'notice',
            'transcript',
          ].includes(p.site)
        ) {
          a.push({ ...p });
        }
        if (m.method === 'ui/render') {
          const block = this.items.find(
            (i) => i.type === 'uiBlock' && i.uiId === p.id,
          );
          if (block) {
            block.tree = p.tree;
            block.rev = p.rev;
            block.actionsEnabled = p.actionsEnabled;
            this.dirtyItem(block.id);
          }
          const i = this.items.find((i) => i.id === p.id);
          if (i) {
            this.dirtyItem(i.id);
            i.uiDisplay = {
              rev: p.rev,
              tree: p.tree,
              actionsEnabled: p.actionsEnabled,
            };
          }
        }
        break;
      }
      default:
        return false;
    }
    if (
      [
        'thread/updated',
        'turn/started',
        'turn/completed',
        'prompt/open',
        'prompt/closed',
        'goal/updated',
      ].includes(m.method)
    )
      this.inventoryVersion++;
    if (m.eventId) this.cursor = m.eventId;
    return true;
  }
}
// Same URL allowlist as ui.SafeURL: absolute HTTPS or HTTP loopback only.
export function safeURL(s: string) {
  if (s.length > 2048 || /[\\\s\u0000-\u001f\u007f-\u009f]/.test(s))
    return false;
  try {
    const u = new URL(s);
    if (u.username || u.password || !u.hostname) return false;
    if (u.protocol === 'https:') return true;
    return (
      u.protocol === 'http:' &&
      (u.hostname === 'localhost' ||
        u.hostname === '[::1]' ||
        /^127\.(\d{1,3}\.){2}\d{1,3}$/.test(u.hostname))
    );
  } catch {
    return false;
  }
}
export function clean(s: any) {
  return String(s ?? '')
    .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, '')
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '')
    .replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g, '');
}
export function imageResource(resource: string) {
  const m = /^([A-Za-z0-9_-]+)-image-(\d+)$/.exec(resource);
  return m ? { itemId: m[1], index: Number(m[2]) } : null;
}

// Inventory is a preview, never a second Markdown drawing.
export function plainMarkdown(text: string) {
  let source = clean(text);
  // Code is already plain text: retain snake_case and literal pipes inside it.
  // Protect it while removing markup from prose, then restore without HTML.
  let prefix = '\ufffc';
  while (source.includes(prefix)) prefix += '\ufffc';
  const code: string[] = [];
  const hold = (value: string) => prefix + (code.push(value) - 1) + prefix;
  source = source
    .replace(
      /(`{3,}|~{3,})[^\n]*\n([\s\S]*?)(?:\n\1|$)/g,
      (_all, _fence, body) => hold(body),
    )
    .replace(/(`+)([\s\S]*?)\1/g, (_all, _ticks, body) => hold(body))
    .replace(/!?\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/<(https?:\/\/[^>]+)>/g, '$1')
    .replace(/^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)+\|?\s*$/gm, '')
    .replace(/^\s*(?:#{1,6}\s+|>\s?|[-*+]\s+|\d+[.)]\s+)/gm, '')
    .replace(
      /(^|[^\w])(\*{1,3}|_{1,3}|~~)(?=\S)(.+?\S|\S)\2(?=$|[^\w])/g,
      '$1$3',
    )
    .replace(/\|/g, ' ');
  code.forEach((value, i) => {
    source = source.split(prefix + i + prefix).join(value);
  });
  return source.replace(/\s+/g, ' ').trim();
}
export function relativeTime(value: string, now = Date.now()) {
  const time = Date.parse(value);
  if (!Number.isFinite(time)) return '';
  const minutes = Math.max(0, Math.floor((now - time) / 60000));
  return minutes < 1
    ? 'now'
    : minutes < 60
      ? minutes + 'm'
      : minutes < 1440
        ? Math.floor(minutes / 60) + 'h'
        : Math.floor(minutes / 1440) + 'd';
}
export function sessionState(row: Data) {
  return row.error || ['error', 'failed'].includes(row.agent?.lastTurn?.status)
    ? 'error'
    : row.openPrompt || row.goalWaiting
      ? 'needs-you'
      : row.busy
        ? 'busy'
        : 'idle';
}
