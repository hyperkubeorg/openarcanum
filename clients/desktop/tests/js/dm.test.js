// Unit tests for dm.js's loading: answers that arrive out of order (the web
// client's core answers requests concurrently) never put back an older
// conversation history or list. Run with `make test-js`.
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const SRC = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'shared', 'ui', 'js', 'dm.js'), 'utf8');
const start = SRC.indexOf('const dmUI = {');
const DMUI = SRC.slice(start, SRC.indexOf('\n};\n', start) + 4);

// An api whose answers the test releases, in any order.
function harness() {
  const calls = [];
  const ctx = {
    st: { view: 'dms' },
    api: (method, ...args) => new Promise((resolve) => calls.push({ method, args, resolve })),
    toastError: () => {},
    renderRail: () => {},
    renderSidebar: () => {},
    renderMain: () => {},
    dmViewMatches: () => true,
  };
  vm.createContext(ctx);
  vm.runInContext(DMUI + '\nglobalThis.dmUI = dmUI;', ctx);
  const next = async (method) => {
    for (let i = 0; i < 50 && !calls.some((c) => c.method === method && !c.done); i++) await new Promise((r) => setImmediate(r));
    const c = calls.find((x) => x.method === method && !x.done);
    assert.ok(c, `a ${method} call`);
    c.done = true;
    return c;
  };
  return { ctx, next };
}

const tick = () => new Promise((r) => setImmediate(r));

test('an older conversation load answering last does not replace the newer history', async () => {
  const { ctx, next } = harness();
  const { dmUI } = ctx;
  dmUI.current = 'g';
  const older = dmUI.load('g'); // say, after a status update
  (await next('dm_info')).resolve({ status: 'active' });
  const histA = await next('dm_history'); // asked before the message was stored
  const newer = dmUI.load('g'); // the new message's
  (await next('dm_info')).resolve({ status: 'active' });
  (await next('dm_history')).resolve([{ id: '1', text: 'hello' }, { id: '2', text: 'new message' }]);
  await newer;
  histA.resolve([{ id: '1', text: 'hello' }]); // the older answer, last
  await older;
  await tick();
  assert.deepEqual(dmUI.history.map((m) => m.text), ['hello', 'new message']);
});

test('an older list answering last does not replace the newer list', async () => {
  const { ctx, next } = harness();
  const { dmUI } = ctx;
  const older = dmUI.refresh();
  const newer = dmUI.refresh();
  const [a, b] = [await next('dm_list'), await next('dm_list')];
  b.resolve([{ id: 'g', unread: 1 }]);
  await newer;
  a.resolve([{ id: 'g', unread: 0 }]);
  await older;
  assert.equal(dmUI.list[0].unread, 1);
});

test('awaiting an overtaken load gets the newer data (a rename, then the details shown again)', async () => {
  const { ctx, next } = harness();
  const { dmUI } = ctx;
  dmUI.current = 'g';
  let olderDone = false;
  const older = dmUI.load('g').then(() => { olderDone = true; });
  const newer = dmUI.load('g');
  (await next('dm_info')).resolve({ name: 'Plans' }); // the older's: overtaken
  await tick();
  assert.equal(olderDone, false, 'still waiting for the newer load');
  (await next('dm_info')).resolve({ name: 'Weekend plans' });
  (await next('dm_history')).resolve([]);
  await newer;
  await older;
  assert.equal(dmUI.info.name, 'Weekend plans');
});
