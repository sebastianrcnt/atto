// Runs before main.ts: the saved tabs, hash and storage of each scenario.
const g = globalThis as any;
g.extraThreads = [{ threadId: 't2', name: 'Second', cwd: '/project' }];
const save = (tabs: any[], active: string) =>
  localStorage.setItem('atto.web.tabs', JSON.stringify({ tabs, active }));
switch (g.scenario) {
  case 'hash-and-storage':
    save([{ id: 't2' }, { id: 'gone' }, { id: 'child', offline: true }], 't2');
    location.hash = '#s=t';
    break;
  case 'storage-only':
    save([{ id: 't' }, { id: 't2' }], 't2');
    break;
  case 'no-storage':
    Object.defineProperty(globalThis, 'localStorage', {
      get() {
        throw Error('storage denied');
      },
    });
    location.hash = '#s=t2';
    break;
  case 'all-gone':
    save([{ id: 'gone' }], 'gone');
    location.hash = '#s=gone2';
    break;
}
