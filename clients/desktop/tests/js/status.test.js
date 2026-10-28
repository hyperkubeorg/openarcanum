// Unit tests for status.js's hover text: the state, and when someone was
// last active, never for offline or invisible. Run with `make test-js`.
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const UI = path.join(__dirname, '..', '..', '..', 'shared', 'ui', 'js');
const SRC = fs.readFileSync(path.join(UI, 'status.js'), 'utf8');
const HOSTING = fs.readFileSync(path.join(UI, 'hosting.js'), 'utf8');
const AGO = HOSTING.slice(HOSTING.indexOf('function agoText('), HOSTING.indexOf('\n}\n', HOSTING.indexOf('function agoText(')) + 3);

function load() {
  const fill = (s, vars = {}) => s.replace(/\{(\w+)\}/g, (_m, k) => String(vars[k]));
  const ctx = { Date, t: fill, tn: (one, many, n, vars) => fill(n === 1 ? one : many, { n, ...vars }), statusUI: {}, h: () => ({}) };
  vm.createContext(ctx);
  vm.runInContext(AGO + '\n' + SRC.slice(SRC.indexOf('function statusLabel('), SRC.indexOf('function myStatus(')) +
    SRC.slice(SRC.indexOf('function statusLine('), SRC.indexOf('\n}\n', SRC.indexOf('function statusSentence(')) + 3), ctx);
  return ctx;
}

const now = Date.now();
const shared = (state, extra = {}) => ({ state, text: '', emoji: '', expires_at: 0, fresh_until: now + 3600_000, shared: true, at: now - 1000, idle_since: 0, ...extra });

test('active: the state and "Active now"', () => {
  const c = load();
  assert.equal(c.statusSentence(c.liveStatus(shared('online'))), 'Online - Active now');
  assert.equal(c.statusSentence(c.liveStatus(shared('dnd', { text: 'Focus' }))), 'Do not disturb - Active now: Focus');
});

test('idle from inactivity: when they were last active', () => {
  const c = load();
  assert.equal(c.statusSentence(c.liveStatus(shared('idle', { idle_since: now - 12 * 60_000 }))), 'Idle - Last active 12 minutes ago');
  assert.equal(c.statusSentence(c.liveStatus(shared('dnd', { idle_since: now - 3 * 3600_000 }))), 'Do not disturb - Last active 3 hours ago');
  // Idle chosen by hand, with no time: just the state.
  assert.equal(c.statusSentence(c.liveStatus(shared('idle'))), 'Idle');
});

test('offline, invisible and unshared statuses say nothing about activity', () => {
  const c = load();
  assert.equal(c.statusSentence(c.liveStatus(shared('offline', { idle_since: now - 60_000 }))), 'Offline');
  assert.equal(c.statusSentence(c.liveStatus(shared('online', { idle_since: now - 60_000 }), false)), 'Offline');
  assert.equal(c.statusSentence(c.liveStatus(shared('online', { shared: false }))), 'Online');
  assert.equal(c.statusSentence(c.liveStatus(null)), 'Online'); // connected, no status shared
  assert.equal(c.statusSentence({ state: 'invisible', text: '', emoji: '', activity: { idleSince: now } }), 'Invisible');
  // A contact's status gone stale: offline, nothing more.
  assert.equal(c.statusSentence(c.liveStatus(shared('online', { source: 'contacts', fresh_until: now - 1 }))), 'Offline');
});
