import '../src/main';
declare const sent: any[];
declare const ws: any;
function assert(ok: any, s: string) {
  if (!ok) throw Error(s);
}
function find(text: string) {
  return Array.from(document.querySelectorAll('button')).find(
    (b) => b.textContent === text,
  )!;
}
(globalThis as any).openPage = () => {
  assert(ws.url.endsWith('/ws'), 'socket at /ws');
  assert(ws.protocols.join() === 'atto.rpc.v3', 'no credential offer');
  find('New session').click();
};
(globalThis as any).checkPage = () => {
  assert(
    document.getElementById('transcript')?.textContent?.includes('Initial'),
    'tail hydrated',
  );
  assert(
    document.body.textContent?.includes('Extension transcript block'),
    'uiBlock wire fields',
  );
  assert(
    document.body.textContent?.includes('Above prompt') &&
      document.body.textContent.includes('Status tree'),
    'band/status',
  );
  assert(
    document.body.querySelector(
      innerWidth < 700 ? '.pane-above' : '.pane-dock',
    ),
    'responsive pane site',
  );
  find('All').click();
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
        m.method === 'input/submit' &&
        m.params.intent === 'replace' &&
        m.params.input === 'hello',
    ),
    'native send-now',
  );
  assert(
    document.getElementById('transcript')?.textContent?.includes('Hello'),
    'streamed transcript',
  );

  assert(
    sent.some((m) => m.method === 'thread/list' && m.params.includeArchived),
    'All inventory filter',
  );
  assert(
    document.querySelectorAll('.project-heading').length === 2,
    'project directory groups',
  );
  assert(
    document.querySelectorAll('.session-title').length === 2,
    'finished agent subtree folded by default',
  );
  const fold = document.querySelector<HTMLButtonElement>('.agent-fold')!;
  fold.click();
  (globalThis as any).flush();
  assert(
    document.querySelectorAll('.session-title').length === 3 &&
      document.body.textContent?.includes('Child'),
    'agent children unfold and indent',
  );
  const other = Array.from(document.querySelectorAll('.inventory-row')).find(
    (r) => r.textContent?.includes('Other task'),
  )!;
  other.querySelector<HTMLButtonElement>('.row-menu-button')!.click();
  (globalThis as any).flush();
  assert(find('Delete') && find('Archive'), 'mutations in row menu');
  (globalThis as any).confirm = () => false;
  find('Delete').click();
  assert(
    !sent.some((m) => m.method === 'thread/delete'),
    'delete confirmation cancellation',
  );
  (globalThis as any).confirm = () => true;
  assert(
    !Array.from(
      document.getElementById('effort')!.querySelectorAll('option'),
    ).some((o) => o.textContent === 'stale-effort'),
    'catalog efforts selected model only',
  );
  const emit = (method: string, item: any, eventId: number) =>
    ws.onmessage({
      data: JSON.stringify({
        method,
        eventId,
        params: { threadId: 't', item },
      }),
    });
  emit(
    'item/started',
    {
      id: 'user',
      type: 'userMessage',
      entryId: 'user-entry',
      text: 'A prompt',
    },
    27,
  );
  emit(
    'item/started',
    {
      id: 'tool',
      type: 'commandExecution',
      status: 'inProgress',
      description: 'Inspect files',
      command: 'ls',
      output: 'live output',
    },
    28,
  );
  ws.onmessage({
    data: JSON.stringify({
      method: 'turn/started',
      eventId: 29,
      params: { threadId: 't' },
    }),
  });
  (globalThis as any).flush();
  ws.onmessage({
    data: JSON.stringify({
      method: 'turn/pending',
      params: {
        threadId: 't',
        pending: {
          items: [{ id: 'queued', kind: 'queued', text: 'Next task' }],
        },
      },
    }),
  });
  ws.onmessage({
    data: JSON.stringify({
      method: 'ui/render',
      params: {
        threadId: 't',
        site: 'band',
        id: 'atto/queue',
        rev: 5,
        tree: { type: 'Text', props: { text: 'Queued · Next task' } },
      },
    }),
  });
  (globalThis as any).flush();
  assert(
    document.querySelector('.queue')?.textContent?.includes('Next task') &&
      find('Edit'),
    'shared queue tree above composer with takeback',
  );
  assert(
    document.querySelector('.queue')!.textContent!.split('Next task').length ===
      2,
    'queue preview not duplicated',
  );
  ws.onmessage({
    data: JSON.stringify({
      method: 'turn/pending',
      params: { threadId: 't', pending: null },
    }),
  });
  ws.onmessage({
    data: JSON.stringify({
      method: 'ui/render',
      params: {
        threadId: 't',
        site: 'band',
        id: 'atto/queue',
        rev: 6,
        tree: null,
      },
    }),
  });
  assert(
    (document.querySelector('.tool-chip') as HTMLDetailsElement).open,
    'running tool expanded',
  );
  assert(
    find('Queue') &&
      find('Steer') &&
      document.querySelector('[aria-label=Stop]'),
    'busy composer actions',
  );
  emit(
    'item/completed',
    {
      id: 'tool',
      type: 'commandExecution',
      entryId: 'tool-entry',
      status: 'completed',
      description: 'Inspect files',
      command: 'ls',
      output: Array.from({ length: 50 }, (_, i) => 'line ' + i).join('\n'),
      exitCode: 0,
      durationMs: 1200,
    },
    30,
  );
  emit(
    'item/completed',
    {
      id: 'thinking',
      type: 'reasoning',
      status: 'completed',
      entryId: 'think-entry',
      text: 'private reasoning',
    },
    31,
  );
  emit(
    'item/completed',
    {
      id: 'notice',
      type: 'notice',
      entryId: 'notice-entry',
      text: 'Worked for 1.2s',
    },
    32,
  );
  ws.onmessage({
    data: JSON.stringify({
      method: 'turn/completed',
      eventId: 33,
      params: { threadId: 't' },
    }),
  });
  (globalThis as any).flush();
  assert(
    !(document.querySelector('.tool-chip') as HTMLDetailsElement).open,
    'completed tool auto-collapsed',
  );
  assert(
    document
      .querySelector('.tool-chip')
      ?.textContent?.includes('exit 0 · 1.2s'),
    'tool metrics',
  );
  assert(
    document.querySelectorAll('.fork').length === 1,
    'fork only on user messages',
  );
  assert(
    document.querySelector('.thinking')?.textContent?.includes('Thinking'),
    'human reasoning label',
  );
  assert(
    !document.querySelector('.notice')?.querySelector('header'),
    'quiet notice',
  );
  find('Show more').click();
  (globalThis as any).flush();
  assert(
    document.querySelector('.tool-chip')?.textContent?.includes('line 0'),
    'show more restores long output',
  );
  assert(
    !find('Queue') && document.querySelector('[aria-label=Send]'),
    'idle composer actions',
  );
  (globalThis as any).unchangedItem =
    document.querySelector('[data-item=t-i1]');
  const effort = document.getElementById('effort') as HTMLSelectElement;
  effort.value = 'high';
  effort.onchange!({} as any);
};
(globalThis as any).checkMutation = () => {
  assert(
    document.querySelector('[data-item=t-i1]') ===
      (globalThis as any).unchangedItem,
    'unchanged transcript DOM stable through settings/status repaint',
  );
  assert(
    document.getElementById('transcript')?.textContent?.includes('Hello'),
    'mutation preserves transcript',
  );
  assert(
    document.body.textContent?.includes('Above prompt'),
    'mutation preserves UI',
  );
  find('Load earlier messages').click();
};
(globalThis as any).checkPageMerge = () => {
  assert(
    document.getElementById('transcript')?.textContent?.includes('Earlier'),
    'paging',
  );
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
        tree: {
          type: 'Button',
          key: 'ext/yes',
          events: ['press'],
          props: { label: 'Approve' },
        },
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
    document
      .getElementById('transcript')
      ?.textContent?.includes('Native override'),
    'engine reference rendered natively',
  );
  assert(
    document
      .getElementById('transcript')
      ?.textContent?.includes('Extension footer') && find('Show original'),
    'original available outside drawing',
  );
  const before = sent.length;
  ws.close();
  assert(sent.length === before, 'disconnect never resends');
  (globalThis as any).testDone = true;
};
