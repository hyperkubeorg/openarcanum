// Unit tests for the rail's order and folders (app.js "the rail's order and
// folders"): moving, folding two communities into a folder, ungrouping,
// and communities that come or go. Run with `make test-js`.
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const APP = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'shared', 'ui', 'js', 'app.js'), 'utf8');
const SECTION = APP.slice(APP.indexOf("// ---------------------------------------------------------------- the rail's order and folders"), APP.indexOf('function folderMenu('));

function rail(ids, layout = []) {
  const saved = [];
  const ctx = {
    st: { communities: ids.map((id) => ({ id, name: id.toUpperCase() })) },
    api: (method, key, value) => { if (method === 'ui_state_set') saved.push(value); return Promise.resolve(); },
    renderRail: () => {},
    t: (s) => s,
    Date,
  };
  vm.createContext(ctx);
  vm.runInContext(SECTION + '\nrailUI.layout = ' + JSON.stringify(layout) + ';', ctx);
  ctx.saved = saved;
  return ctx;
}
const shape = (ctx) => JSON.parse(JSON.stringify(ctx.railEntries())).map((e) => (typeof e === 'string' ? e : { name: e.name, items: e.items }));

test('new communities go at the end, gone ones drop out', () => {
  const r = rail(['a', 'b', 'c'], ['c', 'x', 'a']);
  assert.deepEqual(shape(r), ['c', 'a', 'b']);
});

test('moving before and after, and one step up or down', () => {
  const r = rail(['a', 'b', 'c']);
  r.railMove('c', 'a', 'before');
  assert.deepEqual(shape(r), ['c', 'a', 'b']);
  r.railMove('c', 'b', 'after');
  assert.deepEqual(shape(r), ['a', 'b', 'c']);
  r.railStep('a', 1);
  assert.deepEqual(shape(r), ['b', 'a', 'c']);
  assert.equal(r.saved.length, 3);
});

test('dropping onto a community makes a folder; onto a folder adds to it', () => {
  const r = rail(['a', 'b', 'c', 'd']);
  r.railMove('b', 'a', 'into');
  assert.deepEqual(shape(r), [{ name: '', items: ['a', 'b'] }, 'c', 'd']);
  const f = r.railEntries()[0].folder;
  r.railMove('d', f, 'into');
  assert.deepEqual(shape(r), [{ name: '', items: ['a', 'b', 'd'] }, 'c']);
  // Within a folder, into a community there just places it after.
  r.railMove('c', 'a', 'into');
  assert.deepEqual(shape(r), [{ name: '', items: ['a', 'c', 'b', 'd'] }]);
  assert.equal(r.folderName(r.railEntries()[0]), 'A, C, B…');
});

test('folders do not nest, and empty folders go away', () => {
  const r = rail(['a', 'b', 'c', 'd']);
  r.railMove('b', 'a', 'into');
  r.railMove('d', 'c', 'into');
  const [f1, f2] = r.railEntries().map((e) => e.folder);
  r.railMove(f2, 'a', 'into'); // a folder dropped into a folder's community: next to that folder
  assert.deepEqual(shape(r), [{ name: '', items: ['a', 'b'] }, { name: '', items: ['c', 'd'] }]);
  r.railOutOfFolder('a');
  r.railOutOfFolder('b');
  // Each lands just after the folder it left.
  assert.deepEqual(shape(r), ['b', 'a', { name: '', items: ['c', 'd'] }]);
  r.railUngroup(f2);
  assert.deepEqual(shape(r), ['b', 'a', 'c', 'd']);
  assert.ok(f1);
});

test('new folder from the menu, and adding to an existing one', () => {
  const r = rail(['a', 'b', 'c']);
  r.railToFolder('b', '');
  assert.deepEqual(shape(r), ['a', 'c', { name: '', items: ['b'] }]);
  r.railToFolder('a', r.railEntries()[2].folder);
  assert.deepEqual(shape(r), ['c', { name: '', items: ['b', 'a'] }]);
  r.railEditFolder(r.railEntries()[1].folder, (f) => { f.name = 'Friends'; });
  assert.equal(r.folderName(r.railEntries()[1]), 'Friends');
});
