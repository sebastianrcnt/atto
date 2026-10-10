// Tab titles match the sidebar; the older-runtime notice comes and goes with
// the snapshot.
import './title_setup';
import '../src/main';
declare const ws: any;
function assert(ok: any, s: string) {
  if (!ok) throw Error(s);
}
const g = globalThis as any;
const tabs = () =>
  Array.from(document.querySelectorAll('button'))
    .filter((b) => b.classList.contains('tab'))
    .map((b) => b.textContent);
const notice = () => document.querySelector('.runtime-notice');
g.checkTitle = () => {
  assert(
    document.getElementById('transcript')?.dataset.thread === 'u',
    'session opened from the hash',
  );
  assert(
    tabs().join('|') === 'do you know how to code in c#?',
    'tab uses the inventory preview, not the first loaded message: ' +
      tabs().join('|'),
  );
  const rows = Array.from(document.querySelectorAll('button'))
    .filter((b) => b.classList.contains('session-row'))
    .map((b) => b.title);
  assert(
    rows.includes('do you know how to code in c#?') &&
      !rows.some((r) => r.includes('cpp')),
    'sidebar row shows the same title: ' + rows.join('|'),
  );
  assert(
    notice()?.textContent?.includes('older atto (v0.0.1)') &&
      notice()?.textContent?.includes('idle and reopened'),
    'older runtime notice shown',
  );
  // The worker was replaced: the facade resets, the page reads again.
  g.current = true;
  ws.onmessage({
    data: JSON.stringify({ method: 'events/reset', params: { threadId: 'u' } }),
  });
};
g.checkTitleAfter = () => {
  assert(!notice(), 'notice gone once the worker is current');
  assert(
    tabs().join('|') === 'do you know how to code in c#?',
    'title kept after the reread',
  );
  Array.from(document.querySelectorAll('button'))
    .find((b) => b.textContent === '+ New')!
    .click();
};
g.checkTitleNew = () => {
  assert(
    tabs().includes('a brand new prompt'),
    'a session no row knows yet falls back to its first loaded message: ' +
      tabs().join('|'),
  );
  assert(!notice(), 'no notice for a current worker');
  g.testDone = true;
};
