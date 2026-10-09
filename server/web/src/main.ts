// New revision-3 client. Beautiful UI Chat/Thinking/Streaming Text/Tool Chips/
// Prompt Bar adaptations; Vercel Queue/Checkpoint/Context/Agent/Terminal are
// behavior references only. No React, remote scripts or frozen wire shapes.
import {
  Data,
  Tree,
  RPC,
  View,
  catalog,
  consumeToken,
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
const token = consumeToken(location, sessionStorage, (url) =>
  history.replaceState(null, '', url),
);
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
function schedule() {
  if (!frame)
    frame = requestAnimationFrame(() => {
      frame = 0;
      paint();
    });
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
  inventory = r.threads || [];
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
function connect() {
  clearTimeout(reconnectTimer);
  socket = new WebSocket(
    (location.protocol === 'https:' ? 'wss://' : 'ws://') +
      location.host +
      '/ws',
    ['atto.rpc.v3', ...(token ? ['atto.auth.' + token] : [])],
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
      'Cannot connect. Open the link printed by atto (including its token fragment), or check the listener.',
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
  const generation = v.generation;
  const sc = document.getElementById('transcript');
  const oldHeight = sc?.scrollHeight || 0,
    oldTop = sc?.scrollTop || 0;
  try {
    const page = await call(
      'thread/items',
      { before: v.info.before, limit: 100, offline: !!v.info.offline },
      id,
    );
    if (v.prepend(page, generation)) {
      paint();
      const target = document.getElementById('transcript');
      if (active === id && target) {
        target.scrollTop = oldTop + target.scrollHeight - oldHeight;
        scrolls.set(id, target.scrollTop);
      }
    }
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
function uiTree(v: View, i: Data, item?: Data) {
  const scope = v.info.threadId + '\0' + i.site + '\0' + i.id;
  const enabled =
    writable(v) &&
    (!item ||
      !!item.uiDisplay?.actionsEnabled ||
      (item.type === 'uiBlock' && !!item.actionsEnabled));
  if (item && i.tree && !validTree(i.tree, i.site, i.id))
    return nativeItem(v, item);
  const tree = render(i.tree, {
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
      ? (n) => nativeItem(v, { ...item, ...n.props.overrides }, false)
      : undefined,
    image: (r, img) => image(r, img, v),
  });
  tree.dataset.scope = scope;
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
      head.append(
        button(
          'Fork',
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
        ),
      );
    }
    head.querySelector('button')?.classList.add('fork');
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
    details.append(
      title,
      code(i.command || '', { language: i.shell ? 'shell' : 'bash' }),
      code(preview),
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
          schedule();
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
      markdown(i.text || ''),
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
    e.append(text);
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
  if (i.type === 'uiBlock' && !i.tree) return el('span');
  const key = v.info.threadId + '\0' + i.id;
  if (!i.uiDisplay?.tree || originals.has(key)) {
    const n = nativeItem(v, i);
    if (i.uiDisplay?.tree)
      n.append(
        button('Show extension drawing', () => {
          originals.delete(key);
          schedule();
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
      schedule();
    }),
  );
  return e;
}
// Reuse unchanged transcript DOM across stream/composer/status paints. This
// keeps completed blocks, selections and disclosures still instead of flashing
// the whole conversation on each token. Cache only the currently loaded pages.
const itemDOM = new Map<string, { fingerprint: string; node: HTMLElement }>();
function transcriptItem(v: View, i: Data) {
  const key = v.info.threadId + '\0' + i.id;
  const fingerprint = JSON.stringify([
    i,
    writable(v),
    originals.has(key),
    [...siteLocal(v.info.threadId, 'native', i.id).open],
    [...siteLocal(v.info.threadId, itemSite(i), i.id).open],
  ]);
  const old = itemDOM.get(key);
  if (old?.fingerprint === fingerprint) return old.node;
  const node = drawTranscriptItem(v, i);
  itemDOM.set(key, { fingerprint, node });
  return node;
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
      preview:
        v.items.find((i) => i.type === 'userMessage')?.text || row.preview,
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
    const b = button('', () => void run(() => open(row)), !online);
    b.className = 'session-row';
    const title = plainMarkdown(
      row.name || row.agent?.name || row.preview || 'New session',
    );
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
    case 'tree':
    case 'fork':
      await treeDialog(v, name === 'fork');
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
async function treeDialog(v: View, fork = false) {
  const r = await call(
    'thread/tree',
    { offline: !!v.info.offline },
    v.info.threadId,
  );
  const body = el('div');
  const entries = new Map<string, Data>(
    (r.entries || []).map((e: Data) => [e.id, e]),
  );
  for (const entry of r.entries || []) {
    const row = el('div', null, 'pending-row');
    let parent = entry.parentId,
      depth = 0;
    const seen = new Set<string>();
    while (parent && entries.has(parent) && !seen.has(parent) && depth < 12) {
      seen.add(parent);
      depth++;
      parent = entries.get(parent)!.parentId;
    }
    row.style.paddingLeft = depth + 'ch';
    row.append(
      el(
        'span',
        (
          entry.label ||
          entry.message?.content?.find((b: Data) => b.type === 'text')?.text ||
          entry.summary ||
          entry.notes ||
          entry.title ||
          entry.type ||
          'Checkpoint'
        ).slice(0, 160) +
          ' · ' +
          entry.id,
      ),
      button(
        'Fork',
        () =>
          void run(async () => {
            const s = await call(
              'thread/fork',
              { entryId: entry.id },
              v.info.threadId,
            );
            localModal = null;
            await open({ threadId: s.threadId });
            if (s.input) drafts.set(active, s.input);
            recoverImages(active, s.images || []);
            schedule();
          }),
        !writable(v),
      ),
    );
    if (!fork)
      row.append(
        button(
          'Go here',
          () =>
            void run(async () => {
              if (!confirm('Move to this branch checkpoint?')) return;
              await call(
                'thread/navigate',
                { entryId: entry.id, summary: { mode: 'none' } },
                v.info.threadId,
              );
              localModal = null;
              schedule();
            }),
          !writable(v),
        ),
      );
    body.append(row);
  }
  showModal('Checkpoints', body);
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
function composer(v: View, instances: Data[], above: boolean) {
  const area = el('div', null, 'composer-area');
  const panes = instances.filter((i) => i.site === 'pane');
  if (above && panes.length) area.append(pane(v, panes, true));
  for (const i of instances.filter(
    (i) => i.site === 'band' && i.id !== 'atto/queue',
  )) {
    const b = el('div', null, 'band');
    b.append(uiTree(v, i));
    area.append(b);
  }
  const queueBand = instances.find(
    (i) => i.site === 'band' && i.id === 'atto/queue',
  );
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
          : plainMarkdown(i.text || i.input || i.preview || 'Pending message'),
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
  area.append(pending);
  const typed = drafts.get(active) || '';
  if (/^\/[^\s]*$/.test(typed)) {
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
    area.append(menu);
  }
  if (context) {
    const details = el('details', null, 'context-card');
    details.open = true;
    details.append(
      el(
        'summary',
        'Context · ' +
          (context.contextTokens || 0) +
          ' / ' +
          (context.contextWindow || '?') +
          ' tokens',
      ),
      context.tree
        ? uiTree(v, {
            site: 'pane',
            id: 'atto/context',
            rev: 0,
            tree: context.tree,
          })
        : code(JSON.stringify(context, null, 2)),
      button('Hide', () => {
        context = null;
        schedule();
      }),
    );
    area.append(details);
  }
  if (v.info.busy)
    area.append(
      el(
        'div',
        typeof v.info.activity === 'string'
          ? v.info.activity
          : (v.info.activity?.phase || 'Thinking') + '…',
        'shimmer',
      ),
    );
  const form = el('div', null, 'composer');
  const ims = attachments.get(active) || [];
  if (ims.length) {
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
    form.append(strip);
  }
  const input = el('textarea');
  input.id = 'prompt';
  input.dataset.focusId = 'prompt';
  input.value = typed;
  input.placeholder =
    v.info.readOnly || v.info.offline
      ? 'Read only — resume an active session to write'
      : v.info.busy
        ? 'Steer the running turn…'
        : 'Message atto…';
  input.setAttribute('aria-label', 'Message');
  input.disabled = !writable(v);
  input.oninput = () => {
    drafts.set(active, input.value);
    schedule();
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
  form.append(input);
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
  bar.append(model);
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
  bar.append(effort);
  if (!effort.children.length) effort.hidden = true;
  if (v.info.busy) {
    bar.append(
      button('Steer', () => void run(() => submit()), !writable(v)),
      button('Queue', () => void run(() => submit('queue')), !writable(v)),
    );
    const more = el('details', null, 'composer-more');
    more.append(el('summary', '⋯'));
    more.append(
      button('Send now', () => void run(() => submit('replace')), !writable(v)),
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
  form.append(bar);
  area.append(form);
  const status = el('footer', null, 'status');
  const statuses = instances
    .filter((i) => i.site === 'status')
    .sort((a, b) => (b.options?.priority || 0) - (a.options?.priority || 0));
  for (const i of statuses) {
    const n = uiTree(v, i);
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
  area.append(status);
  return area;
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
function paint() {
  const loaded = new Set(
    [...views.values()].flatMap((v) =>
      v.items.map((i) => v.info.threadId + '\0' + i.id),
    ),
  );
  for (const key of itemDOM.keys()) if (!loaded.has(key)) itemDOM.delete(key);
  const oldPane = root.querySelector<HTMLElement>('.pane-dock,.pane-above');
  if (oldPane?.dataset.thread) {
    const id = oldPane.dataset.thread;
    const state = paneStates.get(id) || { scroll: 0 };
    state.scroll = oldPane.scrollTop;
    if (
      oldPane.classList.contains('pane-dock') &&
      oldPane.style.width.endsWith('px')
    )
      state.width = parseFloat(oldPane.style.width);
    paneStates.set(id, state);
  }
  const oldScroll = document.getElementById('transcript');
  const oldId = oldScroll?.dataset.thread;
  const oldBottom = oldScroll
    ? oldScroll.scrollHeight - oldScroll.scrollTop - oldScroll.clientHeight < 70
    : true;
  if (oldScroll && oldId) scrolls.set(oldId, oldScroll.scrollTop);
  const focus = document.activeElement as HTMLInputElement;
  const focusId = focus?.dataset.focusId,
    focusKey = focus?.dataset.key,
    focusScope = focus?.closest('[data-scope]')?.getAttribute('data-scope');
  const start = focus?.selectionStart,
    end = focus?.selectionEnd;
  const layout = el('div', null, 'layout');
  layout.append(inventoryUI());
  const workspace = el('main', null, 'workspace');
  const top = el('div', null, 'topbar');
  top.append(
    button('☰', () => {
      sidebar = !sidebar;
      schedule();
    }),
  );
  top.firstElementChild!.classList.add('mobile-menu');
  const tabs = el('div', null, 'tabs');
  for (const [id, v] of views) {
    const tab = button(
      (v.info.busy ? '◌ ' : v.info.prompt ? '! ' : '') +
        plainMarkdown(
          v.info.name ||
            v.items.find((i) => i.type === 'userMessage')?.text ||
            'New session',
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
  top.append(tabs);
  if (active)
    top.append(
      button('Tree', () => void run(() => treeDialog(current()!))),
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
  workspace.append(top);
  const v = current();
  if (!v) {
    const empty = el('div', null, 'empty');
    empty.append(
      el('div', '✳', 'empty-mark'),
      el('h2', 'What would you like to build?'),
      el(
        'p',
        'A little help for your next big idea. Start a session, or pick up where you left off.',
      ),
      button('New session', () => void run(() => open()), !online),
    );
    workspace.append(empty);
  } else {
    const content = el('div', null, 'content');
    const conversation = el('div', null, 'conversation');
    const sc = el('div', null, 'transcript');
    sc.id = 'transcript';
    sc.dataset.thread = active;
    const more = button(
      pages.has(active) ? 'Loading earlier messages…' : 'Load earlier messages',
      () => void run(() => earlier(v)),
      pages.has(active) || !online,
    );
    if (v.info.hasMore) sc.append(more);
    for (const i of v.items) sc.append(transcriptItem(v, i));
    if (
      !v.items.some((i) =>
        ['userMessage', 'agentMessage', 'commandExecution'].includes(i.type),
      )
    ) {
      const welcome = el('div', null, 'conversation-empty');
      welcome.append(
        el('div', '✳', 'empty-mark'),
        el('h2', 'Ready when you are'),
        el(
          'p',
          'Ask a question, explore your project, or build something new.',
        ),
      );
      sc.append(welcome);
    }
    sc.onscroll = () => {
      scrolls.set(v.info.threadId, sc.scrollTop);
      if (sc.scrollTop < 40 && v.info.hasMore && !pages.has(v.info.threadId))
        void run(() => earlier(v));
    };
    conversation.append(sc);
    const instances = (v.info.ui?.instances || []).filter(
      (i: Data) =>
        i.site !== 'toast' ||
        !i.options?.expiresAt ||
        i.options.expiresAt > Date.now(),
    );
    const panes = instances.filter((i: Data) => i.site === 'pane');
    const picked =
      panes.find((i: Data) => i.id === paneTab.get(active)) || panes[0];
    const side =
      columns() >= 120 &&
      contentWidth() >= 120 &&
      picked?.options?.placement !== 'abovePrompt';
    conversation.append(composer(v, instances, !side));
    content.append(conversation);
    if (side && panes.length) content.append(pane(v, panes, false));
    workspace.append(content);
    const toasts = el('div', null, 'toasts');
    for (const i of instances.filter((i: Data) => i.site === 'toast')) {
      const t = el('div', null, 'toast');
      t.setAttribute('role', 'status');
      t.append(uiTree(v, i));
      toasts.append(t);
    }
    layout.append(toasts);
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
      layout.append(backdrop);
    } else {
      const card = promptDialog(v);
      if (card) {
        const backdrop = el('div', null, 'dialog-backdrop');
        backdrop.append(card);
        layout.append(backdrop);
      }
    }
  }
  layout.append(workspace);
  if (notice) {
    const toast = el('div', notice, 'toasts toast');
    toast.setAttribute('role', 'alert');
    layout.append(toast);
  }
  if (localModal) {
    const backdrop = el('div', null, 'dialog-backdrop');
    backdrop.append(localModal);
    layout.append(backdrop);
  }
  root.replaceChildren(layout);
  const promptInput = document.getElementById('prompt') as HTMLTextAreaElement;
  if (promptInput) {
    promptInput.style.height = 'auto';
    promptInput.style.height =
      Math.min(Math.max(56, promptInput.scrollHeight), innerHeight * 0.3) +
      'px';
  }
  const newPane = root.querySelector<HTMLElement>('.pane-dock,.pane-above');
  if (newPane?.dataset.thread)
    newPane.scrollTop = paneStates.get(newPane.dataset.thread)?.scroll || 0;
  const status = root.querySelector<HTMLElement>('.status');
  if (status) {
    const rows = Array.from(
      status.querySelectorAll<HTMLElement>('.ui-tree'),
    ).sort(
      (a, b) =>
        Number(a.dataset.priority) - Number(b.dataset.priority) ||
        Number(b.dataset.registration) - Number(a.dataset.registration),
    );
    while (status.scrollWidth > status.clientWidth && rows.length > 1)
      rows.shift()!.remove();
  }
  const sc = document.getElementById('transcript');
  if (sc) {
    sc.scrollTop =
      oldId === active && oldBottom
        ? sc.scrollHeight
        : (scrolls.get(active) ?? sc.scrollHeight);
  }
  const controls = Array.from(
    root.querySelectorAll<HTMLElement>('[data-focus-id],[data-key]'),
  );
  const restore = controls.find((n) =>
    focusId
      ? n.dataset.focusId === focusId
      : n.dataset.key === focusKey &&
        n.closest('[data-scope]')?.getAttribute('data-scope') === focusScope,
  );
  const dialog = root.querySelector<HTMLElement>('[role=dialog]');
  if (dialog && (!restore || !dialog.contains(restore))) {
    dialog.querySelector<HTMLElement>('input,button,select,summary')?.focus();
  } else if (restore) {
    restore.focus({ preventScroll: true });
    if (
      start != null &&
      end != null &&
      (restore instanceof HTMLTextAreaElement ||
        restore instanceof HTMLInputElement) &&
      restore.type !== 'checkbox'
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
