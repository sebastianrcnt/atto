// New revision-3 client. Beautiful UI Chat/Thinking/Streaming Text/Tool Chips/
// Prompt Bar adaptations; Vercel Queue/Checkpoint/Context/Agent/Terminal are
// behavior references only. No React, remote scripts or frozen wire shapes.
import {
  Data,
  Tree,
  RPC,
  View,
  catalog,
  imageResource,
  clean,
  plainMarkdown,
  relativeTime,
  sessionState,
} from './core';
import {
  el,
  button,
  markdown,
  code,
  Local,
  render,
  validTree,
  Context,
} from './elements';
const root = document.getElementById('app')!;
const views = new Map<string, View>();
const buffers = new Map<string, Data[]>();
const locals = new Map<string, Local>();
const drafts = new Map<string, string>();
const attachments = new Map<string, Data[]>();
const commands = new Map<string, Data[]>();
const scrolls = new Map<string, number>();
const pages = new Set<string>();
const paneStates = new Map<string, { scroll: number; width?: number }>();
let active = '',
  online = false,
  sidebar = false,
  filter = 'active',
  models: Data[] = [],
  inventory: Data[] = [],
  rpc: RPC,
  socket: WebSocket,
  paneTab = new Map<string, string>(),
  clientId = '';
let localModal: HTMLElement | null = null,
  context: Data | null = null,
  notice = '',
  noticeTimer: ReturnType<typeof setTimeout>,
  frame = 0;
let focusHint = '';
const originals = new Set<string>();
const imageCache = new Map<string, Promise<string>>();
let reconnectTimer: ReturnType<typeof setTimeout>;
function warn(e: any) {
  notice = clean(e?.message || e);
  clearTimeout(noticeTimer);
  noticeTimer = setTimeout(() => {
    notice = '';
    schedule();
  }, 8000);
  schedule();
}
async function run(fn: () => Promise<any>) {
  try {
    return await fn();
  } catch (e) {
    warn(e);
    return null;
  }
}
// While an IME composes (Korean, Japanese, …) the prompt must not be
// replaced, or the half-typed syllable is lost: paints wait for its end.
let composing = false;
function schedule() {
  if (!frame)
    frame = requestAnimationFrame(() => {
      frame = 0;
      if (!composing) paint();
    });
}
// The prompt starts as one line and grows with its text, up to 30% of the
// window.
function growPrompt(input: HTMLTextAreaElement) {
  input.style.height = 'auto';
  input.style.height = Math.min(input.scrollHeight, innerHeight * 0.3) + 'px';
}
// A compact toolbar picker: the visible label is small text sized to its
// content, the native select sits invisibly on top of it (so taps and keys
// open the system picker, and on touch devices it keeps its 16px font, below
// which iOS zooms on focus).
function picker(select: HTMLSelectElement, title: string) {
  const wrap = el('span', null, 'picker');
  const label = el('span', null, 'picker-label');
  const sync = () => {
    const o = Array.from(select.querySelectorAll('option')).find(
      (o) => o.value === select.value,
    );
    label.textContent = o?.textContent || select.value;
  };
  sync();
  const change = select.onchange;
  select.onchange = (e) => {
    sync();
    change?.call(select, e);
  };
  wrap.title = title;
  wrap.append(label, select);
  return wrap;
}
// Only the slash-command menu depends on the typed text.
function slashMenu(text: string) {
  return /^\/[^\s]*$/.test(text) ? text : '';
}
function current() {
  return views.get(active);
}
function writable(v?: View) {
  return online && !!v && !v.reset && !v.info.readOnly && !v.info.offline;
}
function call(method: string, p: Data = {}, id = active) {
  if (!online) return Promise.reject(new Error('Not connected'));
  return rpc.call(method, { threadId: id, ...p });
}
async function settings(method: string, p: Data) {
  const id = active,
    v = current();
  const info = await call(method, p, id);
  if (v && info) Object.assign(v.info, info);
  schedule();
}
async function refreshInventory() {
  const requestedFilter = filter;
  const r = await rpc.call('thread/list', {
    archived: requestedFilter === 'archived',
    includeArchived: requestedFilter === 'all',
    includeAgents: true,
    includeClosedAgents: requestedFilter !== 'active',
  });
  if (requestedFilter !== filter) return;
  const rows = r.threads || [];
  if (JSON.stringify(rows) !== JSON.stringify(inventory)) inventory = rows;
  schedule();
}
async function loadCommands(id: string) {
  const r = await call('commands/list', {}, id);
  commands.set(id, r.commands || []);
  schedule();
}
// Subscribe on connection first, buffer while reading, then apply only events
// newer than the lane-consistent snapshot. Branch movement invalidates pages.
async function hydrate(id: string, method = 'thread/read', extra: Data = {}) {
  if (buffers.has(id)) return;
  const buffer: Data[] = [];
  buffers.set(id, buffer);
  let v = views.get(id);
  if (v) {
    v.reset = true;
    v.generation++;
  }
  try {
    const s = await call(method, { limit: 100, ...extra }, id);
    if (buffers.get(id) !== buffer) return;
    const buffered = buffer;
    buffers.delete(id);
    v = v || new View();
    v.snapshot(s);
    for (const key of locals.keys())
      if (
        key.startsWith(s.threadId + '\0') &&
        !s.ui?.instances?.some(
          (i: Data) => key === s.threadId + '\0' + i.site + '\0' + i.id,
        ) &&
        !v.items.some(
          (i) => key.endsWith('\0' + i.id) || key.endsWith('\0' + i.uiId),
        )
      )
        locals.delete(key);
    views.set(s.threadId, v);
    if (s.threadId !== id) {
      views.delete(id);
      drafts.set(s.threadId, drafts.get(id) || '');
      active = s.threadId;
    }
    for (const e of buffered) v.apply(e);
    if (v.reset) {
      await hydrate(s.threadId);
      return;
    }
    if (!s.offline) await loadCommands(s.threadId);
    schedule();
  } catch (e) {
    buffers.delete(id);
    throw e;
  }
}
async function open(row?: Data) {
  if (row?.archived) {
    await hydrate(row.threadId, 'thread/read', { offline: true });
    active = row.threadId;
  } else if (row) {
    active = row.threadId;
    await hydrate(active, 'thread/resume');
  } else {
    const key = 'starting';
    buffers.set(key, []);
    try {
      const s = await rpc.call('thread/start', {});
      const v = new View();
      v.snapshot(s);
      for (const key of locals.keys())
        if (
          key.startsWith(s.threadId + '\0') &&
          !s.ui?.instances?.some(
            (i: Data) => key === s.threadId + '\0' + i.site + '\0' + i.id,
          ) &&
          !v.items.some(
            (i) => key.endsWith('\0' + i.id) || key.endsWith('\0' + i.uiId),
          )
        )
          locals.delete(key);
      views.set(s.threadId, v);
      active = s.threadId;
      for (const n of buffers.get(key) || []) v.apply(n);
      await loadCommands(active);
    } finally {
      buffers.delete(key);
    }
  }
  sidebar = false;
  context = null;
  schedule();
  await refreshInventory();
}
async function detach(id: string) {
  try {
    await call('thread/detach', {}, id);
  } finally {
    views.delete(id);
    buffers.delete(id);
    for (const key of locals.keys())
      if (key.startsWith(id + '\0')) locals.delete(key);
    if (active === id) active = views.keys().next().value || '';
    paneStates.delete(id);
    schedule();
  }
}
function event(m: Data) {
  const id = m.params?.threadId;
  if (m.method === 'input/recovered' && m.params?.clientId === clientId) {
    const p = m.params,
      cur = drafts.get(id) || '';
    if (!p.ifEmpty || !cur) {
      drafts.set(id, (p.text || '') + (cur ? '\n\n' + cur : ''));
      recoverImages(id, p.images || []);
      schedule();
    }
    return;
  }
  if (m.method === 'events/reset' && !id) {
    for (const [tid] of views)
      if (!buffers.has(tid)) void run(() => hydrate(tid));
    return;
  }
  if (buffers.has('starting')) {
    const a = buffers.get('starting')!;
    if (a.length < 4096) a.push(m);
  }
  if (id && buffers.has(id)) {
    const a = buffers.get(id)!;
    if (a.length >= 4096) {
      a.length = 0;
      a.push({ method: 'events/reset', params: { threadId: id } });
    } else a.push(m);
    return;
  }
  const v = views.get(id);
  if (!v) return;
  if (v.apply(m)) {
    if (v.reset) void run(() => hydrate(id));
    if (m.params?.focusClientId === clientId && !(drafts.get(id) || '')) {
      focusHint = id + '\0' + m.params.site + '\0' + m.params.id;
      if (m.params.site === 'pane') paneTab.set(id, m.params.id);
    }
    schedule();
  }
  if (m.method === 'commands/changed' || m.method === 'thread/reloaded')
    void run(() => loadCommands(id));
}
// Open tabs survive a reload: their thread IDs (and whether each was read
// offline, an archived session) and the active one live in localStorage, the
// active one also in location.hash (#s=<threadId>) so a reload or bookmark of a
// session works without storage. The hash wins. Storage may be unavailable.
const tabsKey = 'atto.web.tabs';
let restored = false,
  restoring = false,
  savedTabs = '';
function hashThread() {
  const m = /^#s=(.+)$/.exec(location.hash || '');
  if (!m) return '';
  try {
    return decodeURIComponent(m[1]);
  } catch {
    return '';
  }
}
type SavedTabs = { tabs: { id: string; offline?: boolean }[]; active: string };
function loadTabs(): SavedTabs {
  try {
    const d = JSON.parse(localStorage.getItem(tabsKey) || 'null');
    if (d && Array.isArray(d.tabs))
      return {
        tabs: d.tabs.filter((t: Data) => t && typeof t.id === 'string'),
        active: typeof d.active === 'string' ? d.active : '',
      };
  } catch {}
  return { tabs: [], active: '' };
}
// Called from paint once the first restore attempt is over.
function persistTabs() {
  const state = JSON.stringify({
    tabs: [...views].map(([id, v]) => ({ id, offline: !!v.info.offline })),
    active,
  });
  if (state !== savedTabs) {
    savedTabs = state;
    try {
      localStorage.setItem(tabsKey, state);
    } catch {}
  }
  const hash = active ? '#s=' + encodeURIComponent(active) : '';
  if ((location.hash || '') !== hash)
    try {
      history.replaceState(
        null,
        '',
        hash || location.pathname + location.search,
      );
    } catch {}
}
// Re-open threads like open(row): archived ones are read offline, the others
// resumed. Threads that no longer exist are dropped silently. Never starts one.
async function reopen(ids: string[]) {
  const r = await rpc.call('thread/list', {
    includeArchived: true,
    includeAgents: true,
    includeClosedAgents: true,
  });
  const rows = new Map<string, Data>(
    (r.threads || []).map((t: Data) => [t.threadId, t]),
  );
  await Promise.all(
    ids.map(async (id) => {
      const row = rows.get(id);
      if (!row || views.has(id)) return;
      try {
        if (row.archived) await hydrate(id, 'thread/read', { offline: true });
        else await hydrate(id, 'thread/resume');
      } catch {}
    }),
  );
}
async function restoreTabs() {
  const saved = loadTabs(),
    hashed = hashThread();
  const ids = saved.tabs.map((t) => t.id);
  if (hashed && !ids.includes(hashed)) ids.push(hashed);
  restoring = true;
  schedule();
  try {
    if (ids.length) await reopen(ids);
    // Tabs keep their saved order, whichever finished loading first.
    const opened = [...views];
    views.clear();
    for (const id of ids) {
      const v = opened.find(([key]) => key === id)?.[1];
      if (v) views.set(id, v);
    }
    for (const [id, v] of opened) if (!views.has(id)) views.set(id, v);
    active =
      [hashed, saved.active, active, ...views.keys()].find(
        (id) => !!id && views.has(id),
      ) || '';
  } finally {
    restoring = false;
    restored = true;
    schedule();
  }
}
window.onhashchange = () => {
  const id = hashThread();
  if (!id || id === active || !online) return;
  if (views.has(id)) {
    active = id;
    context = null;
    schedule();
  } else
    void run(async () => {
      await reopen([id]);
      if (views.has(id)) active = id;
      schedule();
    });
};
function connect() {
  clearTimeout(reconnectTimer);
  socket = new WebSocket(
    (location.protocol === 'https:' ? 'wss://' : 'ws://') +
      location.host +
      '/ws',
    ['atto.rpc.v3'],
  );
  rpc = new RPC((s) => {
    if (socket.readyState !== WebSocket.OPEN) throw Error('Disconnected');
    socket.send(s);
  }, event);
  socket.onmessage = (e) => {
    try {
      rpc.receive(e.data);
    } catch {
      warn('Invalid protocol message');
      socket.close();
    }
  };
  socket.onopen = () =>
    void run(async () => {
      const init = await rpc.call('initialize', {
        protocolVersions: [3],
        clientInfo: { name: 'atto-web', title: 'atto Web', version: '1' },
        capabilities: {
          interactive: true,
          images: true,
          ui: {
            version: 1,
            surface: 'web',
            width: columns(),
            elements: catalog,
          },
        },
      });
      clientId = init.clientId;
      rpc.notify('initialized');
      online = true;
      await Promise.all(
        [...views.keys()].map((id) =>
          hydrate(
            id,
            views.get(id)?.info.offline ? 'thread/read' : 'thread/resume',
            views.get(id)?.info.offline ? { offline: true } : {},
          ),
        ),
      );
      models = (await rpc.call('models/list')).models || [];
      if (!restored) await restoreTabs();
      await refreshInventory();
      schedule();
    });
  socket.onclose = () => {
    online = false;
    rpc.close();
    buffers.clear();
    for (const v of views.values()) {
      v.reset = true;
      v.generation++;
    }
    schedule();
    reconnectTimer = setTimeout(connect, 2000);
  };
  socket.onerror = () =>
    warn(
      'Cannot connect. Check that atto serve (or /remote) is still running.',
    );
}
function columns() {
  const probe = el('span', '0000000000', 'ui-tree');
  probe.style.position = 'absolute';
  probe.style.visibility = 'hidden';
  document.body.append(probe);
  const width = probe.getBoundingClientRect().width / 10 || 8;
  probe.remove();
  return Math.min(512, Math.max(1, Math.floor(innerWidth / width)));
}
let resizeTimer: ReturnType<typeof setTimeout>;
window.onresize = () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => {
    if (online)
      rpc.notify('ui/capabilities', {
        surface: 'web',
        width: columns(),
        elements: catalog,
      });
    schedule();
  }, 150);
};
async function earlier(v: View) {
  const id = v.info.threadId;
  if (pages.has(id) || !v.info.hasMore || v.reset) return;
  pages.add(id);
  schedule();
  const generation = v.generation;
  try {
    const page = await call(
      'thread/items',
      { before: v.info.before, limit: 100, offline: !!v.info.offline },
      id,
    );
    if (v.prepend(page, generation)) schedule();
  } finally {
    pages.delete(id);
    schedule();
  }
}
function siteLocal(id: string, site: string, key: string) {
  const k = id + '\0' + site + '\0' + key;
  let l = locals.get(k);
  if (!l) {
    l = new Local();
    locals.set(k, l);
  }
  return l;
}
function image(resource: string, img: HTMLImageElement, v: View) {
  const ref = imageResource(resource);
  if (!ref) {
    img.replaceWith(el('span', '[image: ' + img.alt + ']', 'muted'));
    return;
  }
  const k = v.info.threadId + '\0' + resource;
  let pending = imageCache.get(k);
  if (!pending) {
    pending = call(
      'item/image',
      { ...ref, offline: !!v.info.offline },
      v.info.threadId,
    ).then((r) => {
      if (
        !['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(
          r.mimeType,
        )
      )
        throw Error('Unsupported image');
      const bytes = Uint8Array.from(atob(r.data), (c) => c.charCodeAt(0));
      if (bytes.length > 10 << 20) throw Error('Image too large');
      return URL.createObjectURL(new Blob([bytes], { type: r.mimeType }));
    });
    imageCache.set(k, pending);
    while (imageCache.size > 40) {
      const key = imageCache.keys().next().value!;
      const old = imageCache.get(key)!;
      imageCache.delete(key);
      old.then(
        (url) => URL.revokeObjectURL(url),
        () => {},
      );
    }
  }
  pending.then(
    (url) => {
      img.src = url;
    },
    () => {
      img.replaceWith(
        el('span', '[image unavailable: ' + img.alt + ']', 'muted'),
      );
    },
  );
}
const treeDOM = new Map<
  string,
  {
    rev: number;
    node: HTMLElement;
    tree: Tree;
    context: Context;
    item?: Data;
    engineNode?: HTMLElement;
    overrides?: Data;
  }
>();
function uiTree(v: View, i: Data, item?: Data) {
  const scope = v.info.threadId + '\0' + i.site + '\0' + i.id;
  const enabled =
    writable(v) &&
    (!item ||
      !!item.uiDisplay?.actionsEnabled ||
      (item.type === 'uiBlock' && !!item.actionsEnabled));
  const old = treeDOM.get(scope);
  if (
    old &&
    old.rev === i.rev &&
    (i.id !== 'atto/context' || old.tree === i.tree)
  ) {
    old.item = item;
    if (item && old.engineNode) {
      const next = nativeItem(v, { ...item, ...old.overrides }, false);
      if (old.node === old.engineNode) {
        next.classList.add('ui-tree');
        next.dataset.scope = scope;
        old.node = next;
      }
      old.engineNode.replaceWith(next);
      old.engineNode = next;
    }
    old.context.enabled = enabled;
    syncEnabled(old.node, enabled);
    return old.node;
  }
  if (item && i.tree && !validTree(i.tree, i.site, i.id))
    return nativeItem(v, item);
  let engineNode: HTMLElement | undefined, overrides: Data | undefined;
  const renderContext: Context = {
    site: i.site,
    id: i.id,
    rev: i.rev,
    enabled,
    local: siteLocal(v.info.threadId, i.site, i.id),
    focus: focusHint === scope,
    action: (key, type, value) =>
      void run(async () => {
        try {
          await call(
            'ui/event',
            {
              site: i.site,
              id: i.id,
              rev: i.rev,
              key,
              type,
              ...(value === undefined ? {} : { value }),
            },
            v.info.threadId,
          );
        } catch (e) {
          await hydrate(v.info.threadId);
          throw e;
        }
      }),
    engine: item
      ? (n) => {
          overrides = n.props.overrides;
          return (engineNode = nativeItem(v, { ...item, ...overrides }, false));
        }
      : undefined,
    image: (r, img) => image(r, img, v),
  };
  const tree = render(i.tree, renderContext);
  tree.dataset.scope = scope;
  syncEnabled(tree, enabled);
  treeDOM.set(scope, {
    rev: i.rev,
    node: tree,
    tree: i.tree,
    context: renderContext,
    item,
    engineNode,
    overrides,
  });
  return tree;
}
function itemSite(i: Data) {
  return i.type === 'userMessage'
    ? 'userMessage'
    : i.type === 'commandExecution'
      ? 'toolCall'
      : i.type === 'notice'
        ? 'notice'
        : 'assistantMessage';
}
function nativeItem(v: View, i: Data, meta = true) {
  const e = el(
    'article',
    null,
    'item ' + (i.type === 'userMessage' ? 'user' : i.type),
  );
  e.dataset.item = i.id;
  if (meta && ['userMessage', 'agentMessage'].includes(i.type)) {
    const head = el('header', i.type === 'userMessage' ? 'You' : 'Atto');
    if (i.type === 'userMessage' && i.entryId) {
      // Edit forks at this message: a new session with its text in the editor.
      const edit = button(
        'Edit',
        () =>
          void run(async () => {
            const r = await call(
              'thread/fork',
              { entryId: i.entryId },
              v.info.threadId,
            );
            await open({ threadId: r.threadId });
            if (r.input) drafts.set(active, r.input);
            recoverImages(active, r.images || []);
            schedule();
          }),
        !writable(v),
      );
      edit.className = 'message-edit';
      edit.title = 'Edit and resend from here, in a new session';
      head.append(edit);
    }
    e.append(head);
  }
  if (i.type === 'commandExecution') {
    const details = el('details', null, 'tool-chip');
    const key = v.info.threadId + '\0tool\0' + i.id;
    const l = siteLocal(v.info.threadId, 'native', i.id);
    details.open = i.status === 'inProgress' || (l.open.get(key) ?? false);
    const title = el('summary');
    const label = el(
      'span',
      (i.description === 'user command' ? 'Shell command' : i.description) ||
        i.command?.split('\n')[0] ||
        'Run command',
      'tool-title',
    );
    title.append(
      el(
        'span',
        null,
        'state-dot ' +
          (i.status === 'inProgress'
            ? 'busy'
            : i.exitCode
              ? 'error'
              : 'success'),
      ),
      label,
    );
    const metrics = [
      i.exitCode != null ? 'exit ' + i.exitCode : 'Running',
      i.durationMs != null ? (i.durationMs / 1000).toFixed(1) + 's' : '',
    ]
      .filter(Boolean)
      .join(' · ');
    title.append(el('small', metrics, 'tool-metrics'));
    const output = i.output || '';
    const expanded = l.open.get('output') || false;
    const lines = output.split('\n');
    const clipped = !expanded && (lines.length > 18 || output.length > 4000);
    const preview = clipped ? lines.slice(-18).join('\n').slice(-4000) : output;
    // No output yet (a running command): no empty box under the command.
    const body = streamBody(code(preview));
    body.hidden = !output;
    details.append(
      title,
      code(i.command || '', { language: i.shell ? 'shell' : 'bash' }),
      body,
    );
    // Only user disclosure choices persist. Browser toggle events also fire on
    // insertion; remembering those accidentally kept completed tools open.
    title.onclick = () => {
      if (i.status !== 'inProgress') l.open.set(key, !details.open);
    };
    if (clipped || expanded)
      details.append(
        button(expanded ? 'Show less' : 'Show more', () => {
          l.open.set('output', !expanded);
          invalidateItem(v, i.id);
        }),
      );
    if (i.fullOutput || i.truncated || i.dropped)
      details.append(
        button(
          'Full output',
          () =>
            void run(async () => {
              const r = await call(
                'item/output',
                { itemId: i.id, offline: !!v.info.offline },
                v.info.threadId,
              );
              showModal(
                'Command output',
                code(r.output + (r.truncated ? '\n[truncated]' : '')),
              );
            }),
        ),
      );
    e.append(details);
  } else if (i.type === 'reasoning') {
    const details = el('details', null, 'thinking');
    const l = siteLocal(v.info.threadId, 'native', i.id);
    details.open = l.open.get('open') || false;
    details.onclick = (event) => {
      if ((event.target as HTMLElement).closest('summary'))
        l.open.set('open', !details.open);
    };
    details.append(
      el(
        'summary',
        i.status === 'inProgress' ? 'Thinking…' : 'Thinking',
        i.status === 'inProgress' ? 'shimmer' : '',
      ),
      streamBody(markdown(i.text || '')),
    );
    e.append(details);
  } else if (i.type === 'event') {
    // [atto event]s (job exits, timers, monitors, agent messages): their
    // titles, as the TUI shows them; the full text the model got, verbatim,
    // when opened (job output keeps its lines).
    const details = el('details', null, 'thinking event');
    const l = siteLocal(v.info.threadId, 'native', i.id);
    details.open = l.open.get('open') || false;
    details.onclick = (event) => {
      if ((event.target as HTMLElement).closest('summary'))
        l.open.set('open', !details.open);
    };
    const titles: string[] = i.titles?.length
      ? i.titles
      : [String(i.text || '').split('\n')[0]];
    details.append(
      el(
        'summary',
        titles.map((t) => t.replace(/^\[atto event\]\s*/, '')).join('\n'),
      ),
      streamBody(code(i.text || '')),
    );
    e.append(details);
  } else if (i.type === 'uiBlock') {
    e.append(
      uiTree(
        v,
        { site: 'transcript', id: i.uiId, rev: i.rev, tree: i.tree },
        i,
      ),
    );
  } else if (i.loaded) {
    const details = el('details', null, 'thinking');
    details.append(
      el(
        'summary',
        (i.reloaded ? 'Reloaded' : 'Loaded') +
          ' context · ' +
          (i.loaded.model?.name || v.info.model),
      ),
      code(JSON.stringify(i.loaded, null, 2)),
    );
    e.append(details);
    if (i.note) e.append(el('p', i.note));
  } else {
    if (i.title) e.append(el('strong', i.title));
    const text = markdown(
      i.text ||
        i.resultText ||
        i.reason ||
        (i.type === 'compaction'
          ? 'Context checkpoint · ' +
            (i.tokensBefore || 0) +
            ' → ' +
            (i.tokensAfter || 0) +
            ' tokens'
          : ''),
    );
    if (i.level === 'warning' || i.level === 'error')
      text.style.color = `var(--${i.level})`;
    if (i.status === 'inProgress') text.classList.add('streaming');
    e.append(streamBody(text));
  }
  for (let index = 0; index < (i.images || []).length; index++) {
    const img = el('img');
    img.alt = i.images[index].name || 'Attached image';
    image(i.id + '-image-' + index, img, v);
    e.append(img);
  }
  return e;
}
function drawTranscriptItem(v: View, i: Data) {
  if (i.type === 'uiBlock' && !i.tree) {
    const placeholder = el('span');
    placeholder.dataset.item = i.id;
    return placeholder;
  }
  const key = v.info.threadId + '\0' + i.id;
  if (!i.uiDisplay?.tree || originals.has(key)) {
    const n = nativeItem(v, i);
    if (i.uiDisplay?.tree)
      n.append(
        button('Show extension drawing', () => {
          originals.delete(key);
          invalidateItem(v, i.id);
        }),
      );
    return n;
  }
  const e = el('article', null, 'item');
  e.dataset.item = i.id;
  e.append(
    uiTree(
      v,
      {
        site: itemSite(i),
        id: i.id,
        rev: i.uiDisplay.rev,
        tree: i.uiDisplay.tree,
      },
      i,
    ),
    button('Show original', () => {
      originals.add(key);
      invalidateItem(v, i.id);
    }),
  );
  return e;
}
// Cache keyed items only for loaded pages; full item events replace one row.
// Deltas retain the row, disclosure, headers and images and touch only its body.
const itemDOM = new Map<
  string,
  { node: HTMLElement; type: string; rev?: number; uiId?: string }
>();
function streamBody(node: HTMLElement) {
  node.classList.add('stream-body');
  return node;
}
function invalidateItem(v: View, id: string) {
  v.dirtyItem(id);
  schedule();
}
function transcriptItem(v: View, i: Data) {
  const key = v.info.threadId + '\0' + i.id;
  const old = itemDOM.get(key);
  if (old && !v.dirtyItems.has(i.id)) return old.node;
  if (
    old &&
    i.type === 'uiBlock' &&
    old.type === i.type &&
    old.uiId === i.uiId &&
    old.rev === i.rev
  ) {
    const cached = treeDOM.get(v.info.threadId + '\0transcript\0' + i.uiId);
    if (cached) cached.item = i;
    return old.node;
  }
  if (old && v.deltaItems.has(i.id)) {
    const drawing =
      i.uiDisplay?.tree && !originals.has(key)
        ? treeDOM.get(v.info.threadId + '\0' + itemSite(i) + '\0' + i.id)
        : undefined;
    // A pure extension drawing has no native text to update. Engine overrides
    // are display-only: a constant override stays constant while data streams.
    if (
      drawing &&
      (!drawing.engineNode ||
        (i.type === 'commandExecution' ? 'output' : 'text') in
          (drawing.overrides || {}))
    )
      return old.node;
    const body = old.node.querySelector<HTMLElement>('.stream-body');
    if (body) {
      if (i.type === 'commandExecution') {
        const output = i.output || '';
        const expanded = siteLocal(v.info.threadId, 'native', i.id).open.get(
          'output',
        );
        const lines = output.split('\n');
        const clipped =
          !expanded && (lines.length > 18 || output.length > 4000);
        body.replaceChildren(
          ...Array.from(
            code(clipped ? lines.slice(-18).join('\n').slice(-4000) : output)
              .childNodes,
          ),
        );
        if (body.hidden !== !output) body.hidden = !output;
        let more = old.node.querySelector<HTMLButtonElement>('.output-more');
        if (clipped && !more) {
          more = button('Show more', () => {
            siteLocal(v.info.threadId, 'native', i.id).open.set('output', true);
            invalidateItem(v, i.id);
          });
          more.className = 'output-more';
          body.parentNode!.appendChild(more);
        }
      } else {
        const text =
          i.type === 'event' ? code(i.text || '') : markdown(i.text || '');
        if (i.status === 'inProgress') text.classList.add('streaming');
        body.replaceChildren(...Array.from(text.childNodes));
      }
      return old.node;
    }
  }
  const node = drawTranscriptItem(v, i);
  for (const b of node.querySelectorAll<HTMLButtonElement>('button'))
    if (b.textContent === 'Show more' || b.textContent === 'Show less')
      b.classList.add('output-more');
  itemDOM.set(key, { node, type: i.type, rev: i.rev, uiId: i.uiId });
  return node;
}
// While a turn runs with no text streaming (thinking, a tool running, waiting
// on the model), a quiet cursor sits after the last transcript item. It is its
// own small region: showing or hiding it never redraws a transcript item.
function transcriptTail(v: View) {
  let streaming = false;
  for (let n = v.items.length - 1; n >= 0; n--) {
    const i = v.items[n];
    if (
      i.status === 'inProgress' &&
      (i.type === 'agentMessage' || i.type === 'reasoning')
    ) {
      streaming = true;
      break;
    }
    if (i.type === 'userMessage') break;
  }
  const activity =
    typeof v.info.activity === 'string'
      ? v.info.activity
      : v.info.activity?.phase;
  const phase = !v.info.busy || streaming ? '' : activity || 'Thinking';
  return region(v.info.threadId + '\0tail', [phase], () => {
    const tail = el('div', null, 'transcript-tail');
    tail.setAttribute('aria-live', 'polite');
    if (phase)
      tail.append(
        el('span', '▌', 'tail-cursor'),
        el('span', phase.replace(/…$/, '') + '…', 'tail-phase'),
      );
    return tail;
  });
}
function showModal(
  title: string,
  body: HTMLElement,
  actions: HTMLElement[] = [],
) {
  const card = el('section', null, 'approval-card');
  card.setAttribute('role', 'dialog');
  card.setAttribute('aria-modal', 'true');
  card.setAttribute('aria-label', title);
  card.append(el('h2', title), body);
  const bar = el('div', null, 'toolbar');
  bar.append(
    ...actions,
    button('Close', () => {
      localModal = null;
      schedule();
    }),
  );
  card.append(bar);
  localModal = card;
  schedule();
}
function pane(v: View, instances: Data[], above: boolean) {
  const container = el('aside', null, above ? 'pane-above' : 'pane-dock');
  container.dataset.thread = v.info.threadId;
  const tabs = el('div', null, 'pane-tabs');
  const selected = paneTab.get(v.info.threadId);
  const picked = instances.find((i) => i.id === selected) || instances[0];
  paneTab.set(v.info.threadId, picked.id);
  if (above)
    container.style.maxHeight =
      Math.min(
        picked.options?.rows || 8,
        Math.max(2, Math.floor(innerHeight / 22 / 3)),
      ) *
        22 +
      'px';
  else
    container.style.width =
      Math.max(
        32,
        Math.min(picked.options?.columns || 40, contentWidth() - 72),
      ) + 'ch';
  for (const i of instances)
    tabs.append(
      button(i.options?.title || i.id, () => {
        paneTab.set(v.info.threadId, i.id);
        schedule();
      }),
    );
  tabs.append(
    button(
      '×',
      () =>
        void run(() =>
          call(
            'ui/event',
            {
              site: 'pane',
              id: picked.id,
              key: '$site',
              type: 'close',
              rev: picked.rev,
            },
            v.info.threadId,
          ),
        ),
      !writable(v),
    ),
  );
  if (!above && paneStates.get(v.info.threadId)?.width)
    container.style.width =
      Math.max(
        256,
        Math.min(
          paneStates.get(v.info.threadId)!.width!,
          (contentWidth() - 72) * 8,
        ),
      ) + 'px';
  container.onscroll = () =>
    paneStates.set(v.info.threadId, {
      ...paneStates.get(v.info.threadId),
      scroll: container.scrollTop,
    });
  container.append(tabs, uiTree(v, picked));
  container.onkeydown = (e) => {
    if (e.key === 'Escape') {
      e.stopPropagation();
      document.getElementById('prompt')?.focus();
      if (picked.options?.closeOnEscape)
        void run(() =>
          call(
            'ui/event',
            {
              site: 'pane',
              id: picked.id,
              key: '$site',
              type: 'close',
              rev: picked.rev,
            },
            v.info.threadId,
          ),
        );
    }
  };
  return container;
}
const agentFolds = new Map<string, boolean>();
let rowMenu = '';
let foldVersion = 0;
function sortedRows() {
  const rows = inventory.map((row) => {
    const v = views.get(row.threadId);
    if (!v) return row;
    return {
      ...row,
      name: v.info.name,
      busy:
        v.info.busy ||
        v.items.some(
          (i) => i.type === 'commandExecution' && i.status === 'inProgress',
        ),
      openPrompt: !!v.info.prompt,
      // The row's preview is the session's first prompt; the loaded page of
      // a long session starts later.
      preview:
        row.preview || v.items.find((i) => i.type === 'userMessage')?.text,
      lastMessage:
        [...v.items].reverse().find((i) => i.type === 'agentMessage')?.text ||
        row.lastMessage,
    };
  });
  const map = new Map(rows.map((r) => [r.threadId, r]));
  const out: Data[] = [],
    seen = new Set<string>();
  function visit(r: Data, depth: number, project: string) {
    if (seen.has(r.threadId)) return;
    seen.add(r.threadId);
    const children = rows.filter((c) => c.agent?.parentThreadId === r.threadId);
    // Like the command center, only all-finished subtrees fold by default.
    // Explicit choices survive refreshes; cycle guards also cover corrupt files.
    const allFinished = (row: Data, seen = new Set<string>()): boolean => {
      if (seen.has(row.threadId)) return false;
      seen.add(row.threadId);
      return (
        !!(row.archived || row.agent?.lifecycle === 'closed') &&
        rows
          .filter((c) => c.agent?.parentThreadId === row.threadId)
          .every((c) => allFinished(c, seen))
      );
    };
    if (children.length && !agentFolds.has(r.threadId))
      agentFolds.set(
        r.threadId,
        children.every((c) => allFinished(c)),
      );
    const folded = agentFolds.get(r.threadId) || false;
    out.push({
      ...r,
      displayDepth: depth,
      project,
      childCount: children.length,
      folded,
    });
    for (const child of children) {
      if (!folded) visit(child, depth + 1, project);
      else hide(child);
    }
  }
  function hide(r: Data) {
    if (seen.has(r.threadId)) return;
    seen.add(r.threadId);
    for (const child of rows)
      if (child.agent?.parentThreadId === r.threadId) hide(child);
  }
  const roots = rows.filter((r) => !map.has(r.agent?.parentThreadId));
  const projects = [...new Set(roots.map((r) => r.cwd || 'Other sessions'))];
  for (const project of projects)
    for (const r of roots)
      if ((r.cwd || 'Other sessions') === project) visit(r, 0, project);
  for (const r of rows)
    if (!seen.has(r.threadId)) visit(r, 0, r.cwd || 'Other sessions');
  return out;
}
async function mutate(row: Data, method: string) {
  if (
    !confirm(
      (method === 'thread/delete' ? 'Permanently delete ' : 'Archive ') +
        (row.name || row.threadId) +
        '?',
    )
  )
    return;
  let stop = false;
  if (row.busy) {
    if (
      !confirm(
        'Stop its work and ' +
          (method === 'thread/delete' ? 'delete' : 'archive') +
          '?',
      )
    )
      return;
    stop = true;
  }
  await rpc.call(method, { threadId: row.threadId, stop });
  views.delete(row.threadId);
  if (active === row.threadId) active = views.keys().next().value || '';
  await refreshInventory();
  schedule();
}
// A session's title, the same in its tab, the sidebar and recents: its name,
// else its inventory row's agent name or preview (the session's first prompt).
// The first loaded user message stands in only when the inventory knows no
// prompt: a page of a long session starts after its first prompt.
function sessionTitle(row?: Data, v?: View) {
  return (
    v?.info.name ||
    row?.name ||
    row?.agent?.name ||
    row?.preview ||
    v?.items.find((i) => i.type === 'userMessage')?.text ||
    'New session'
  );
}
// A session row's main button (sidebar and the empty state's recents).
function sessionButton(row: Data) {
  const b = button('', () => void run(() => open(row)), !online);
  b.className = 'session-row';
  const title = plainMarkdown(sessionTitle(row));
  const heading = el('div', null, 'session-heading');
  const dot = el('span', null, 'state-dot ' + sessionState(row));
  dot.title = sessionState(row).replace('-', ' ');
  dot.setAttribute('aria-label', dot.title);
  heading.append(
    dot,
    el('span', title, 'session-title'),
    el('small', relativeTime(row.updatedAt), 'session-time'),
  );
  const preview = el(
    'small',
    plainMarkdown(row.lastMessage || row.preview || 'No messages yet'),
    'session-preview',
  );
  b.append(heading, preview);
  b.title = title;
  return b;
}
// The latest few top-level sessions, newest first.
function recentRows() {
  const time = (r: Data) => Date.parse(r.updatedAt) || 0;
  return inventory
    .filter((r) => !r.agent)
    .sort((a, b) => time(b) - time(a))
    .slice(0, 6);
}
function inventoryUI() {
  const s = el('aside', null, 'sidebar' + (sidebar ? ' visible' : ''));
  s.setAttribute('aria-label', 'Sessions');
  const header = el('header');
  const close = button('×', () => {
    sidebar = false;
    schedule();
  });
  close.className = 'sidebar-close';
  close.setAttribute('aria-label', 'Close sidebar');
  const refresh = button('↻', () => void run(refreshInventory), !online);
  refresh.setAttribute('aria-label', 'Refresh sessions');
  refresh.title = 'Refresh sessions';
  header.append(
    el('strong', 'atto'),
    button('+ New', () => void run(() => open()), !online),
    refresh,
    close,
  );
  s.append(header);
  const filters = el('div', null, 'segments');
  filters.setAttribute('aria-label', 'Session filter');
  for (const [value, label] of [
    ['active', 'Active'],
    ['all', 'All'],
    ['archived', 'Archived'],
  ]) {
    const b = button(label, () => {
      filter = value;
      rowMenu = '';
      void run(refreshInventory);
    });
    b.setAttribute('aria-pressed', String(filter === value));
    filters.append(b);
  }
  s.append(filters);
  const list = el('div', null, 'inventory');
  let project = '';
  for (const row of sortedRows()) {
    if (project !== row.project) {
      project = row.project;
      const heading = el(
        'div',
        project.split('/').filter(Boolean).pop() || project,
        'project-heading',
      );
      heading.title = project;
      list.append(heading);
    }
    const r = el(
      'div',
      null,
      'inventory-row' + (active === row.threadId ? ' active' : ''),
    );
    r.style.marginLeft = Math.min(row.displayDepth, 8) * 12 + 'px';
    const b = sessionButton(row);
    const title = b.title;
    r.append(b);
    const menu = button(
      '⋯',
      () => {
        rowMenu = rowMenu === row.threadId ? '' : row.threadId;
        schedule();
      },
      !online,
    );
    menu.className = 'row-menu-button';
    menu.setAttribute('aria-label', 'Actions for ' + title);
    menu.setAttribute('aria-expanded', String(rowMenu === row.threadId));
    r.append(menu);
    if (row.childCount) {
      const fold = button(
        (row.folded ? '▸ ' : '▾ ') + row.childCount + ' agents',
        () => {
          agentFolds.set(row.threadId, !row.folded);
          foldVersion++;
          schedule();
        },
      );
      fold.className = 'agent-fold';
      fold.setAttribute('aria-expanded', String(!row.folded));
      r.append(fold);
    }
    if (rowMenu === row.threadId) {
      const bar = el('div', null, 'row-menu');
      bar.append(
        button(
          row.archived ? 'Restore' : 'Archive',
          () =>
            void run(async () => {
              rowMenu = '';
              if (row.archived) {
                await rpc.call('thread/unarchive', { threadId: row.threadId });
                await refreshInventory();
              } else await mutate(row, 'thread/archive');
            }),
          !online,
        ),
        button(
          'Delete',
          () =>
            void run(async () => {
              rowMenu = '';
              await mutate(row, 'thread/delete');
            }),
          !online,
        ),
      );
      r.append(bar);
    }
    list.append(r);
  }
  if (!inventory.length)
    list.append(
      el(
        'p',
        filter === 'archived'
          ? 'No archived sessions'
          : 'Your sessions will appear here.',
        'inventory-empty',
      ),
    );
  s.append(
    list,
    el('small', online ? '● Connected' : '◌ Reconnecting…', 'connection-state'),
  );
  return s;
}
function recoverImages(id: string, images: Data[]) {
  const a = attachments.get(id) || [];
  for (const im of images) {
    if (a.length < 10)
      a.push({ file: im.file, mimeType: im.mimeType, name: im.name });
  }
  attachments.set(id, a);
}
async function addImages(files: FileList | File[]) {
  const id = active,
    v = current();
  if (!v || !writable(v)) return;
  const model = models.find((m) => m.id === v.info.model);
  if (!model?.images) {
    warn('This model does not support images. Pick an image-capable model.');
    return;
  }
  const list = attachments.get(id) || [];
  for (const file of Array.from(files)) {
    if (list.length >= 10) {
      warn('At most 10 images');
      break;
    }
    if (
      !['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(
        file.type,
      ) ||
      file.size > 10 << 20
    ) {
      warn('Use PNG/JPEG/GIF/WebP, at most 10 MiB each.');
      continue;
    }
    const data = await new Promise<string>((resolve, reject) => {
      const r = new FileReader();
      r.onload = () => resolve(String(r.result));
      r.onerror = reject;
      r.readAsDataURL(file);
    });
    list.push({ mimeType: file.type, data, name: file.name });
  }
  attachments.set(id, list);
  schedule();
}
async function nativeCommand(v: View, text: string) {
  const [name, ...rest] = text.slice(1).split(' ');
  const arg = rest.join(' ').trim();
  const c = (commands.get(v.info.threadId) || []).find((c) => c.name === name);
  switch (name) {
    case 'sessions':
    case 'agents':
    case 'resume':
      sidebar = true;
      await refreshInventory();
      schedule();
      return true;
    case 'new':
    case 'clear':
      await open();
      return true;
    case 'quit':
    case 'exit':
      await detach(v.info.threadId);
      return true;
    case 'close':
      if (confirm('Stop this session and all its work?')) {
        await call('thread/close', {}, v.info.threadId);
        await detach(v.info.threadId);
      }
      return true;
    // The checkpoint tree is a terminal feature; Edit on a message forks.
    case 'tree':
    case 'fork':
      warn(
        '/' +
          name +
          ' is only available in the terminal' +
          (name === 'fork' ? '. Use Edit on a message to resend from there.' : '.'),
      );
      return true;
    case 'archive':
      await mutate({ ...v.info, threadId: v.info.threadId }, 'thread/archive');
      return true;
    case 'context':
      return false;
    case 'copy': {
      const last = [...v.items]
        .reverse()
        .find((i) => i.type === 'agentMessage');
      if (last) {
        try {
          if (!navigator.clipboard) throw Error('Clipboard unavailable');
          await navigator.clipboard.writeText(last.text || '');
        } catch {
          showModal('Copy answer', code(last.text || ''));
        }
      }
      return true;
    }
    case 'model':
      if (!arg) {
        document.getElementById('model')?.focus();
        return true;
      }
      return false;
    case 'effort':
      if (!arg) {
        document.getElementById('effort')?.focus();
        return true;
      }
      return false;
    case 'remote':
      warn(
        'This page is already a remote client. Share the listener link from the machine running atto.',
      );
      return true;
    default:
      if (c?.local) {
        warn(
          '/' +
            name +
            ' is a terminal-only command; use the CLI for credentials/diagnostics.',
        );
        return true;
      }
  }
  return false;
}
const sending = new Set<string>();
async function submit(intent = 'auto') {
  const id = active,
    v = current();
  if (!v || !writable(v) || sending.has(id)) return;
  const text = drafts.get(id) || '',
    images = attachments.get(id) || [];
  if (!text.trim() && !images.length && !v.info.pending) return;
  sending.add(id);
  try {
    if (text.startsWith('/') && (await nativeCommand(v, text))) {
      drafts.set(id, '');
      schedule();
      return;
    }
    await call(
      'input/submit',
      {
        input: text,
        images: images.map(({ mimeType, data, name, file }) => ({
          mimeType,
          data,
          name,
          file,
        })),
        intent,
      },
      id,
    );
    if (drafts.get(id) === text) drafts.set(id, '');
    attachments.set(id, []);
    schedule();
  } finally {
    sending.delete(id);
  }
}
// A region keeps its outer node for its lifetime. Inputs are small metadata
// values/references, never serialized transcript text. Reconcile only changed
// children: inserting an older page never detaches the already loaded rows.
type Region = {
  node: HTMLElement;
  inputs: unknown[];
  scroll?: number;
  inventoryScroll?: number;
};
const regions = new Map<string, Region>();
function syncChildren(parent: HTMLElement, nodes: Node[]) {
  const wanted = new Set(nodes);
  for (const child of Array.from(parent.childNodes))
    if (!wanted.has(child)) child.remove();
  let cursor = parent.firstChild;
  for (const node of nodes) {
    if (node === cursor) cursor = cursor.nextSibling;
    else parent.insertBefore(node, cursor);
  }
  while (cursor) {
    const next = cursor.nextSibling;
    parent.removeChild(cursor);
    cursor = next;
  }
}
function region(key: string, inputs: unknown[], build: () => HTMLElement) {
  const old = regions.get(key);
  if (
    old &&
    inputs.length === old.inputs.length &&
    inputs.every((x, n) => x === old.inputs[n])
  )
    return old.node;
  if (
    old?.node.classList.contains('pane-dock') &&
    old.node.style.width.endsWith('px')
  ) {
    const id = old.node.dataset.thread!;
    paneStates.set(id, {
      ...paneStates.get(id),
      scroll: old.node.scrollTop,
      width: parseFloat(old.node.style.width),
    });
  }
  // Read offsets before a builder can move a cached tree out of this region.
  // Hidden mobile regions have no CSS box; retain their last visible offset.
  const scroll = old
    ? old.node.clientHeight
      ? old.node.scrollTop
      : old.scroll || 0
    : 0;
  const previousList = old?.node.querySelector<HTMLElement>('.inventory');
  const inventoryScroll = previousList?.clientHeight
    ? previousList.scrollTop
    : old?.inventoryScroll;
  const disclosures = old
    ? Array.from(old.node.querySelectorAll<HTMLDetailsElement>('details'))
    : [];
  const fresh = build();
  if (!old) {
    regions.set(key, { node: fresh, inputs });
    return fresh;
  }
  const node = old.node;
  old.scroll = scroll;
  old.inventoryScroll = inventoryScroll;
  for (const details of fresh.querySelectorAll<HTMLDetailsElement>('details')) {
    const previous = disclosures.find(
      (d) =>
        d.closest('[data-scope]')?.getAttribute('data-scope') ===
          details.closest('[data-scope]')?.getAttribute('data-scope') &&
        (d.dataset.key
          ? d.dataset.key === details.dataset.key
          : d.className && d.className === details.className),
    );
    if (previous) details.open = previous.open;
  }
  node.className = fresh.className;
  node.style.cssText = fresh.style.cssText;
  if (node.tagName === 'BUTTON')
    (node as HTMLButtonElement).disabled = (
      fresh as HTMLButtonElement
    ).disabled;
  node.onclick = fresh.onclick;
  node.onkeydown = fresh.onkeydown;
  node.onscroll = fresh.onscroll;
  syncChildren(node, Array.from(fresh.childNodes));
  node.scrollTop = scroll;
  const list = node.querySelector<HTMLElement>('.inventory');
  if (list && inventoryScroll != null) list.scrollTop = inventoryScroll;
  old.inputs = inputs;
  return node;
}
function siteVersions(instances: Data[]) {
  return instances.map((i) => i.site + ':' + i.id + ':' + i.rev).join('|');
}
function syncEnabled(node: HTMLElement, enabled: boolean) {
  for (const n of [
    node,
    ...Array.from(node.querySelectorAll<HTMLElement>('[data-ui-disabled]')),
  ]) {
    if (n.dataset.uiDisabled == null) continue;
    const disabled = !enabled || n.dataset.uiDisabled === 'true';
    if ((n as HTMLInputElement).disabled !== disabled)
      (n as HTMLInputElement).disabled = disabled;
  }
}
type ThreadDOM = {
  content: HTMLElement;
  conversation: HTMLElement;
  transcript: HTMLElement;
  area: HTMLElement;
  form: HTMLElement;
  input: HTMLTextAreaElement;
  listVersion: number;
  writable?: boolean;
  statusFit?: string;
  siteInputs?: unknown[];
  promptSize?: string;
};
const threadDOMs = new Map<string, ThreadDOM>();
function threadDOM(v: View) {
  const id = v.info.threadId;
  let dom = threadDOMs.get(id);
  if (!dom) {
    const content = el('div', null, 'content');
    const conversation = el('div', null, 'conversation');
    const transcript = el('div', null, 'transcript');
    transcript.dataset.thread = id;
    transcript.onscroll = () => {
      if (!transcript.isConnected) return;
      scrolls.set(id, transcript.scrollTop);
      if (transcript.scrollTop < 40 && v.info.hasMore && !pages.has(id))
        void run(() => earlier(v));
    };
    const area = el('div', null, 'composer-area');
    const form = el('div', null, 'composer');
    const input = el('textarea');
    input.rows = 1;
    input.dataset.focusId = 'prompt';
    input.dataset.thread = id;
    input.setAttribute('aria-label', 'Message');
    input.addEventListener('compositionstart', () => (composing = true));
    const finishComposition = () => {
      if (!composing) return;
      composing = false;
      schedule();
    };
    input.addEventListener('compositionend', finishComposition);
    input.onblur = finishComposition;
    // Typing repaints nothing but the slash-command menu when it changes.
    input.oninput = () => {
      const before = slashMenu(drafts.get(id) || '');
      drafts.set(id, input.value);
      growPrompt(input);
      if (slashMenu(input.value) !== before) schedule();
    };
    input.onkeydown = (e) => {
      if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
        e.preventDefault();
        void run(() => submit(e.ctrlKey || e.metaKey ? 'replace' : 'auto'));
      } else if (e.key === 'Escape') {
        e.preventDefault();
        void run(() => call('turn/interrupt'));
      }
    };
    input.onpaste = (e) => {
      if (e.clipboardData?.files.length) {
        e.preventDefault();
        void run(() => addImages(e.clipboardData!.files));
      }
    };
    form.ondragover = (e) => e.preventDefault();
    form.ondrop = (e) => {
      e.preventDefault();
      if (e.dataTransfer) void run(() => addImages(e.dataTransfer!.files));
    };
    conversation.append(transcript, area);
    content.append(conversation);
    dom = {
      content,
      conversation,
      transcript,
      area,
      form,
      input,
      listVersion: -1,
    };
    threadDOMs.set(id, dom);
  }
  return dom;
}

function composer(v: View, instances: Data[], above: boolean) {
  const { area, form, input } = threadDOM(v);
  const id = v.info.threadId;
  const parts: HTMLElement[] = [];
  const panes = instances.filter((i) => i.site === 'pane');
  if (above && panes.length) parts.push(paneRegion(v, panes, true));
  for (const i of instances.filter(
    (i) => i.site === 'band' && i.id !== 'atto/queue',
  ))
    parts.push(
      region(id + '\0band\0' + i.id, [i.rev], () => {
        const b = el('div', null, 'band');
        b.append(uiTree(v, i));
        return b;
      }),
    );
  const queueBand = instances.find(
    (i) => i.site === 'band' && i.id === 'atto/queue',
  );
  parts.push(
    region(
      id + '\0queue',
      [queueBand?.rev, v.info.pending, writable(v)],
      () => {
        const pending = el('div', null, 'queue');
        if (queueBand?.tree) pending.append(uiTree(v, queueBand));
        if (v.info.pending?.items?.length)
          pending.append(el('div', 'Next up', 'queue-title'));
        const p = v.info.pending;
        for (const i of p?.items || []) {
          const row = el('div', null, 'pending-row');
          row.append(
            el('small', i.kind === 'steer' ? 'Steer' : 'Queued'),
            el(
              'span',
              queueBand?.tree
                ? ''
                : plainMarkdown(
                    i.text || i.input || i.preview || 'Pending message',
                  ),
            ),
          );
          row.append(
            button(
              'Edit',
              () =>
                void run(async () => {
                  const r = await call('turn/unsteer', { inputId: i.id });
                  drafts.set(
                    active,
                    (r.text || '') +
                      (drafts.get(active) ? '\n' + drafts.get(active) : ''),
                  );
                  recoverImages(active, r.images || []);
                  schedule();
                }),
              !writable(v),
            ),
          );
          pending.append(row);
        }
        return pending;
      },
    ),
  );
  const typed = drafts.get(id) || '';
  if (slashMenu(typed))
    parts.push(
      region(id + '\0commands', [slashMenu(typed), commands.get(id)], () => {
        const menu = el('div', null, 'commands');
        for (const c of (commands.get(active) || [])
          .filter((c) => c.name.startsWith(typed.slice(1)))
          .slice(0, 10))
          menu.append(
            button('/' + c.name + ' — ' + c.description, () => {
              drafts.set(active, '/' + c.name + ' ');
              schedule();
            }),
          );
        return menu;
      }),
    );
  if (context) {
    const info = context;
    parts.push(
      region(id + '\0context', [info], () => {
        const details = el('details', null, 'context-card');
        details.open = true;
        details.append(
          el(
            'summary',
            'Context · ' +
              (info.contextTokens || 0) +
              ' / ' +
              (info.contextWindow || '?') +
              ' tokens',
          ),
          info.tree
            ? uiTree(v, {
                site: 'pane',
                id: 'atto/context',
                rev: 0,
                tree: info.tree,
              })
            : code(JSON.stringify(info, null, 2)),
          button('Hide', () => {
            context = null;
            schedule();
          }),
        );
        return details;
      }),
    );
  }
  const formParts: HTMLElement[] = [];
  const ims = attachments.get(id) || [];
  if (ims.length)
    formParts.push(
      region(id + '\0attachments', [ims, ims.length], () => {
        const strip = el('div', null, 'attachments');
        ims.forEach((im, i) => {
          const group = el('div');
          const img = el('img');
          if (im.data) img.src = im.data;
          img.alt = im.name || 'Recovered image';
          group.append(
            img,
            button('×', () => {
              ims.splice(i, 1);
              schedule();
            }),
          );
          strip.append(group);
        });
        return strip;
      }),
    );
  if (input.value !== typed) {
    input.value = typed;
    growPrompt(input);
  }
  const placeholder =
    v.info.readOnly || v.info.offline
      ? 'Read only — resume an active session to write'
      : v.info.busy
        ? 'Steer the running turn…'
        : 'Message atto…';
  if (input.placeholder !== placeholder) input.placeholder = placeholder;
  if (input.disabled !== !writable(v)) input.disabled = !writable(v);
  formParts.push(input);
  const bar = region(
    id + '\0toolbar',
    [
      models,
      v.info.model,
      v.info.effort,
      v.info.efforts,
      v.info.busy,
      writable(v),
    ],
    () => {
      const bar = el('div', null, 'toolbar');
      const file = el('input');
      file.type = 'file';
      file.accept = 'image/png,image/jpeg,image/gif,image/webp';
      file.multiple = true;
      file.hidden = true;
      file.onchange = () => {
        if (file.files) void run(() => addImages(file.files!));
      };
      bar.append(
        file,
        button(
          '＋',
          () => file.click(),
          !writable(v) || !models.find((m) => m.id === v.info.model)?.images,
        ),
      );
      bar.querySelector('button')!.setAttribute('aria-label', 'Attach image');
      bar.querySelector('button')!.title = 'Attach image';
      const model = el('select');
      model.id = 'model';
      model.dataset.focusId = 'model';
      model.setAttribute('aria-label', 'Model');
      for (const m of models) {
        const o = el('option', m.name || m.id);
        o.value = m.id;
        o.disabled = !m.hasKey;
        model.append(o);
      }
      model.value = v.info.model;
      model.disabled = !writable(v);
      model.onchange = () =>
        void run(() => settings('thread/setModel', { model: model.value }));
      bar.append(picker(model, 'Model'));
      const effort = el('select');
      effort.id = 'effort';
      effort.dataset.focusId = 'effort';
      effort.setAttribute('aria-label', 'Reasoning effort');
      for (const x of models.find((m) => m.id === v.info.model)?.efforts ||
        v.info.efforts ||
        []) {
        const o = el('option', x);
        o.value = x;
        effort.append(o);
      }
      effort.value = v.info.effort;
      effort.disabled = !writable(v);
      effort.onchange = () =>
        void run(() => settings('thread/setEffort', { effort: effort.value }));
      const effortPicker = picker(effort, 'Reasoning effort');
      bar.append(effortPicker);
      if (!effort.children.length) effortPicker.hidden = true;
      if (v.info.busy) {
        bar.append(
          button('Steer', () => void run(() => submit()), !writable(v)),
          button('Queue', () => void run(() => submit('queue')), !writable(v)),
        );
        const more = el('details', null, 'composer-more');
        more.append(el('summary', '⋯'));
        more.append(
          button(
            'Send now',
            () => void run(() => submit('replace')),
            !writable(v),
          ),
          button(
            'Background',
            () => void run(() => call('turn/background')),
            !writable(v),
          ),
        );
        bar.append(more);
      }
      const send = button(
        v.info.busy ? '■' : '↑',
        () => void run(() => (v.info.busy ? call('turn/interrupt') : submit())),
        !writable(v),
      );
      send.className = 'send';
      send.setAttribute('aria-label', v.info.busy ? 'Stop' : 'Send');
      send.title = v.info.busy ? 'Stop turn' : 'Send message';
      bar.append(send);
      return bar;
    },
  );
  formParts.push(bar);
  syncChildren(form, formParts);
  // A worker of an older build (busy when the session was opened, or too old
  // to be replaced): the snapshot says so; a current worker's clears it.
  if (v.info.runtimeOutdated)
    parts.push(
      region(id + '\0runtime', [v.info.runtimeVersion], () => {
        const note = el(
          'div',
          'This session runs an older atto (' +
            (v.info.runtimeVersion || 'unknown version') +
            '). It will update the next time it is idle and reopened.',
          'runtime-notice',
        );
        note.setAttribute('role', 'note');
        return note;
      }),
    );
  parts.push(form);
  const status = region(
    id + '\0status',
    [
      siteVersions(instances.filter((i) => i.site === 'status')),
      v.info.contextTokens,
      v.info.usage?.cost,
      online,
      innerWidth,
    ],
    () => {
      const status = el('footer', null, 'status');
      const statuses = instances
        .filter((i) => i.site === 'status')
        .sort(
          (a, b) => (b.options?.priority || 0) - (a.options?.priority || 0),
        );
      for (const i of statuses) {
        const n = uiTree(v, i);
        n.style.display = '';
        n.dataset.priority = String(i.options?.priority || 0);
        n.dataset.registration = String(instances.indexOf(i));
        n.style.order = String(instances.indexOf(i));
        if (i.options?.align === 'end') n.style.marginLeft = 'auto';
        status.append(n);
      }
      status.append(
        button(
          (v.info.contextTokens || 0) +
            ' tokens · $' +
            (v.info.usage?.cost || 0).toFixed(4),
          () =>
            void run(async () => {
              context = await call('thread/context');
              schedule();
            }),
          !online,
        ),
      );
      return status;
    },
  );
  parts.push(status);
  syncChildren(area, parts);
  return area;
}
function paneRegion(v: View, panes: Data[], above: boolean) {
  const selected =
    panes.find((i) => i.id === paneTab.get(v.info.threadId)) || panes[0];
  paneTab.set(v.info.threadId, selected.id);
  return region(
    v.info.threadId + '\0pane',
    [
      siteVersions(panes),
      paneTab.get(v.info.threadId),
      above,
      innerWidth,
      innerHeight,
      writable(v),
    ],
    () => pane(v, panes, above),
  );
}
function promptDialog(v: View) {
  const p = v.info.prompt;
  if (!p || p.uiId) return null;
  const card = el('section', null, 'approval-card');
  card.setAttribute('role', 'dialog');
  card.setAttribute('aria-modal', 'true');
  card.setAttribute('aria-label', p.title || 'Question');
  card.append(el('h2', p.title || 'Question'));
  if (p.subtitle) card.append(el('p', p.subtitle));
  if (p.kind === 'input') {
    const input = el('input');
    input.type = 'text';
    input.value =
      siteLocal(active, 'prompt', p.id).drafts.get('value') ?? p.text ?? '';
    input.dataset.focusId = 'question';
    input.placeholder = p.placeholder || '';
    input.oninput = () =>
      siteLocal(active, 'prompt', p.id).drafts.set('value', input.value);
    card.append(
      input,
      button(
        'Submit',
        () =>
          void run(() =>
            call('prompt/answer', { id: p.id, text: input.value }),
          ),
        !writable(v),
      ),
    );
  } else if (p.kind === 'multiSelect') {
    const l = siteLocal(active, 'prompt', p.id);
    const selected = new Set<number>(
      (l.drafts.get('selected') || '').split(',').filter(Boolean).map(Number),
    );
    (p.options || []).forEach((o: Data, index: number) => {
      const label = el('label', o.label);
      const input = el('input');
      input.type = 'checkbox';
      input.checked = selected.has(index);
      input.onchange = () => {
        if (input.checked) selected.add(index);
        else selected.delete(index);
        l.drafts.set('selected', [...selected].join(','));
      };
      label.prepend(input);
      card.append(label);
    });
    card.append(
      button(
        'Submit',
        () =>
          void run(() =>
            call('prompt/answer', { id: p.id, indexes: [...selected] }),
          ),
        !writable(v),
      ),
    );
  } else
    for (const [index, o] of (p.options || []).entries())
      card.append(
        button(
          o.label + (o.description ? ' — ' + o.description : ''),
          () => void run(() => call('prompt/answer', { id: p.id, index })),
          !writable(v),
        ),
      );
  card.append(
    button(
      'Cancel',
      () => void run(() => call('prompt/answer', { id: p.id, cancel: true })),
      !writable(v),
    ),
  );
  return card;
}
const layout = el('div', null, 'layout');
const workspace = el('main', null, 'workspace');
root.replaceChildren(layout);
layout.append(workspace);
function paint() {
  // Bounded timing evidence is available to the local CDP verification tool.
  performance.mark('atto-paint-start');
  const previousScroll = document.getElementById('transcript');
  const switching = previousScroll?.dataset.thread !== active;
  if (switching && previousScroll?.dataset.thread) {
    scrolls.set(previousScroll.dataset.thread, previousScroll.scrollTop);
    const previousPane = root.querySelector<HTMLElement>(
      '.pane-dock,.pane-above',
    );
    if (previousPane?.dataset.thread)
      paneStates.set(previousPane.dataset.thread, {
        ...paneStates.get(previousPane.dataset.thread),
        scroll: previousPane.scrollTop,
        ...(previousPane.style.width?.endsWith('px')
          ? { width: parseFloat(previousPane.style.width) }
          : {}),
      });
  }
  const focus = document.activeElement as HTMLInputElement;
  const focusId = focus?.dataset.focusId,
    focusKey = focus?.dataset.key,
    focusScope = focus?.closest('[data-scope]')?.getAttribute('data-scope');
  const start = focus?.selectionStart,
    end = focus?.selectionEnd;
  for (const [id, dom] of threadDOMs)
    if (!views.has(id)) {
      dom.content.remove();
      threadDOMs.delete(id);
      for (const key of regions.keys())
        if (key.startsWith(id + '\0')) regions.delete(key);
      for (const key of itemDOM.keys())
        if (key.startsWith(id + '\0')) itemDOM.delete(key);
      for (const key of treeDOM.keys())
        if (key.startsWith(id + '\0')) treeDOM.delete(key);
    }
  const sidebarNode = region(
    'sidebar',
    [
      inventory,
      active,
      filter,
      sidebar,
      online,
      rowMenu,
      Math.floor(Date.now() / 60000),
      foldVersion,
      [...views]
        .map(([id, v]) => id + ':' + v.inventoryVersion + ':' + v.info.name)
        .join('|'),
    ],
    inventoryUI,
  );
  const tabKey = JSON.stringify(
    [...views].map(([id, v]) => [
      id,
      v.info.name,
      v.info.busy,
      !!v.info.prompt,
      v.info.name ? 0 : v.tabVersion,
    ]),
  );
  const top = region(
    'topbar',
    [active, online, tabKey, inventory, writable(current())],
    () => {
      const top = el('div', null, 'topbar');
      top.append(
        button('☰', () => {
          sidebar = !sidebar;
          schedule();
        }),
      );
      top.firstElementChild!.classList.add('mobile-menu');
      const tabs = region('tabs', [active, online, tabKey, inventory], () => {
        const tabs = el('div', null, 'tabs');
        for (const [id, v] of views) {
          const tab = button(
            (v.info.busy ? '◌ ' : v.info.prompt ? '! ' : '') +
              plainMarkdown(
                sessionTitle(
                  inventory.find((r) => r.threadId === id),
                  v,
                ),
              ),
            () => {
              active = id;
              context = null;
              schedule();
            },
          );
          tab.className = 'tab' + (id === active ? ' active' : '');
          const group = el(
            'div',
            null,
            'tab-group' + (id === active ? ' active' : ''),
          );
          const close = button('×', () => void run(() => detach(id)), !online);
          close.setAttribute('aria-label', 'Close session tab');
          group.append(tab, close);
          tabs.append(group);
        }
        return tabs;
      });
      top.append(tabs);
      if (active)
        top.append(
          button(
            'Rename',
            () => {
              const name = prompt('Session name', current()?.info.name || '');
              if (name != null)
                void run(() => settings('thread/setName', { name }));
            },
            !writable(current()),
          ),
        );
      return top;
    },
  );
  const workspaceParts = [top];
  const layoutParts = [sidebarNode];
  const v = current();
  let newThreadInput: HTMLTextAreaElement | undefined;
  let stick: HTMLElement | undefined;
  let restoreThreadScroll: number | undefined;
  if (!v) {
    workspaceParts.push(
      region(
        'empty',
        [online, restoring, inventory, Math.floor(Date.now() / 60000)],
        () => {
          const empty = el('div', null, 'empty');
          if (restoring) {
            empty.append(el('p', 'Reopening your sessions…', 'muted'));
            return empty;
          }
          const recents = recentRows();
          empty.append(
            el('div', '✳', 'empty-mark'),
            el(
              'h2',
              recents.length
                ? 'Pick up where you left off'
                : 'What would you like to build?',
            ),
          );
          if (recents.length) {
            const list = el('div', null, 'recents');
            list.setAttribute('aria-label', 'Recent sessions');
            for (const row of recents) {
              const r = el('div', null, 'inventory-row');
              r.append(sessionButton(row));
              list.append(r);
            }
            empty.append(list);
          } else
            empty.append(
              el(
                'p',
                'A little help for your next big idea. Start a session to begin.',
              ),
            );
          const start = button(
            'New session',
            () => void run(() => open()),
            !online,
          );
          start.className = recents.length ? 'new-secondary' : 'new-primary';
          empty.append(start);
          return empty;
        },
      ),
    );
  } else {
    const dom = threadDOM(v);
    syncChildren(workspace, [top, dom.content]);
    // Only the attached view has document IDs. Inactive views keep their DOM,
    // editor and scroll offsets, detached from the workspace until selected.
    for (const [id, d] of threadDOMs) {
      const transcriptId = id === active ? 'transcript' : '';
      const promptId = id === active ? 'prompt' : '';
      if (d.transcript.id !== transcriptId) d.transcript.id = transcriptId;
      if (d.input.id !== promptId) d.input.id = promptId;
    }
    const sc = dom.transcript;
    if (switching && dom.listVersion >= 0) {
      restoreThreadScroll = scrolls.get(active);
      if (restoreThreadScroll != null) sc.scrollTop = restoreThreadScroll;
    }
    const firstPaint = dom.listVersion < 0;
    if (firstPaint) newThreadInput = dom.input;
    const bottom = sc.scrollHeight - sc.scrollTop - sc.clientHeight < 70;
    const structural = dom.listVersion !== v.listVersion;
    const oldHeight = sc.scrollHeight;
    const oldTop = sc.scrollTop;
    const oldFirst = sc.querySelector<HTMLElement>('[data-item]')?.dataset.item;
    for (const id of v.dirtyItems) {
      const item = v.items.find((i) => i.id === id);
      if (!item) continue;
      const old = itemDOM.get(active + '\0' + id)?.node;
      const node = transcriptItem(v, item);
      v.dirtyItems.delete(id);
      v.deltaItems.delete(id);
      if (old && node !== old && old.parentNode === sc) old.replaceWith(node);
    }
    const transcriptInputs = [
      v.listVersion,
      v.info.hasMore,
      pages.has(active),
      online,
    ];
    const listKey = active + '\0list';
    const listState = regions.get(listKey);
    if (
      !listState ||
      transcriptInputs.some((x, n) => x !== listState.inputs[n])
    ) {
      const children: HTMLElement[] = [];
      if (v.info.hasMore)
        children.push(
          region(active + '\0earlier', [pages.has(active), online], () =>
            button(
              pages.has(active)
                ? 'Loading earlier messages…'
                : 'Load earlier messages',
              () => void run(() => earlier(v)),
              pages.has(active) || !online,
            ),
          ),
        );
      for (const i of v.items) children.push(transcriptItem(v, i));
      if (
        !v.items.some((i) =>
          ['userMessage', 'agentMessage', 'commandExecution'].includes(i.type),
        )
      )
        children.push(
          region(active + '\0welcome', [], () => {
            const welcome = el('div', null, 'conversation-empty');
            welcome.append(
              el('div', '✳', 'empty-mark'),
              el('h2', 'Ready when you are'),
              el(
                'p',
                'Ask a question, explore your project, or build something new.',
              ),
            );
            return welcome;
          }),
        );
      children.push(transcriptTail(v));
      syncChildren(sc, children);
      regions.set(listKey, { node: sc, inputs: transcriptInputs });
      dom.listVersion = v.listVersion;
      const loaded = new Set(v.items.map((i) => active + '\0' + i.id));
      for (const key of itemDOM.keys())
        if (key.startsWith(active + '\0') && !loaded.has(key))
          itemDOM.delete(key);
    }
    transcriptTail(v);
    if (dom.writable !== writable(v)) {
      for (const node of sc.querySelectorAll<HTMLButtonElement>(
        '.message-edit',
      ))
        node.disabled = !writable(v);
      dom.writable = writable(v);
    }
    v.dirtyItems.clear();
    v.deltaItems.clear();
    const instances = (v.info.ui?.instances || []).filter(
      (i: Data) =>
        i.site !== 'toast' ||
        !i.options?.expiresAt ||
        i.options.expiresAt > Date.now(),
    );
    const siteInputs = [
      v.listVersion,
      v.uiVersion,
      siteVersions(instances.filter((i: Data) => i.site === 'toast')),
      context,
    ];
    if (
      !dom.siteInputs ||
      siteInputs.some((x, n) => x !== dom.siteInputs![n])
    ) {
      dom.siteInputs = siteInputs;
      const scopes = new Set<string>(
        instances.map((i: Data) => active + '\0' + i.site + '\0' + i.id),
      );
      for (const item of v.items) {
        if (item.type === 'uiBlock')
          scopes.add(active + '\0transcript\0' + item.uiId);
        if (item.uiDisplay?.tree)
          scopes.add(active + '\0' + itemSite(item) + '\0' + item.id);
      }
      if (context) scopes.add(active + '\0pane\0atto/context');
      for (const key of treeDOM.keys())
        if (key.startsWith(active + '\0') && !scopes.has(key))
          treeDOM.delete(key);
      const bands = new Set(
        instances
          .filter((i: Data) => i.site === 'band')
          .map((i: Data) => active + '\0band\0' + i.id),
      );
      for (const key of regions.keys())
        if (key.startsWith(active + '\0band\0') && !bands.has(key))
          regions.delete(key);
    }
    for (const [scope, cached] of treeDOM)
      if (scope.startsWith(active + '\0')) {
        const item = cached.item;
        const enabled =
          writable(v) &&
          (!item ||
            !!item.uiDisplay?.actionsEnabled ||
            (item.type === 'uiBlock' && !!item.actionsEnabled));
        if (cached.context.enabled !== enabled) {
          cached.context.enabled = enabled;
          syncEnabled(cached.node, enabled);
        }
      }
    const panes = instances.filter((i: Data) => i.site === 'pane');
    const picked =
      panes.find((i: Data) => i.id === paneTab.get(active)) || panes[0];
    const side =
      contentWidth() >= 120 && picked?.options?.placement !== 'abovePrompt';
    composer(v, instances, !side);
    syncChildren(dom.content, [
      dom.conversation,
      ...(side && panes.length ? [paneRegion(v, panes, false)] : []),
    ]);
    workspaceParts.push(dom.content);
    layoutParts.push(
      region(
        active + '\0toasts',
        [siteVersions(instances.filter((i: Data) => i.site === 'toast'))],
        () => {
          const toasts = el('div', null, 'toasts');
          for (const i of instances.filter((i: Data) => i.site === 'toast')) {
            const t = el('div', null, 'toast');
            t.setAttribute('role', 'status');
            t.append(uiTree(v, i));
            toasts.append(t);
          }
          return toasts;
        },
      ),
    );
    const dialogs = instances.filter((i: Data) => i.site === 'dialog');
    const dialogNode = region(
      active + '\0dialogs',
      [siteVersions(dialogs), v.info.prompt, writable(v)],
      () => {
        const dialogs = instances.filter((i: Data) => i.site === 'dialog');
        if (dialogs.length) {
          const i = dialogs[0];
          const card = el('section', null, 'approval-card');
          card.setAttribute('role', 'dialog');
          card.setAttribute('aria-modal', 'true');
          card.setAttribute('aria-label', i.options?.title || 'Question');
          card.append(
            el('h2', i.options?.title || 'Question'),
            uiTree(v, i),
            button(
              'Cancel',
              () =>
                void run(() =>
                  call('ui/event', {
                    site: 'dialog',
                    id: i.id,
                    key: '$site',
                    type: 'close',
                    rev: i.rev,
                  }),
                ),
              !writable(v),
            ),
          );
          const backdrop = el('div', null, 'dialog-backdrop');
          backdrop.append(card);
          return backdrop;
        } else {
          const card = promptDialog(v);
          if (card) {
            const backdrop = el('div', null, 'dialog-backdrop');
            backdrop.append(card);
            return backdrop;
          }
        }
        return el('div');
      },
    );
    if (dialogNode.className === 'dialog-backdrop')
      layoutParts.push(dialogNode);
    // Paging uses the height difference; live text follows only if already near
    // the bottom. Never jump a reader inspecting older messages to the tail.
    const prepended = structural && oldFirst && v.items[0]?.id !== oldFirst;
    if (prepended) sc.scrollTop = oldTop + sc.scrollHeight - oldHeight;
    else if (bottom || firstPaint) {
      sc.scrollTop = sc.scrollHeight;
      stick = sc;
    }
    scrolls.set(active, sc.scrollTop);
    const status = dom.area.querySelector<HTMLElement>('.status');
    const fitKey = JSON.stringify([
      siteVersions(instances.filter((i: Data) => i.site === 'status')),
      v.info.contextTokens,
      v.info.usage?.cost,
      innerWidth,
      status?.clientWidth,
    ]);
    if (status && dom.statusFit !== fitKey) {
      dom.statusFit = fitKey;
      const rows = Array.from(
        status.querySelectorAll<HTMLElement>('.ui-tree'),
      ).sort(
        (a, b) =>
          Number(a.dataset.priority) - Number(b.dataset.priority) ||
          Number(b.dataset.registration) - Number(a.dataset.registration),
      );
      for (const row of rows) row.style.display = '';
      while (status.scrollWidth > status.clientWidth && rows.length > 1)
        rows.shift()!.style.display = 'none';
    }
  }
  layoutParts.push(workspace);
  if (notice)
    layoutParts.push(
      region('notice', [notice], () => {
        const toast = el('div', notice, 'toasts toast');
        toast.setAttribute('role', 'alert');
        return toast;
      }),
    );
  if (localModal)
    layoutParts.push(
      region('modal', [localModal], () => {
        const backdrop = el('div', null, 'dialog-backdrop');
        backdrop.append(localModal!);
        return backdrop;
      }),
    );
  syncChildren(workspace, workspaceParts);
  syncChildren(layout, layoutParts);
  if (restored) persistTabs();
  if (v) {
    const dom = threadDOM(v);
    const size = innerWidth + 'x' + innerHeight + ':' + dom.input.clientWidth;
    // Measure the editor after attachment: phone field metrics differ from a
    // detached textarea. Subsequent growth happens on input/value/resize only.
    if (dom.promptSize !== size) {
      dom.promptSize = size;
      growPrompt(dom.input);
    }
  }
  if (v && switching) {
    if (restoreThreadScroll != null)
      threadDOM(v).transcript.scrollTop = restoreThreadScroll;
    const pane = root.querySelector<HTMLElement>('.pane-dock,.pane-above');
    if (pane?.dataset.thread === active)
      pane.scrollTop = paneStates.get(active)?.scroll || 0;
  }
  if (stick) {
    stick.scrollTop = stick.scrollHeight;
    scrolls.set(active, stick.scrollTop);
  }
  const controls = Array.from(
    root.querySelectorAll<HTMLElement>('[data-focus-id],[data-key]'),
  );
  const restore =
    focus?.isConnected && root.contains(focus)
      ? focus
      : focusId || focusKey
        ? controls.find((n) =>
            focusId
              ? n.dataset.focusId === focusId
              : n.dataset.key === focusKey &&
                n.closest('[data-scope]')?.getAttribute('data-scope') ===
                  focusScope,
          )
        : newThreadInput;
  const dialog = root.querySelector<HTMLElement>('[role=dialog]');
  if (dialog && (!restore || !dialog.contains(restore))) {
    dialog.querySelector<HTMLElement>('input,button,select,summary')?.focus();
  } else if (restore && restore !== document.activeElement) {
    restore.focus({ preventScroll: true });
    if (
      start != null &&
      end != null &&
      (restore instanceof HTMLTextAreaElement ||
        restore instanceof HTMLInputElement) &&
      restore.type !== 'checkbox' &&
      (restore.dataset.focusId !== 'prompt' || focus?.dataset.thread === active)
    )
      try {
        restore.setSelectionRange(start, end);
      } catch {}
  } else if (focusHint && !(drafts.get(active) || '')) {
    const scope = Array.from(
      root.querySelectorAll<HTMLElement>('[data-scope]'),
    ).find((e) => e.dataset.scope === focusHint);
    scope
      ?.querySelector<HTMLElement>(
        '[data-autofocus],button,input,select,summary',
      )
      ?.focus();
  }
  focusHint = '';
  performance.measure('atto-paint', 'atto-paint-start');
  performance.clearMarks('atto-paint-start');
  // Avoid accumulating timing entries during day-long sessions.
  if (performance.getEntriesByName('atto-paint').length > 600)
    performance.clearMeasures('atto-paint');
}
function contentWidth() {
  return Math.floor((innerWidth - (innerWidth > 700 ? 260 : 0)) / 8);
}
// Dialog focus stays inside the current question. Escape cancels a question or
// returns from a pane; it never accidentally interrupts behind a dialog.
document.addEventListener('keydown', (e) => {
  const dialog = root.querySelector<HTMLElement>('[role=dialog]');
  if (dialog) {
    if (e.key === 'Escape') {
      e.preventDefault();
      if (localModal) {
        localModal = null;
        schedule();
      } else {
        const b = Array.from(
          dialog.querySelectorAll<HTMLButtonElement>('button'),
        ).find((b) => b.textContent === 'Cancel');
        b?.click();
      }
    }
    if (e.key === 'Tab') {
      const controls = Array.from(
        dialog.querySelectorAll<HTMLElement>(
          'button:not(:disabled),input:not(:disabled),select:not(:disabled),summary',
        ),
      );
      if (!controls.length) return;
      const n = controls.indexOf(document.activeElement as HTMLElement);
      if (e.shiftKey && n <= 0) {
        e.preventDefault();
        controls[controls.length - 1].focus();
      } else if (!e.shiftKey && n === controls.length - 1) {
        e.preventDefault();
        controls[0].focus();
      }
    }
    return;
  }
  if (
    e.key === 'Escape' &&
    current()?.info.busy &&
    !(e.target instanceof HTMLTextAreaElement) &&
    !e.defaultPrevented
  ) {
    e.preventDefault();
    void run(() => call('turn/interrupt'));
  }
});
setInterval(() => {
  if (online && !document.hidden) void run(refreshInventory);
}, 10000);
setInterval(() => {
  if (
    current()?.info.ui?.instances.some(
      (i: Data) => i.site === 'toast' && i.options?.expiresAt <= Date.now(),
    )
  )
    schedule();
}, 1000);
paint();
connect();
