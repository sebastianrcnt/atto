import { View, RPC, safeURL, consumeToken, clean, imageResource } from '../src/core';
import { render, Local, validTree, markdown } from '../src/elements';
declare const fixture: any;
declare function flushTimers(): void;
function assert(ok: any, why: string) {
  if (!ok) throw Error(why);
}
const v = new View();
v.snapshot({
  threadId: 't',
  eventId: 10,
  serverInstanceId: 's',
  items: [{ id: 'i', type: 'agentMessage', text: 'a' }],
  ui: { instances: [] },
  hasMore: true,
  before: 'i',
});
assert(
  !v.apply({
    method: 'item/delta',
    eventId: 10,
    params: { threadId: 't', itemId: 'i', delta: 'x' },
  }),
  'snapshot cursor',
);
assert(
  !v.apply({
    method: 'item/delta',
    eventId: 11,
    params: { threadId: 'other', itemId: 'i', delta: 'x' },
  }),
  'thread isolation',
);
assert(
  v.apply({
    method: 'item/delta',
    eventId: 12,
    params: { threadId: 't', itemId: 'i', delta: 'b' },
  }),
  'streaming',
);
assert(v.items[0].text === 'ab', 'delta appended');
v.info.prompt = { id: 'question' };
v.apply({
  method: 'thread/updated',
  eventId: 13,
  params: { threadId: 't', thread: { threadId: 't', model: 'local/m' } },
});
assert(
  v.info.prompt.id === 'question' && v.info.serverInstanceId === 's',
  'metadata update preserves question and cursor identity',
);
const generation = v.generation;
v.prepend(
  {
    items: [
      { id: 'old', text: 'old' },
      { id: 'i', text: 'stale' },
    ],
    hasMore: false,
    before: 'old',
  },
  generation,
);
assert(v.items[1].text === 'ab' && v.cursor === 13, 'paging never overwrites live/cursor');
for (const [method, rev] of [
  ['ui/open', 2],
  ['ui/render', 3],
  ['ui/close', 4],
  ['ui/render', 3],
] as const)
  v.apply({
    method,
    eventId: 12 + rev,
    params: {
      threadId: 't',
      site: 'pane',
      id: 'atto/p',
      rev,
      tree: { type: 'Text', props: { text: 'test' } },
      options: { title: 'Pane' },
    },
  });
assert(v.info.ui.instances.length === 0, 'close tombstone');
v.apply({
  method: 'ui/render',
  eventId: 25,
  params: {
    threadId: 't',
    site: 'pane',
    id: 'atto/p',
    rev: 3,
    tree: { type: 'Text', props: { text: 'late' } },
  },
});
assert(v.info.ui.instances.length === 0, 'rev tombstone rejects a later transport event');
v.apply({ method: 'thread/branchChanged', eventId: 30, params: { threadId: 't' } });
assert(!v.prepend({ items: [], hasMore: false }, generation), 'branch discards page');
v.snapshot({
  threadId: 't',
  eventId: 1,
  serverInstanceId: 'new',
  items: [],
  ui: { instances: [] },
});
assert(v.cursor === 1 && !v.reset, 'replacement instance');
for (const url of [
  'https://example.com',
  'http://localhost:123/x',
  'http://127.0.0.1/x',
  'http://[::1]/x',
])
  assert(safeURL(url), 'safe link ' + url);
for (const url of [
  'javascript:alert(1)',
  'data:text/html,x',
  'http://example.com',
  'https://u:p@example.com',
  'https://example.com/\n',
])
  assert(!safeURL(url), 'unsafe link ' + url);
assert(!clean('x\x1b]52;secret\x07\x1b[31m<svg>\x00').includes('secret'), 'escapes');
const store = new Map<string, string>();
let url = '';
const t = consumeToken(
  { hash: '#token=abc', pathname: '/', search: '' },
  { setItem: (k, v) => store.set(k, v), getItem: (k) => store.get(k) || null },
  (s) => (url = s),
);
assert(t === 'abc' && url === '/' && store.get('atto-token') === 'abc', 'fragment token');
assert(imageResource('t-i1-image-2')?.index === 2 && !imageResource('../x'), 'resource identity');
let sent: any[] = [];
const client = new RPC(
  (s) => sent.push(JSON.parse(s)),
  () => {},
);
let result: any = null;
client.call('ping').then((r) => (result = r));
client.receive(JSON.stringify({ id: sent[0].id, result: { ok: true } }));
let disconnected = false;
client.call('input/submit', { input: 'once' }).catch(() => (disconnected = true));
client.close();
assert(sent.length === 2 && client.pending.size === 0, 'never resend');
let actions: any[] = [];
const local = new Local();
const ctx: any = {
  site: 'pane',
  id: 'atto/test',
  rev: 7,
  enabled: true,
  local,
  action: (...a: any[]) => actions.push(a),
  image: (r: string, img: any) => {
    img.alt = 'resource:' + r;
  },
};
const node = render(fixture, ctx);
document.body.append(node);
assert(node.textContent?.includes('Portable é 👩‍💻 漢字'), 'shared fixture text');
assert(
  node.querySelector('table') && node.querySelector('details') && node.querySelector('button'),
  'semantic catalog',
);
node.querySelector<HTMLButtonElement>('button')!.click();
assert(actions[0][0] === 'stop' && actions[0][1] === 'press', 'button event');
const inputTree: any = {
  type: 'Box',
  props: {},
  children: [
    {
      type: 'Input',
      key: 'in',
      events: ['input', 'submit'],
      props: { value: 'worker', label: 'Label' },
    },
    {
      type: 'Select',
      key: 'sel',
      events: ['select'],
      props: {
        options: [
          { value: 'a', label: 'A' },
          { value: 'b', label: 'B' },
        ],
      },
    },
    { type: 'Progress', props: { value: 0.5, label: 'Progress' } },
    { type: 'Image', props: { resource: 't-i1-image-0', alt: 'alt' } },
    { type: 'Link', props: { href: 'https://example.com', label: 'link' } },
  ],
};
let form = render(inputTree, ctx);
document.body.append(form);
const input = form.querySelector<HTMLInputElement>('input')!;
input.value = 'draft';
input.oninput!({} as any);
flushTimers();
assert(
  actions.some((a) => a[1] === 'input' && a[2] === 'draft'),
  'debounced input',
);
form = render(inputTree, ctx);
assert(
  form.querySelector<HTMLInputElement>('input')!.value === 'draft',
  'unrelated redraw preserves draft',
);
inputTree.children[0].props.value = 'changed';
form = render(inputTree, ctx);
assert(
  form.querySelector<HTMLInputElement>('input')!.value === 'changed',
  'worker value resets draft',
);
assert(
  form.querySelector('progress') &&
    form.querySelector('img') &&
    (form.querySelector('a') as HTMLAnchorElement)?.rel === 'noopener noreferrer',
  'leaves',
);
const disabled = render(inputTree, { ...ctx, enabled: false });
disabled.querySelector<HTMLButtonElement>('button')!.click();
assert(disabled.querySelector<HTMLInputElement>('input')!.disabled, 'offline controls');
assert(
  markdown(
    '# Heading\n- item\n<script>alert(1)</script>\n[x](javascript:evil)',
  ).textContent?.includes('<script>'),
  'raw HTML is text',
);
assert(!markdown('[x](javascript:evil)').querySelector('a'), 'markdown allowlist');
const unknown = render(
  {
    type: 'Future',
    props: { text: 'Fallback' },
    events: ['press'],
    children: [{ type: 'Button', key: 'hidden', events: ['press'], props: { label: 'hidden' } }],
  },
  ctx,
);
assert(
  unknown.textContent?.includes('Fallback') && !unknown.querySelector('button'),
  'passive unknown subtree',
);
assert(
  !validTree({ type: 'Link', props: { href: 'javascript:x' } }, 'pane', 'atto/test'),
  'invalid link',
);
assert(
  !validTree(
    {
      type: 'Box',
      props: {},
      children: [
        { type: 'Text', key: 'x', props: {} },
        { type: 'Text', key: 'x', props: {} },
      ],
    },
    'pane',
    'atto/test',
  ),
  'duplicate key',
);
const engine = render(
  { type: 'engine', props: { site: 'assistantMessage', id: 'i', overrides: { text: 'display' } } },
  { ...ctx, site: 'assistantMessage', id: 'i', engine: () => document.createElement('article') },
);
assert(engine.tagName === 'ARTICLE', 'engine native renderer');
(globalThis as any).checkPromises = () => {
  assert(result?.ok && disconnected, 'RPC promise replies/disconnect');
  (globalThis as any).testDone = true;
};
