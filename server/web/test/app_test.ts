import '../src/main';
declare const sent: any[];
declare const ws: any;
function assert(ok: any, s: string) {
  if (!ok) throw Error(s);
}
function find(text: string) {
  return Array.from(document.querySelectorAll('button')).find((b) => b.textContent === text)!;
}
(globalThis as any).openPage = () => {
  assert(!ws.url.includes('token='), 'no query token');
  assert(ws.protocols.includes('atto.auth.secret'), 'header token offer');
  assert(location.hash === '', 'fragment consumed');
  find('New session').click();
};
(globalThis as any).checkPage = () => {
  assert(document.getElementById('transcript')?.textContent?.includes('Initial'), 'tail hydrated');
  assert(document.body.textContent?.includes('Extension transcript block'), 'uiBlock wire fields');
  assert(
    document.body.textContent?.includes('Above prompt') &&
      document.body.textContent.includes('Status tree'),
    'band/status',
  );
  assert(
    document.body.querySelector(innerWidth < 700 ? '.pane-above' : '.pane-dock'),
    'responsive pane site',
  );
  const prompt = document.getElementById('prompt') as HTMLTextAreaElement;
  prompt.value = 'hello';
  prompt.oninput!({} as any);
  prompt.onkeydown!({
    key: 'Enter',
    shiftKey: false,
    ctrlKey: true,
    isComposing: false,
    preventDefault() {},
  } as any);
};
(globalThis as any).checkTurn = () => {
  assert(
    sent.some(
      (m) =>
        m.method === 'input/submit' && m.params.intent === 'replace' && m.params.input === 'hello',
    ),
    'native send-now',
  );
  assert(
    document.getElementById('transcript')?.textContent?.includes('Hello'),
    'streamed transcript',
  );
  const effort = document.getElementById('effort') as HTMLSelectElement;
  effort.value = 'high';
  effort.onchange!({} as any);
};
(globalThis as any).checkMutation = () => {
  assert(
    document.getElementById('transcript')?.textContent?.includes('Hello'),
    'mutation preserves transcript',
  );
  assert(document.body.textContent?.includes('Above prompt'), 'mutation preserves UI');
  find('Load earlier messages').click();
};
(globalThis as any).checkPageMerge = () => {
  assert(document.getElementById('transcript')?.textContent?.includes('Earlier'), 'paging');
  assert(
    document.getElementById('transcript')?.textContent?.includes('Hello'),
    'page preserves live item',
  );
  ws.onmessage({
    data: JSON.stringify({
      method: 'ui/open',
      eventId: 40,
      params: {
        threadId: 't',
        site: 'dialog',
        id: 'ext/question',
        rev: 1,
        options: { title: 'Approval' },
      },
    }),
  });
  ws.onmessage({
    data: JSON.stringify({
      method: 'ui/render',
      eventId: 41,
      params: {
        threadId: 't',
        site: 'dialog',
        id: 'ext/question',
        rev: 2,
        tree: { type: 'Button', key: 'ext/yes', events: ['press'], props: { label: 'Approve' } },
      },
    }),
  });
};
(globalThis as any).checkDialog = () => {
  assert(document.body.querySelector('[role=dialog]'), 'dialog site');
  find('Approve').click();
  ws.onmessage({
    data: JSON.stringify({
      method: 'ui/render',
      eventId: 50,
      params: {
        threadId: 't',
        site: 'assistantMessage',
        id: 't-i3',
        rev: 3,
        tree: {
          type: 'Box',
          props: {},
          children: [
            {
              type: 'engine',
              props: {
                site: 'assistantMessage',
                id: 't-i3',
                overrides: { text: 'Native override' },
              },
            },
            { type: 'Text', props: { text: 'Extension footer' } },
          ],
        },
      },
    }),
  });
};
(globalThis as any).checkAction = () => {
  assert(
    sent.some(
      (m) =>
        m.method === 'ui/event' &&
        m.params.rev === 2 &&
        m.params.site === 'dialog' &&
        m.params.key === 'ext/yes',
    ),
    'revision-bound UI action',
  );
  assert(
    document.getElementById('transcript')?.textContent?.includes('Native override'),
    'engine reference rendered natively',
  );
  assert(
    document.getElementById('transcript')?.textContent?.includes('Extension footer') &&
      find('Show original'),
    'original available outside drawing',
  );
  const before = sent.length;
  ws.close();
  assert(sent.length === before, 'disconnect never resends');
  (globalThis as any).testDone = true;
};
