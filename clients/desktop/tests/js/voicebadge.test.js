// Unit tests for app.js's voice badge on the rail: one icon, the strongest
// of screen share > camera > people in voice. Run with `make test-js`.
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const APP = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'shared', 'ui', 'js', 'app.js'), 'utf8');
const SECTION = APP.slice(APP.indexOf('const VOICE_KINDS'), APP.indexOf('function voiceBadge('));

function activity(rooms, connection = 'online') {
  const ctx = { voiceUI: { rooms: { c: rooms } } };
  vm.createContext(ctx);
  vm.runInContext(SECTION + '\nglobalThis.voiceActivity = voiceActivity;', ctx);
  return JSON.parse(JSON.stringify(ctx.voiceActivity({ id: 'c', connection })));
}

const p = (camera = false, screen = false) => ({ camera, screen });

test('nobody in voice: no badge', () => {
  assert.equal(activity([]), null);
  assert.equal(activity([{ channel: 1, participants: [] }]), null);
});

test('people in voice, no video: the voice badge, with the count', () => {
  assert.deepEqual(activity([{ channel: 1, participants: [p(), p()] }]), { kind: 'voice', people: 2 });
});

test('a camera outranks voice; a screen share outranks both, across channels', () => {
  assert.deepEqual(activity([{ channel: 1, participants: [p(), p(true)] }]), { kind: 'camera', people: 2 });
  assert.deepEqual(activity([{ channel: 1, participants: [p(true)] }, { channel: 2, participants: [p(false, true), p()] }]), { kind: 'screen', people: 3 });
  assert.deepEqual(activity([{ channel: 1, participants: [p(true, true)] }]), { kind: 'screen', people: 1 });
});

test('a community that is not connected shows nothing (its room list is stale)', () => {
  assert.equal(activity([{ channel: 1, participants: [p()] }], 'offline'), null);
});
