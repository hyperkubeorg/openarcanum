// Unit tests for the notification and call sounds (clients/shared/ui/js/sounds.js)
// with a fake AudioContext: when each sound may play (the master switch,
// each sound's switch, Do not disturb, deafened, muted channels and
// communities, notification levels, the channel being read, bursts), and
// that the sounds are short, quiet and smooth. Run with `make test-js`.
// tests/test_ui_sounds.py checks the same in the real window.
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const UI = path.join(__dirname, '..', '..', '..', 'shared', 'ui', 'js');

// ---------------------------------------------------------------- fakes

class FakeParam {
  constructor() { this.value = 0; this.events = []; }
  setValueAtTime(v, t) { this.events.push(['set', v, t]); }
  linearRampToValueAtTime(v, t) { this.events.push(['linear', v, t]); }
  exponentialRampToValueAtTime(v, t) { this.events.push(['exp', v, t]); }
}

class FakeAudioContext {
  constructor() {
    this.currentTime = 10;
    this.state = 'running';
    this.destination = { name: 'destination' };
    this.oscillators = [];
    this.gains = [];
    this.sinks = [];
  }
  createOscillator() {
    const o = { type: 'sine', frequency: { value: 0 }, connect: (n) => n, start(t) { this.startAt = t; }, stop(t) { this.stopAt = t; } };
    this.oscillators.push(o);
    return o;
  }
  createGain() {
    const g = { gain: new FakeParam(), connect: (n) => { g.to = n; return n; } };
    this.gains.push(g);
    return g;
  }
  resume() { return Promise.resolve(); }
  setSinkId(id) { this.sinks.push(id); return Promise.resolve(); }
}

function load() {
  const ctx = vm.createContext({ console, Set, Map, Math, Promise, Number, String, Date });
  vm.runInContext(fs.readFileSync(path.join(UI, 'sounds.js'), 'utf8')
    + '\nObject.assign(globalThis, { SOUND_NOTES, SOUND_SWITCH, SOUND_SWITCHES, SOUND_PREVIEW, SOUND_PEAK, OWN_SOUNDS,'
    + ' soundBlocked, communityMessageSound, dmMessageSound, voiceRoomChanges, SoundBoard, soundsUI, soundsCallChanged });', ctx);
  return ctx;
}

const S = load();
const ALL = Object.keys(S.SOUND_NOTES);
const prefs = (over = {}) => ({ sounds: true, sound_volume: 50, sounds_off: [], ...over });

// A board on a fake clock that records what it announces and plays.
function board(opts = {}) {
  const clock = { now: 1000 };
  const st = { prefs: prefs(opts.prefs), dnd: false, deafened: false, activated: opts.activated ?? true };
  const announced = [];
  const contexts = [];
  const b = new S.SoundBoard({
    prefs: () => st.prefs,
    state: () => ({ dnd: st.dnd, deafened: st.deafened }),
    now: () => clock.now,
    activated: () => st.activated,
    makeContext: () => { const c = new FakeAudioContext(); contexts.push(c); return c; },
    announce: (name) => announced.push(name),
    sinkId: async (label) => (label === 'USB headset' ? 'dev-usb' : ''),
  });
  return { b, clock, st, announced, contexts };
}

const free = (over = {}) => ({ prefs: prefs(over.prefs), dnd: false, deafened: false, now: 100000, last: {}, lastArrival: -Infinity, ...over });

// ---------------------------------------------------------------- the rules

test('the master switch off silences every sound', () => {
  for (const name of ALL) {
    assert.equal(S.soundBlocked(name, free()), '', name);
    assert.equal(S.soundBlocked(name, free({ prefs: prefs({ sounds: false }) })), 'off', name);
    assert.equal(S.soundBlocked(name, free({ prefs: prefs({ sound_volume: 0 }) })), 'silent', name);
  }
  const { b, announced, contexts } = board({ prefs: { sounds: false } });
  for (const name of ALL) assert.equal(b.play(name), false);
  assert.deepEqual(announced, []);
  assert.equal(contexts.length, 0);
});

test('settings not read yet: no sound', () => {
  assert.equal(S.soundBlocked('message', free({ prefs: null })), 'unset');
});

test("a sound's own switch turns it off, and only it", () => {
  const off = (list) => free({ prefs: prefs({ sounds_off: list }) });
  assert.equal(S.soundBlocked('mention', off(['mention'])), 'switch');
  assert.equal(S.soundBlocked('message', off(['mention'])), '');
  // One switch for each pair.
  for (const name of ['mute', 'unmute']) assert.equal(S.soundBlocked(name, off(['mute'])), 'switch');
  for (const name of ['deafen', 'undeafen']) assert.equal(S.soundBlocked(name, off(['deafen'])), 'switch');
  for (const name of ['connect', 'disconnect']) assert.equal(S.soundBlocked(name, off(['call'])), 'switch');
  // Every sound has a switch, and every switch a preview.
  for (const name of ALL) assert.ok(S.SOUND_SWITCHES.includes(S.SOUND_SWITCH[name]), name);
  for (const key of S.SOUND_SWITCHES) assert.ok(S.SOUND_NOTES[S.SOUND_PREVIEW[key]], key);
});

test("Do not disturb and deafen leave only your own call's sounds", () => {
  for (const state of [{ dnd: true }, { deafened: true }]) {
    for (const name of ['message', 'dm', 'mention', 'join', 'leave']) assert.notEqual(S.soundBlocked(name, free(state)), '', name);
    for (const name of ['mute', 'unmute', 'deafen', 'undeafen', 'connect', 'disconnect']) assert.equal(S.soundBlocked(name, free(state)), '', name);
  }
});

test('muted communities and channels, and notification levels', () => {
  const msg = { id: 5, text: 'hi' };
  const mention = { id: 6, text: '@me', mentions_me: true };
  const env = (over = {}) => ({ muted: false, level: 'all', channelMuted: false, reading: false, ...over });
  assert.equal(S.communityMessageSound(msg, env()), 'message');
  assert.equal(S.communityMessageSound(mention, env()), 'mention');
  // A muted channel: only mentions, as for notifications.
  assert.equal(S.communityMessageSound(msg, env({ channelMuted: true })), '');
  assert.equal(S.communityMessageSound(mention, env({ channelMuted: true })), 'mention');
  // A muted community, or Nothing: not even mentions.
  assert.equal(S.communityMessageSound(mention, env({ muted: true })), '');
  assert.equal(S.communityMessageSound(mention, env({ level: 'nothing' })), '');
  // Mentions only.
  assert.equal(S.communityMessageSound(msg, env({ level: 'mentions' })), '');
  assert.equal(S.communityMessageSound(mention, env({ level: 'mentions' })), 'mention');
  assert.equal(S.communityMessageSound({ ...msg, deleted: true }, env()), '');
  // Direct messages follow the default level.
  assert.equal(S.dmMessageSound(msg, { mode: 'all' }), 'dm');
  assert.equal(S.dmMessageSound(msg, { mode: 'mentions' }), '');
  assert.equal(S.dmMessageSound(mention, { mode: 'mentions' }), 'mention');
  assert.equal(S.dmMessageSound({ ...msg, mine: true }, { mode: 'all' }), '');
});

test('the channel or conversation you are reading still sounds (only the desktop notification is left out)', () => {
  const env = { muted: false, level: 'all', channelMuted: false, reading: true };
  assert.equal(S.communityMessageSound({ id: 1 }, env), 'message');
  assert.equal(S.communityMessageSound({ id: 1, mentions_me: true }, env), 'mention');
  assert.equal(S.dmMessageSound({ id: 1 }, { mode: 'all', reading: true }), 'dm');
  assert.equal(S.dmMessageSound({ id: 1, mentions_me: true }, { mode: 'all', reading: true }), 'mention');
  // Muting, and "nothing", still win.
  assert.equal(S.communityMessageSound({ id: 1 }, { ...env, muted: true }), '');
  assert.equal(S.communityMessageSound({ id: 1 }, { ...env, level: 'nothing' }), '');
});

test('a burst plays once, and each sound has a cooldown', () => {
  const { b, clock, announced, contexts } = board();
  for (let i = 0; i < 20; i++) { b.play('message'); clock.now += 5; }
  assert.deepEqual(announced, ['message']);
  assert.equal(contexts[0].oscillators.length, S.SOUND_NOTES.message.length);
  // Something else arriving in the same burst doesn't add a sound either.
  assert.equal(b.play('mention'), false);
  assert.equal(b.play('dm'), false);
  // Your own clicks aren't held back by it.
  assert.equal(b.play('mute'), true);
  clock.now += 1100;
  assert.equal(b.play('mention'), true);
  assert.equal(b.play('message'), false); // within its 2 s cooldown and the burst
  clock.now += 2100;
  assert.equal(b.play('message'), true);
  assert.deepEqual(announced, ['message', 'mute', 'mention', 'message']);
  // Joins: two quick ones play once.
  assert.equal(b.play('join'), true);
  clock.now += 100;
  assert.equal(b.play('join'), false);
});

// ---------------------------------------------------------------- the sounds

test('every sound is short, quiet and smooth', () => {
  for (const name of ALL) {
    const { b, contexts } = board({ prefs: { sound_volume: 100 } });
    assert.equal(b.play(name), true);
    const c = contexts[0];
    const start = Math.min(...c.oscillators.map((o) => o.startAt));
    const end = Math.max(...c.oscillators.map((o) => o.stopAt));
    assert.ok(end - start <= 0.25, `${name} lasts ${end - start}s`);
    for (const o of c.oscillators) assert.ok(['sine', 'triangle'].includes(o.type), name);
    for (const g of c.gains) {
      const [first, attack, release] = g.gain.events;
      // From silence, a ramp up (no click), a smooth fall back to silence.
      assert.equal(first[0], 'set'); assert.ok(first[1] <= 0.001);
      assert.equal(attack[0], 'linear'); assert.ok(attack[2] - first[2] >= 0.005, `${name} attack`);
      assert.ok(attack[1] <= S.SOUND_PEAK, `${name} too loud`);
      assert.equal(release[0], 'exp'); assert.ok(release[1] <= 0.001);
      assert.equal(g.to, c.destination);
    }
  }
});

test('the volume scales the sound', () => {
  const peak = (vol) => { const { b, contexts } = board({ prefs: { sound_volume: vol } }); b.play('dm'); return contexts[0].gains[0].gain.events[1][1]; };
  assert.ok(Math.abs(peak(100) - 2 * peak(50)) < 1e-9);
});

test('no AudioContext before the user has interacted; then one, made at the first sound', () => {
  const { b, st, contexts, announced } = board({ activated: false });
  assert.equal(contexts.length, 0);
  assert.equal(b.play('message'), true); // requested, but the page may not make sound yet
  assert.deepEqual(announced, ['message']);
  assert.equal(contexts.length, 0);
  st.activated = true;
  b.play('mute');
  b.play('unmute');
  assert.equal(contexts.length, 1);
  assert.equal(contexts[0].oscillators.length, S.SOUND_NOTES.mute.length + S.SOUND_NOTES.unmute.length);
});

test('sounds go to the speaker chosen for calls', async () => {
  const { b, st, contexts } = board({ prefs: { voice_output: 'USB headset' } });
  b.play('mute');
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(contexts[0].sinks, ['dev-usb']);
  st.prefs.voice_output = 'default';
  b.play('unmute');
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(contexts[0].sinks, ['dev-usb', '']);
});

test('preview plays whatever the switches say', () => {
  const { b, announced, contexts } = board({ prefs: { sounds: false, sounds_off: ['dm'] } });
  b.preview('dm', 40);
  assert.deepEqual(announced, ['dm']);
  assert.equal(contexts[0].oscillators.length, S.SOUND_NOTES.dm.length);
});

// ---------------------------------------------------------------- calls

test('who joined and who left a voice room, leaving yourself out', () => {
  const p = (...members) => members.map((member) => ({ member }));
  const changes = (...args) => JSON.parse(JSON.stringify(S.voiceRoomChanges(...args)));
  assert.deepEqual(changes(p('a'), p('a', 'me'), 'me'), { joined: [], left: [] });
  assert.deepEqual(changes(p('a', 'me'), p('a', 'me', 'b'), 'me'), { joined: ['b'], left: [] });
  assert.deepEqual(changes(p('a', 'me'), p('me'), 'me'), { joined: [], left: ['a'] });
});

test('connected once per call; your own mute and deafen', () => {
  const { b, clock, announced } = board();
  S.soundsUI.board = b;
  const state = { active: true, community: 'c', channel: 7 };
  const call = { phase: 'connecting', selfMute: false, selfDeaf: false };
  const step = (change) => { Object.assign(call, change); clock.now += 500; S.soundsCallChanged(call, state); };
  step({});
  step({ phase: 'connected' });
  step({ phase: 'reconnecting' });
  step({ phase: 'connected' });
  step({ selfMute: true });
  step({ selfMute: false });
  // Deafening also mutes: one sound.
  step({ selfDeaf: true, selfMute: true });
  step({ selfDeaf: false });
  assert.deepEqual(announced, ['connect', 'mute', 'unmute', 'deafen', 'undeafen']);
});
