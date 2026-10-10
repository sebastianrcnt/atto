// Reload restore: saved tabs (localStorage) and the active one (#s=<id>).
import './restore_setup';
import '../src/main';
declare const sent: any[];
function assert(ok: any, s: string) {
  if (!ok) throw Error(s);
}
const g = globalThis as any;
const tabs = () =>
  Array.from(document.querySelectorAll('button'))
    .filter((b) => b.classList.contains('tab'))
    .map((b) => b.textContent);
const shown = () => document.getElementById('transcript')?.dataset.thread;
const stored = () => JSON.parse(localStorage.getItem('atto.web.tabs')!);
const sentFor = (method: string, id: string) =>
  sent.some((m) => m.method === method && m.params?.threadId === id);
g.checkRestore = () => {
  assert(!sent.some((m) => m.method === 'thread/start'), 'never auto-creates');
  assert(
    !sentFor('thread/resume', 'gone') && !sentFor('thread/read', 'gone'),
    'a missing thread is dropped without opening it',
  );
  switch (g.scenario) {
    case 'hash-and-storage': {
      assert(
        tabs().join('|') === 'Second|Child|Test',
        'tabs reopened in saved order, hash thread appended: ' +
          tabs().join('|'),
      );
      assert(shown() === 't', 'hash wins over the stored active tab');
      assert(
        sent.some(
          (m) =>
            m.method === 'thread/read' &&
            m.params.threadId === 'child' &&
            m.params.offline,
        ) && !sentFor('thread/resume', 'child'),
        'archived tab read offline, not resumed',
      );
      assert(sentFor('thread/resume', 't2'), 'stored tab resumed');
      const s = stored();
      assert(
        JSON.stringify(s.tabs) ===
          JSON.stringify([
            { id: 't2', offline: false },
            { id: 'child', offline: true },
            { id: 't', offline: false },
          ]) && s.active === 't',
        'storage rewritten without the missing thread: ' + JSON.stringify(s),
      );
      assert(location.hash === '#s=t', 'hash names the active thread');
      Array.from(document.querySelectorAll('button'))
        .find((b) => b.textContent === 'Second')!
        .click();
      g.flush();
      assert(
        location.hash === '#s=t2' && stored().active === 't2',
        'switching tabs updates hash and storage',
      );
      break;
    }
    case 'storage-only':
      assert(tabs().join('|') === 'Test|Second', 'stored tabs reopened');
      assert(shown() === 't2', 'stored active tab selected');
      assert(location.hash === '#s=t2', 'hash written from storage');
      break;
    case 'no-storage':
      assert(tabs().join('|') === 'Second', 'hash alone reopens a session');
      assert(shown() === 't2', 'hash session active without storage');
      break;
    case 'all-gone': {
      assert(!tabs().length && !shown(), 'nothing to restore');
      const recents = document.querySelector('.recents');
      assert(
        recents?.textContent?.includes('Test') &&
          recents.textContent.includes('Second'),
        'empty state lists recent sessions',
      );
      const start = Array.from(document.querySelectorAll('button')).find(
        (b) => b.textContent === 'New session',
      )!;
      assert(
        start.classList.contains('new-secondary'),
        'New session is secondary next to recents',
      );
      assert(
        location.hash === '' && !stored().tabs.length,
        'missing ids forgotten',
      );
      Array.from(recents!.querySelectorAll('button'))
        .find((b) => b.textContent?.includes('Second'))!
        .click();
      break;
    }
  }
};
g.checkRestoreAfter = () => {
  if (g.scenario === 'all-gone')
    assert(
      sentFor('thread/resume', 't2') && shown() === 't2',
      'a recent opens like a sidebar row',
    );
  g.testDone = true;
};
