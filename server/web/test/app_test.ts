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
  const sidebar = document.querySelector<HTMLElement>('.sidebar')!;
  sidebar.scrollTop = 180;
  sidebar.querySelector<HTMLElement>('.inventory')!.scrollTop = 90;
  find('All').click();
  (globalThis as any).flush();
  assert(
    document.querySelector('.sidebar') === sidebar &&
      sidebar.scrollTop === 180 &&
      sidebar.querySelector<HTMLElement>('.inventory')!.scrollTop === 90,
    'inventory repaint preserves sidebar node and scroll',
  );
  const prompt = document.getElementById('prompt') as HTMLTextAreaElement;
  assert(prompt.rows === 1, 'composer starts as one line');
  const model = document.getElementById('model')!;
  assert(
    model.parentNode &&
      (model.parentNode as HTMLElement).classList.contains('picker') &&
      (model.parentNode as HTMLElement).textContent?.startsWith('Local'),
    'model picker label shows the whole model name',
  );
  prompt.value = 'hello';
  prompt.oninput!({} as any);
  (globalThis as any).flush();
  assert(
    document.getElementById('prompt') === prompt,
    'typing repaints nothing',
  );
  prompt.oncompositionstart!({} as any);
  find('Active').click();
  (globalThis as any).flush();
  assert(
    document.getElementById('prompt') === prompt,
    'no repaint while composing',
  );
  prompt.oncompositionend!({} as any);
  (globalThis as any).flush();
  assert(
    document.getElementById('prompt') === prompt,
    'held repaint keeps editor after composing',
  );
  find('All').click();
  (globalThis as any).flush();
  const typed = document.getElementById('prompt') as HTMLTextAreaElement;
  (globalThis as any).turnEditor = typed;
  assert(typed.value === 'hello', 'draft kept');
  typed.onkeydown!({
    key: 'Enter',
    shiftKey: false,
    ctrlKey: true,
    isComposing: false,
    preventDefault() {},
  } as any);
};
(globalThis as any).checkTurn = () => {
  assert(
    document.getElementById('prompt') === (globalThis as any).turnEditor,
    'editor identical across streamed turn',
  );
  const sc = document.getElementById('transcript')!;
  const item = document.querySelector<HTMLElement>('[data-item=t-i3]')!;
  const body = item.querySelector<HTMLElement>('.stream-body')!;
  const sidebar = document.querySelector<HTMLElement>('.sidebar')!;
  const sidebarChildren = Array.from(sidebar.childNodes);
  const untouched = document.querySelector('[data-item=t-i1]');
  const band = document.querySelector<HTMLElement>('.band')!;
  const tree = band.querySelector('.ui-tree');
  ws.onmessage({
    data: JSON.stringify({
      method: 'item/delta',
      params: { threadId: 't', itemId: 't-i3', delta: ' incremental' },
    }),
  });
  (globalThis as any).flush();
  assert(
    document.getElementById('transcript') === sc &&
      document.querySelector('[data-item=t-i3]') === item &&
      item.querySelector('.stream-body') === body &&
      body.textContent?.includes('incremental'),
    'delta updates only streaming body in place',
  );
  assert(
    document.querySelector('[data-item=t-i1]') === untouched,
    'delta leaves other items alone',
  );
  assert(
    document.querySelector('.sidebar') === sidebar &&
      Array.from(sidebar.childNodes).every((n, i) => n === sidebarChildren[i]),
    'delta leaves sidebar node and children untouched',
  );
  assert(
    band.querySelector('.ui-tree') === tree,
    'delta leaves tree untouched',
  );
  ws.onmessage({
    data: JSON.stringify({
      method: 'ui/render',
      params: {
        threadId: 't',
        site: 'band',
        id: 'ext/band',
        rev: 3,
        tree: { type: 'Text', props: { text: 'Ignored' } },
      },
    }),
  });
  (globalThis as any).flush();
  assert(
    band.querySelector('.ui-tree') === tree,
    'same revision does not rerender tree',
  );
  ws.onmessage({
    data: JSON.stringify({
      method: 'ui/render',
      params: {
        threadId: 't',
        site: 'band',
        id: 'ext/band',
        rev: 4,
        tree: { type: 'Text', props: { text: 'Above prompt updated' } },
      },
    }),
  });
  (globalThis as any).flush();
  assert(
    document.querySelector('.band') === band &&
      band.querySelector('.ui-tree') !== tree,
    'new revision rerenders only that site in stable band',
  );
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
  const oldBlock = document.querySelector('[data-item=t-i2]');
  const reboundBlock = {
    id: 't-i2',
    type: 'uiBlock',
    uiId: 'ext/rebound',
    rev: 4,
    tree: { type: 'Text', props: { text: 'Rebound block' } },
  };
  ws.onmessage({
    data: JSON.stringify({
      method: 'item/updated',
      params: { threadId: 't', item: reboundBlock },
    }),
  });
  (globalThis as any).flush();
  const newBlock = document.querySelector('[data-item=t-i2]');
  assert(
    newBlock !== oldBlock && newBlock?.textContent?.includes('Rebound block'),
    'different uiBlock instance renders even at the same revision',
  );
  ws.onmessage({
    data: JSON.stringify({
      method: 'item/updated',
      params: {
        threadId: 't',
        item: {
          ...reboundBlock,
          tree: { type: 'Text', props: { text: 'Same revision ignored' } },
        },
      },
    }),
  });
  (globalThis as any).flush();
  assert(
    document.querySelector('[data-item=t-i2]') === newBlock &&
      newBlock?.textContent?.includes('Rebound block'),
    'same uiBlock instance/revision retains row and drawing',
  );
  // Identical provider-local keys in different pane instances are independent.
  for (const name of ['first', 'second']) {
    ws.onmessage({
      data: JSON.stringify({
        method: 'ui/open',
        params: {
          threadId: 't',
          site: 'pane',
          id: 'ext/' + name,
          rev: 1,
          options: { title: name + ' pane' },
        },
      }),
    });
    ws.onmessage({
      data: JSON.stringify({
        method: 'ui/render',
        params: {
          threadId: 't',
          site: 'pane',
          id: 'ext/' + name,
          rev: 2,
          tree: {
            type: 'Collapse',
            key: 'fold',
            props: { title: name + ' disclosure' },
            children: [{ type: 'Text', props: { text: 'body' } }],
          },
        },
      }),
    });
  }
  (globalThis as any).flush();
  find('first pane').click();
  (globalThis as any).flush();
  const firstDisclosure =
    document.querySelector<HTMLDetailsElement>('[data-key=fold]')!;
  firstDisclosure.open = true;
  firstDisclosure.ontoggle!({} as any);
  find('second pane').click();
  (globalThis as any).flush();
  assert(
    !document.querySelector<HTMLDetailsElement>('[data-key=fold]')!.open,
    'pane switch does not copy another site disclosure state',
  );
  find('first pane').click();
  (globalThis as any).flush();
  assert(
    document.querySelector('[data-key=fold]') === firstDisclosure &&
      firstDisclosure.open,
    'returning pane retains its own tree node and disclosure state',
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
  (globalThis as any).flush();
  const userRow = document.querySelector('[data-item=user]');
  assert(userRow, 'user row drawn');
  ws.onmessage({
    data: JSON.stringify({
      method: 'turn/started',
      eventId: 29,
      params: { threadId: 't' },
    }),
  });
  (globalThis as any).flush();
  const transcript = document.getElementById('transcript')!;
  const tail = transcript.querySelector<HTMLElement>('.transcript-tail')!;
  assert(
    tail &&
      transcript.childNodes[transcript.childNodes.length - 1] === tail &&
      tail.textContent?.includes('Thinking…'),
    'busy turn without streaming text shows a tail indicator in the transcript',
  );
  assert(
    !document.querySelector('.composer-area')!.querySelector('.shimmer'),
    'no activity line above the composer',
  );
  assert(
    document.querySelector('[data-item=user]') === userRow,
    'tail indicator does not redraw transcript items',
  );
  ws.onmessage({
    data: JSON.stringify({
      method: 'turn/activity',
      params: { threadId: 't', activity: { phase: 'Working' } },
    }),
  });
  (globalThis as any).flush();
  assert(
    transcript.querySelector('.transcript-tail') === tail &&
      tail.textContent?.includes('Working…') &&
      document.querySelector('[data-item=user]') === userRow,
    'tail follows the activity phase in place',
  );
  emit(
    'item/started',
    {
      id: 'quiet-tool',
      type: 'commandExecution',
      status: 'inProgress',
      description: 'Wait',
      command: 'sleep 1',
      output: '',
    },
    29.5,
  );
  (globalThis as any).flush();
  const quiet = document.querySelector<HTMLElement>('[data-item=quiet-tool]')!;
  const quietBody = quiet.querySelector<HTMLElement>('.stream-body')!;
  assert(quietBody.hidden, 'running tool without output has no output box');
  assert(
    transcript.childNodes[transcript.childNodes.length - 1] === tail,
    'tail stays after the last item',
  );
  ws.onmessage({
    data: JSON.stringify({
      method: 'item/delta',
      params: { threadId: 't', itemId: 'quiet-tool', delta: 'tick' },
    }),
  });
  (globalThis as any).flush();
  assert(
    document.querySelector('[data-item=quiet-tool]') === quiet &&
      !quietBody.hidden &&
      quietBody.textContent?.includes('tick'),
    'first output shows the box in place',
  );
  emit(
    'item/started',
    { id: 'answer', type: 'agentMessage', status: 'inProgress', text: '' },
    29.6,
  );
  (globalThis as any).flush();
  assert(
    transcript.querySelector('.transcript-tail') === tail && !tail.textContent,
    'streaming text hides the tail indicator',
  );
  emit(
    'item/completed',
    { id: 'answer', type: 'agentMessage', status: 'completed', text: 'Done' },
    29.7,
  );
  emit(
    'item/completed',
    {
      id: 'quiet-tool',
      type: 'commandExecution',
      status: 'completed',
      description: 'Wait',
      command: 'sleep 1',
      output: 'tick',
      exitCode: 0,
    },
    29.8,
  );
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
      Array.from(
        document.querySelector('.queue')!.querySelectorAll('button'),
      ).some(
        (b) => b.textContent === 'Edit',
      ),
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
    !document.querySelector('.transcript-tail')!.textContent,
    'tail indicator gone when the turn ends',
  );
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
  const edits = document.querySelectorAll<HTMLButtonElement>('.message-edit');
  assert(
    edits.length === 1 &&
      edits[0].textContent === 'Edit' &&
      edits[0].title === 'Edit and resend from here, in a new session',
    'Edit (fork from here) only on user messages',
  );
  assert(!find('Fork'), 'no Fork label');
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
  const thinking = document.querySelector<HTMLDetailsElement>('.thinking')!;
  thinking.open = true;
  (globalThis as any).openThinking = thinking;
  const more = document.querySelector<HTMLDetailsElement>('.composer-more');
  if (more) more.open = true;
  (globalThis as any).unchangedItem =
    document.querySelector('[data-item=t-i1]');
  // Deltas inside a disclosure retain that disclosure itself and its state.
  const thinkingBody = thinking.querySelector('.stream-body');
  ws.onmessage({
    data: JSON.stringify({
      method: 'item/delta',
      params: {
        threadId: 't',
        itemId: 'thinking',
        delta: ' more reasoning',
      },
    }),
  });
  (globalThis as any).flush();
  assert(
    document.querySelector('.thinking') === thinking &&
      thinking.open &&
      thinking.querySelector('.stream-body') === thinkingBody,
    'streaming reasoning preserves open disclosure and body node',
  );
  const effort = document.getElementById('effort') as HTMLSelectElement;
  effort.value = 'high';
  effort.onchange!({} as any);
};
(globalThis as any).checkMutation = () => {
  assert(
    document.querySelector('.thinking') === (globalThis as any).openThinking &&
      (globalThis as any).openThinking.open,
    'open details survive unrelated settings updates',
  );
  (globalThis as any).pageItems = Array.from(
    document.querySelectorAll('[data-item]'),
  );
  (globalThis as any).pageEditor = document.getElementById('prompt');
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
  const sc = document.getElementById('transcript')!;
  Object.defineProperty(sc, 'scrollHeight', {
    get: () =>
      Array.from(sc.childNodes).reduce(
        (n, x) => n + ((x as HTMLElement).dataset.item ? 100 : 20),
        0,
      ),
  });
  sc.scrollTop = 80;
  (globalThis as any).pageHeight = sc.scrollHeight;
  find('Load earlier messages').click();
};
(globalThis as any).checkPageMerge = () => {
  const sc = document.getElementById('transcript')!;
  assert(
    sc.scrollTop === 80 + sc.scrollHeight - (globalThis as any).pageHeight,
    'paging preserves scroll anchor',
  );
  for (const node of (globalThis as any).pageItems)
    assert(
      document.querySelector('[data-item=' + node.dataset.item + ']') === node,
      'older insertion keeps every existing item node',
    );
  assert(
    document.getElementById('prompt') === (globalThis as any).pageEditor,
    'paging keeps composer node',
  );
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
  const input = document.getElementById('prompt') as HTMLTextAreaElement;
  input.value = 'primary draft';
  input.oninput!({} as any);
  input.setSelectionRange(2, 5);
  (globalThis as any).primaryEditor = input;
  (globalThis as any).primaryTranscript = document.getElementById('transcript');
  (globalThis as any).primaryTranscript.scrollTop = 100;
  (globalThis as any).clearDetachedScroll = true;
  find('+ New').click();
};
(globalThis as any).checkSecondTab = () => {
  const input = document.getElementById('prompt') as HTMLTextAreaElement;
  assert(
    input !== (globalThis as any).primaryEditor,
    'one editor per thread view',
  );
  input.value = 'second draft';
  input.oninput!({} as any);
  const sc = document.getElementById('transcript')!;
  assert(
    sc.querySelector('[data-item=empty-first]'),
    'empty uiBlock placeholder has an item key',
  );
  Object.defineProperty(sc, 'scrollHeight', {
    get: () => 1000 + sc.childNodes.length * 100,
  });
  sc.scrollTop = 80;
  ws.onmessage({
    data: JSON.stringify({
      method: 'item/started',
      eventId: 1000,
      params: {
        threadId: 't2',
        item: {
          id: 'background-tail',
          type: 'agentMessage',
          text: 'Tail append',
        },
      },
    }),
  });
  (globalThis as any).flush();
  assert(
    sc.scrollTop === 80,
    'tail append after an empty first block preserves reader position',
  );
  (globalThis as any).secondaryEditor = input;
  find('Test').click();
};
(globalThis as any).checkTabBack = () => {
  const input = document.getElementById('prompt') as HTMLTextAreaElement;
  assert(
    input === (globalThis as any).primaryEditor &&
      input.value === 'primary draft' &&
      input.selectionStart === 2 &&
      input.selectionEnd === 5,
    'tab restores existing editor with draft and cursor',
  );
  assert(
    document.getElementById('transcript') ===
      (globalThis as any).primaryTranscript,
    'tab restores existing transcript container',
  );
  assert(
    (globalThis as any).primaryTranscript.scrollTop === 100,
    'tab restores detached transcript scroll offset',
  );
  find('Second').click();
  (globalThis as any).flush();
  assert(
    document.getElementById('prompt') === (globalThis as any).secondaryEditor &&
      (document.getElementById('prompt') as HTMLTextAreaElement).value ===
        'second draft',
    'second thread editor retained',
  );
  find('Test').click();
  (globalThis as any).flush();
  (globalThis as any).reconnectEditor = document.getElementById('prompt');
  (globalThis as any).reconnectItem =
    document.querySelector('[data-item=t-i1]');
  const before = sent.length;
  ws.close();
  assert(sent.length === before, 'disconnect never resends');
  (globalThis as any).flushTimers();
};
(globalThis as any).checkReconnect = () => {
  assert(
    document.getElementById('prompt') === (globalThis as any).reconnectEditor,
    'reconnect hydration retains composer',
  );
  assert(
    document.querySelector('[data-item=t-i1]') ===
      (globalThis as any).reconnectItem,
    'reconnect keeps identical snapshot row',
  );
  const input = document.getElementById('prompt') as HTMLTextAreaElement;
  input.value = '/tree';
  input.oninput!({} as any);
  input.onkeydown!({
    key: 'Enter',
    shiftKey: false,
    isComposing: false,
    preventDefault() {},
  } as any);
};
(globalThis as any).checkTerminalOnly = () => {
  assert(
    !sent.some((m) => m.method === 'thread/tree') &&
      !document.querySelector('[role=dialog]') &&
      document.body.textContent?.includes(
        '/tree is only available in the terminal',
      ),
    '/tree shows a terminal-only notice and opens nothing',
  );
  assert(!find('Tree'), 'no Tree button in the topbar');
  (globalThis as any).testDone = true;
};
