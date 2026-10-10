// Runs before main.ts: an unnamed session whose loaded page starts after its
// first prompt, served first by a worker of an older build.
const g = globalThis as any;
g.extraThreads = [
  {
    threadId: 'u',
    preview: 'do you know how to code in c#?',
    cwd: '/project',
    loaded: true,
  },
];
g.current = false;
const base = {
  cwd: '/project',
  model: 'local/m',
  effort: 'off',
  efforts: ['off', 'high'],
  busy: false,
  eventId: 5,
  serverInstanceId: 's',
  contextTokens: 0,
  ui: { version: 1, instances: [] },
};
g.snapshotFor = (method: string, id: string) => {
  if (method === 'thread/start')
    return {
      ...base,
      threadId: 'fresh',
      hasMore: false,
      items: [{ id: 'f-i1', type: 'userMessage', text: 'a brand new prompt' }],
      runtimeVersion: 'v0.0.2',
    };
  if (id !== 'u') return null;
  return {
    ...base,
    threadId: 'u',
    hasMore: true,
    before: 'u-i1',
    items: [
      { id: 'u-i1', type: 'userMessage', text: 'cpp is painful to build with' },
      { id: 'u-i2', type: 'agentMessage', text: 'It can be.' },
    ],
    runtimeVersion: g.current ? 'v0.0.2' : 'v0.0.1',
    ...(g.current ? {} : { runtimeOutdated: true }),
  };
};
location.hash = '#s=u';
