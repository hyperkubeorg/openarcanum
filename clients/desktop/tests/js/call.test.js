// Unit tests for call.js's device and connection handling, with fake
// WebRTC, WebAudio and media devices. Run with `make test-js`. The real
// media path (SFrame on real QtWebEngine pages through a relay) is in
// clients/desktop/tests/test_calls.py and test_ui_voice.py.
//
// A call must start without a microphone or camera, or with them refused,
// rather than end on "Requested device not found".
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const UI = path.join(__dirname, '..', '..', '..', 'shared', 'ui', 'js');

// ---------------------------------------------------------------- fakes

class FakeTrack {
  constructor(kind, label = 'Fake device') { this.kind = kind; this.label = label; this.enabled = true; this.stopped = false; this.onended = null; }
  clone() { return new FakeTrack(this.kind, this.label); }
  stop() { this.stopped = true; }
  // The device went away.
  unplug() { this.stop(); if (this.onended) this.onended(); }
}

class FakeStream {
  constructor(tracks = []) { this.tracks = tracks; this.id = 'stream'; }
  getTracks() { return this.tracks; }
  getAudioTracks() { return this.tracks.filter((x) => x.kind === 'audio'); }
  getVideoTracks() { return this.tracks.filter((x) => x.kind === 'video'); }
}

class FakeNode {
  constructor() { this.gain = { value: 1 }; this.fftSize = 512; }
  connect(n) { return n; }
  disconnect() {}
  getFloatTimeDomainData(buf) { buf.fill(0); }
}

class FakeAudioContext {
  constructor() { this.destination = new FakeNode(); }
  createGain() { return new FakeNode(); }
  createAnalyser() { return new FakeNode(); }
  createMediaStreamSource() { return new FakeNode(); }
  resume() { return Promise.resolve(); }
  close() { return Promise.resolve(); }
}

// A sender with its encodings (one for audio, the simulcast layers for
// video), each active until set otherwise, as in the engine.
class FakeSender {
  constructor(encodings) { this.track = null; this.encodings = (encodings || [{}]).map((e) => ({ ...e, active: true })); this.sets = 0; }
  createEncodedStreams() { return { readable: { pipeThrough: () => ({ pipeTo: () => Promise.resolve() }) }, writable: {} }; }
  async replaceTrack(track) { this.track = track; }
  getParameters() { return { transactionId: 't', encodings: this.encodings.map((e) => ({ ...e })) }; }
  async setParameters(p) { this.encodings = p.encodings.map((e) => ({ ...e })); this.sets++; }
  // Whether the engine encodes and sends anything on it.
  get live() { return !!this.track && this.encodings.some((e) => e.active); }
  get activeRids() { return Array.from(this.encodings.filter((e) => e.active), (e) => e.rid); }
}

// A peer connection whose connection state the test drives.
class FakePC {
  constructor() { this.transceivers = []; this.connectionState = 'new'; this.iceGatheringState = 'complete'; this.onconnectionstatechange = null; FakePC.last = this; }
  addTransceiver(_media, init = {}) { const tr = { mid: String(this.transceivers.length), sender: new FakeSender(init.sendEncodings) }; this.transceivers.push(tr); return tr; }
  async createOffer() { return { type: 'offer', sdp: 'v=0' }; }
  async setLocalDescription(d) { this.localDescription = d; }
  async setRemoteDescription(d) { this.remoteDescription = d; }
  addEventListener() {}
  removeEventListener() {}
  close() { this.connectionState = 'closed'; }
  set(state) { this.connectionState = state; if (this.onconnectionstatechange) this.onconnectionstatechange(); }
  // Inbound statistics the test sets (an array of stats objects).
  async getStats() { return { forEach: (fn) => (this.stats || []).forEach(fn) }; }
}

class DOMException extends Error {
  constructor(message, name) { super(message); this.name = name; }
}

// Media devices: which kinds exist (or are refused, with that error's name).
function fakeDevices(have) {
  const listeners = new Set();
  return {
    have,
    asked: [],
    async getUserMedia(c) {
      const kind = c.audio ? 'audio' : 'video';
      const id = c[kind] && c[kind].deviceId ? c[kind].deviceId.exact : '';
      this.asked.push({ kind, id, constraints: c[kind] });
      const v = have[kind];
      if (v !== true && !(v && v.ids && v.ids.includes(id || 'default'))) {
        throw new DOMException(typeof v === 'string' && v === 'NotAllowedError' ? 'Permission denied' : 'Requested device not found',
          typeof v === 'string' ? v : 'NotFoundError');
      }
      const track = new FakeTrack(kind);
      this.lastTrack = track;
      return new FakeStream([track]);
    },
    async enumerateDevices() { return []; },
    // A shared screen, with its sound if asked for.
    async getDisplayMedia(c) {
      this.display = c;
      return new FakeStream(c.audio ? [new FakeTrack('video'), new FakeTrack('audio')] : [new FakeTrack('video')]);
    },
    addEventListener(name, fn) { if (name === 'devicechange') listeners.add(fn); },
    removeEventListener(name, fn) { if (name === 'devicechange') listeners.delete(fn); },
    change() { for (const fn of listeners) fn(); },
    listeners,
  };
}

const ANSWER = 'v=0\r\na=candidate:1 1 udp 2130706431 203.0.113.7 7000 typ host\r\n';

function load(devices) {
  const timers = [];
  const ctx = vm.createContext({
    console, TextEncoder, Uint8Array, Float32Array, BigInt, Math, Map, Set, WeakMap, Promise, Error, Object, JSON,
    crypto: globalThis.crypto,
    performance: { now: () => 0 },
    navigator: { mediaDevices: devices },
    AudioContext: FakeAudioContext,
    RTCPeerConnection: FakePC,
    MediaStream: FakeStream,
    Audio: class { play() { return Promise.resolve(); } },
    // Keeps its transformer, for tests that push frames through it by hand.
    TransformStream: class { constructor(t) { this.transformer = t; } },
    DOMException,
    atob: (s) => Buffer.from(s, 'base64').toString('binary'),
    // Timers the test fires by hand (the ICE watchdog), never on their own.
    setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    setInterval: () => 0,
    clearTimeout: () => {},
    clearInterval: () => {},
    t: (s, vars) => s.replace(/\{(\w+)\}/g, (_m, k) => String(vars[k])),
  });
  ctx.module = { exports: {} };
  vm.runInContext(fs.readFileSync(path.join(UI, 'sframe.js'), 'utf8') + '\nglobalThis.SFrame = SFrame;', ctx);
  ctx.module = { exports: {} };
  vm.runInContext(fs.readFileSync(path.join(UI, 'call.js'), 'utf8'), ctx);
  return { ...ctx.module.exports, timers };
}

function session(extra = {}) {
  return {
    participant: 'aa', slot: 1, epoch: 1, key: Buffer.alloc(32, 7).toString('base64'),
    participants: [{ participant: 'aa', member: 'me', slot: 1 }, { participant: 'bb', member: 'them', slot: 2 }],
    perms: { audio: true, video: true, screen: true, vad: true }, mode: 'vad', vadThreshold: -50, mic: '', camera: '', speaker: '',
    ...extra,
  };
}

async function started(have, extra) {
  const devices = fakeDevices(have);
  const mod = load(devices);
  const signals = [];
  const signal = async (kind, ...args) => { signals.push([kind, ...args]); return kind === 'join' ? ANSWER : null; };
  const call = new mod.Call(session(extra), signal, () => {});
  const problems = [];
  call.onProblem = (p) => problems.push(p);
  await call.start();
  return { call, devices, signals, problems, mod };
}

// ---------------------------------------------------------------- tests

test('device errors are sorted into what the user can do about them', () => {
  const { mediaProblem } = load(fakeDevices({}));
  assert.equal(mediaProblem({ name: 'NotFoundError' }), 'none');
  assert.equal(mediaProblem({ name: 'OverconstrainedError' }), 'none');
  assert.equal(mediaProblem({ name: 'NotAllowedError' }), 'denied');
  assert.equal(mediaProblem({ name: 'SecurityError' }), 'denied');
  assert.equal(mediaProblem({ name: 'NotReadableError' }), 'busy');
  assert.equal(mediaProblem({ name: 'TypeError' }), 'failed');
  assert.equal(mediaProblem(null), 'failed');
});

test('the relay media address comes from its answer', () => {
  const { relayMediaAddress } = load(fakeDevices({}));
  assert.equal(relayMediaAddress(ANSWER), '203.0.113.7:7000');
  assert.equal(relayMediaAddress('a=candidate:1 1 udp 1 2001:db8::1 7000 typ host'), '[2001:db8::1]:7000');
  assert.equal(relayMediaAddress('v=0'), '');
});

// As Chromium offers: an audio level among the header extensions, Opus at
// a variable bitrate.
const ENGINE_OFFER = [
  'v=0', 'a=extmap-allow-mixed', 'm=audio 9 UDP/TLS/RTP/SAVPF 111 63 0',
  'a=extmap:1 urn:ietf:params:rtp-hdrext:ssrc-audio-level',
  'a=extmap:2/sendrecv http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time',
  'a=extmap:3 http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01',
  'a=extmap:4 urn:ietf:params:rtp-hdrext:sdes:mid',
  'a=extmap:5/recvonly urn:ietf:params:rtp-hdrext:csrc-audio-level',
  'a=rtpmap:111 opus/48000/2', 'a=fmtp:111 minptime=10;useinbandfec=1;usedtx=1;cbr=0;maxaveragebitrate=510000',
  'a=rtpmap:63 red/48000/2', 'a=fmtp:63 111/111', 'a=rtpmap:0 PCMU/8000',
  'm=video 9 UDP/TLS/RTP/SAVPF 96', 'a=extmap:13 urn:3gpp:video-orientation',
  'a=extmap:10 urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id', 'a=rtpmap:96 VP8/90000', '',
].join('\r\n');

test('what the relay may learn is enforced on every description: no audio level, constant bitrate voice', () => {
  const { hardenSdp, CALL } = load(fakeDevices({}));
  const out = hardenSdp(ENGINE_OFFER);
  assert.ok(!out.includes('audio-level'), out);
  assert.ok(!out.includes('abs-send-time') && !out.includes('video-orientation'), out);
  for (const keep of ['a=extmap-allow-mixed', 'a=extmap:3 http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01',
    'a=extmap:4 urn:ietf:params:rtp-hdrext:sdes:mid', 'a=extmap:10 urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id', 'a=fmtp:63 111/111']) {
    assert.ok(out.split('\r\n').includes(keep), keep);
  }
  assert.ok(out.includes(`a=fmtp:111 minptime=10;useinbandfec=1;cbr=1;usedtx=0;maxaveragebitrate=${CALL.OPUS_BITRATE}\r\n`), out);
  // An Opus format without parameters gets them.
  const bare = hardenSdp('m=audio 9 UDP/TLS/RTP/SAVPF 109\r\na=rtpmap:109 OPUS/48000/2\r\na=sendonly\r\n');
  assert.equal(bare, `m=audio 9 UDP/TLS/RTP/SAVPF 109\r\na=rtpmap:109 OPUS/48000/2\r\na=fmtp:109 cbr=1;usedtx=0;maxaveragebitrate=${CALL.OPUS_BITRATE}\r\na=sendonly\r\n`);
  assert.equal(hardenSdp(out), out);
});

test('the offer, the relay\'s answer and the relay\'s later offers are all hardened', async () => {
  const devices = fakeDevices({ audio: true });
  const mod = load(devices);
  const offer = FakePC.prototype.createOffer;
  FakePC.prototype.createOffer = async () => ({ type: 'offer', sdp: ENGINE_OFFER });
  FakePC.prototype.createAnswer = async () => ({ type: 'answer', sdp: ENGINE_OFFER });
  try {
    const signal = async (kind) => (kind === 'join' ? ANSWER + ENGINE_OFFER.slice('v=0\r\n'.length) : null);
    const call = new mod.Call(session(), signal, () => {});
    await call.start();
    const pc = FakePC.last;
    for (const d of [pc.localDescription, pc.remoteDescription]) {
      assert.ok(!d.sdp.includes('audio-level') && d.sdp.includes('cbr=1;usedtx=0'), d.sdp);
    }
    await call.onRemoteOffer(ENGINE_OFFER);
    for (const d of [pc.localDescription, pc.remoteDescription]) {
      assert.ok(!d.sdp.includes('audio-level') && d.sdp.includes('cbr=1;usedtx=0'), d.sdp);
    }
    call.close();
  } finally {
    FakePC.prototype.createOffer = offer;
    delete FakePC.prototype.createAnswer;
  }
});

test('joining again alone (same epoch and slot, new key) sends; the same key id and key never twice', async () => {
  // One page: the set of used key ids lives as long as the page does.
  const mod = load(fakeDevices({ audio: true }));
  const signal = async (kind) => (kind === 'join' ? ANSWER : null);
  const errors = [];
  const origError = console.error;
  console.error = (...a) => errors.push(a.join(' '));
  try {
    const first = new mod.Call(session(), signal, () => {});
    await first.start();
    assert.ok(first.sframe.send, 'the first call sends');
    first.close();
    // Left and joined again, alone: the new call is at epoch 1 again, slot 1, under a new key.
    const again = new mod.Call(session({ key: Buffer.alloc(32, 9).toString('base64') }), signal, () => {});
    await again.start();
    assert.ok(again.sframe.send, 'a new key under the same key id sends');
    again.close();
    assert.deepEqual(errors, []);
    // The same key id and the same key again (a server that didn't rotate): refused.
    const reused = new mod.Call(session(), signal, () => {});
    await reused.start();
    assert.equal(reused.sframe.send, null);
    assert.equal(errors.length, 1);
  } finally {
    console.error = origError;
  }
});

test('no microphone: the call starts, listening, and nothing is sent', async () => {
  const { call, signals } = await started({ audio: 'NotFoundError' });
  assert.equal(call.micState, 'none');
  assert.equal(call.mic, null);
  assert.equal(call.transmitting(), false);
  assert.equal(call.tx.audio.sender.track, null);
  assert.ok(signals.some((s) => s[0] === 'join'), 'joined the relay');
  assert.equal(call.phase, 'connecting');
  FakePC.last.set('connected');
  assert.equal(call.phase, 'connected');
  assert.equal(call.debug().mic, 'none');
});

test('microphone refused: the call starts, listening, and says it was refused', async () => {
  const { call } = await started({ audio: 'NotAllowedError' });
  assert.equal(call.micState, 'denied');
  assert.equal(call.transmitting(), false);
});

test('a microphone plugged in mid-call is picked up', async () => {
  const { call, devices } = await started({ audio: 'NotFoundError' });
  assert.equal(devices.listeners.size, 1, 'listening for device changes');
  devices.have.audio = true;
  devices.change();
  await new Promise((r) => setImmediate(r));
  assert.equal(call.micState, 'ok');
  assert.ok(call.mic && call.tx.audio.sender.track, 'the new microphone is sent');
  call.vadOpen = true;
  assert.equal(call.transmitting(), true);
});

test('a refused microphone is not asked for again on every device change, only on Try again', async () => {
  const { call, devices } = await started({ audio: 'NotAllowedError' });
  const before = devices.asked.length;
  devices.change();
  await new Promise((r) => setImmediate(r));
  assert.equal(devices.asked.length, before);
  devices.have.audio = true;
  await call.retryMic();
  assert.equal(call.micState, 'ok');
});

test('Try again with still no microphone throws words for a toast and keeps the call', async () => {
  const { call } = await started({ audio: 'NotFoundError' });
  await assert.rejects(call.retryMic(), /No microphone found/);
  assert.equal(call.micState, 'none');
  assert.equal(call.closed, false);
});

test('a chosen microphone that is gone falls back to the default one', async () => {
  const { call, devices } = await started({ audio: { ids: ['default'] } }, { mic: 'usb-headset' });
  assert.equal(call.micState, 'ok');
  assert.deepEqual(devices.asked.map((a) => a.id), ['usb-headset', '']);
  assert.equal(call.s.mic, '');
});

test('an unplugged microphone leaves the call listening and tries another', async () => {
  const { call, devices } = await started({ audio: true });
  assert.equal(call.micState, 'ok');
  devices.have.audio = 'NotFoundError';
  call.mic.unplug();
  await new Promise((r) => setImmediate(r));
  assert.equal(call.micState, 'none');
  assert.equal(call.transmitting(), false);
  devices.have.audio = true;
  devices.change();
  await new Promise((r) => setImmediate(r));
  assert.equal(call.micState, 'ok');
});

test('no camera: turning it on fails in words, the call goes on with video off', async () => {
  const { call, signals } = await started({ audio: true, video: 'NotFoundError' });
  await assert.rejects(call.setCamera(true), /No camera found/);
  assert.equal(call.local.camera, undefined);
  assert.equal(call.closed, false);
  const states = signals.filter((s) => s[0] === 'state');
  assert.equal(states.at(-1)[1].camera, false);
});

test('an unplugged camera turns the camera off and says so', async () => {
  const { call, devices, problems } = await started({ audio: true, video: true });
  await call.setCamera(true);
  assert.ok(call.local.camera);
  devices.lastTrack.unplug();
  await new Promise((r) => setImmediate(r));
  assert.equal(call.local.camera, undefined);
  assert.deepEqual(problems.map((p) => p.kind), ['camera-gone']);
});

test('a listen-only member has no microphone state to fix', async () => {
  const { call, devices } = await started({ audio: true }, { perms: { audio: false, video: false, screen: false, vad: true } });
  assert.equal(call.micState, 'off');
  assert.equal(devices.asked.length, 0);
});

test('a connection that never comes up is UDP blocked, with the relay address, and not retried', async () => {
  const { call, signals, problems, mod } = await started({ audio: true });
  const watchdog = mod.timers.find((x) => x.ms === mod.CALL.ICE_TIMEOUT_MS);
  assert.ok(watchdog, 'an ICE watchdog is set');
  FakePC.last.set('checking');
  watchdog.fn();
  assert.equal(call.phase, 'failed');
  assert.deepEqual(JSON.parse(JSON.stringify(problems)), [{ kind: 'udp', address: '203.0.113.7:7000' }]);
  assert.ok(!signals.some((s) => s[0] === 'failed'), "a blocked path isn't reported for reconnecting (that rekeys everyone)");
});

test('ICE failing before it ever connected is the same UDP problem', async () => {
  const { call, problems, signals } = await started({ audio: true });
  FakePC.last.set('failed');
  assert.equal(call.phase, 'failed');
  assert.equal(problems[0].kind, 'udp');
  assert.ok(!signals.some((s) => s[0] === 'failed'));
});

test('a connection that was up and fails is reconnecting, through Python', async () => {
  const { call, problems, signals, mod } = await started({ audio: true });
  FakePC.last.set('connected');
  FakePC.last.set('disconnected');
  assert.equal(call.phase, 'reconnecting');
  FakePC.last.set('failed');
  assert.equal(call.phase, 'reconnecting');
  assert.ok(signals.some((s) => s[0] === 'failed'));
  mod.timers.find((x) => x.ms === mod.CALL.ICE_TIMEOUT_MS).fn();
  assert.deepEqual(problems, [], 'the watchdog does nothing once connected');
});

test('closing stops listening for devices', async () => {
  const { call, devices } = await started({ audio: true });
  call.close();
  assert.equal(devices.listeners.size, 0);
});

// ---------------------------------------------------------------- talking modes

test('studio mode: the microphone has no processing and no threshold, but silence is silence', async () => {
  const { call, devices } = await started({ audio: true }, { mode: 'studio' });
  const c = devices.asked.find((a) => a.kind === 'audio').constraints;
  assert.deepEqual([c.echoCancellation, c.noiseSuppression, c.autoGainControl], [false, false, false]);
  assert.equal(call.debug().processing, false);
  // Silence on the microphone (the fake analyser reads zeros, as a muted
  // interface gives): not talking, and not shown as talking.
  call.tick();
  assert.equal(call.transmitting(), false);
  assert.equal(call.local.audio.enabled, false);
  assert.equal(call.speaking(call.s.participant), false);
  // A quiet signal, under the voice activity threshold: open, untouched.
  call.micAnalyser.getFloatTimeDomainData = (buf) => buf.fill(0.001); // -60 dBFS
  call.tick();
  assert.equal(call.vadOpen, false);
  assert.equal(call.transmitting(), true);
  assert.equal(call.local.audio.enabled, true);
  // Muted is still muted.
  await call.setSelfMute(true);
  assert.equal(call.transmitting(), false);
});

test('voice activity: processing on, and the gate closed on silence', async () => {
  const { call, devices } = await started({ audio: true });
  const c = devices.asked.find((a) => a.kind === 'audio').constraints;
  assert.deepEqual([c.echoCancellation, c.noiseSuppression, c.autoGainControl], [true, true, true]);
  call.tick();
  assert.equal(call.transmitting(), false);
  assert.equal(call.local.audio.enabled, false);
});

test('studio without Use Voice Activity is push to talk, with processing', async () => {
  const { call, devices } = await started({ audio: true }, { mode: 'studio', perms: { audio: true, video: true, screen: true, vad: false } });
  assert.equal(call.mode(), 'ptt');
  assert.equal(devices.asked.find((a) => a.kind === 'audio').constraints.noiseSuppression, true);
  assert.equal(call.transmitting(), false);
  call.setPushToTalk(true);
  assert.equal(call.transmitting(), true);
});

// ---------------------------------------------------------------- sending only what's taken

const W = (audio, camera = 0, layers = [], screen = 0, screenAudio = 0) => [
  { kind: 'audio', receivers: audio, layers: [] },
  { kind: 'camera', receivers: camera, layers },
  { kind: 'screen', receivers: screen, layers: screen ? ['m'] : [] },
  { kind: 'screen-audio', receivers: screenAudio, layers: [] },
];

test('a sender with nobody watching stops encoding, and starts again when someone watches', async () => {
  const { call } = await started({ audio: true, video: true });
  await call.setCamera(true);
  const cam = call.tx.camera.sender;
  const mic = call.tx.audio.sender;
  // Until the relay says, everything goes (a relay that never says).
  assert.ok(cam.live && mic.live);
  // Nobody watches the camera, nobody hears: nothing encoded or sent, but
  // the camera itself stays on (its own preview, everyone's flag).
  await call.onWatchers(W(0, 0));
  assert.equal(cam.live, false);
  assert.equal(mic.live, false);
  assert.ok(call.local.camera && !call.local.camera.stopped);
  assert.deepEqual({ ...call.debug().sending }, { audio: false, camera: false, screen: false, 'screen-audio': false });
  // Someone watches small: only the low layer. Someone hears: audio on.
  await call.onWatchers(W(1, 1, ['l']));
  assert.deepEqual(cam.activeRids, ['l']);
  assert.ok(mic.live);
  // Another watches large: the layers they asked for.
  await call.onWatchers(W(1, 2, ['l', 'h']));
  assert.deepEqual(cam.activeRids, ['l', 'h']);
  // Nothing changed: the encoders aren't touched again.
  const sets = cam.sets;
  await call.onWatchers(W(1, 2, ['l', 'h']));
  assert.equal(cam.sets, sets);
  // Everyone stops watching, and deafens: paused again.
  await call.onWatchers(W(0, 0));
  assert.equal(cam.live, false);
  assert.equal(mic.live, false);
});

function fakeReceiver() {
  return { createEncodedStreams: () => ({ readable: { pipeThrough: () => ({ pipeTo: () => Promise.resolve() }) }, writable: {} }) };
}

test('local mute stops playout and counts as not listening; deafen and watched screens too', async () => {
  const { call, signals } = await started({ audio: true });
  const listens = () => signals.filter((s) => s[0] === 'listen');
  const lastListen = () => JSON.parse(JSON.stringify(listens().at(-1)[1]));
  // At the start: every microphone, no screen sound.
  assert.deepEqual(lastListen(), { deaf: false, noAudio: [], screenAudio: [] });
  call.onTrack({ streams: [{ id: 'bb' }], track: Object.assign(new FakeTrack('audio'), { id: 'audio' }), receiver: fakeReceiver() });
  const voice = call.remote.get('bb').audio.gain.gain;
  assert.equal(voice.value, 1);
  call.setLocalMute('them', true);
  assert.equal(voice.value, 0, 'not played');
  assert.deepEqual(lastListen(), { deaf: false, noAudio: ['bb'], screenAudio: [] }, 'not received');
  // A volume set meanwhile doesn't unmute.
  call.setVolume('them', 1.5);
  assert.equal(voice.value, 0);
  call.setLocalMute('them', false);
  assert.equal(voice.value, 1.5);
  assert.deepEqual(lastListen().noAudio, []);
  // Watching their screen asks for its sound; muting the stream doesn't.
  call.onTrack({ streams: [{ id: 'bb' }], track: Object.assign(new FakeTrack('audio'), { id: 'screen-audio' }), receiver: fakeReceiver() });
  call.watch('bb', 'screen', 800);
  assert.deepEqual(lastListen().screenAudio, ['bb']);
  call.setStreamMuted('them', true);
  assert.deepEqual(lastListen().screenAudio, []);
  assert.equal(call.remote.get('bb')['screen-audio'].gain.gain.value, 0);
  call.setStreamMuted('them', false);
  call.setStreamVolume('them', 0.5);
  assert.equal(call.remote.get('bb')['screen-audio'].gain.gain.value, 0.5);
  call.unwatch('bb', 'screen');
  assert.deepEqual(lastListen().screenAudio, []);
  // Deafened: nothing wanted; the output level comes back after.
  call.setOutputVolume(0.8);
  await call.setSelfDeaf(true);
  assert.equal(lastListen().deaf, true);
  assert.equal(call.master.gain.value, 0);
  await call.setSelfDeaf(false);
  assert.equal(lastListen().deaf, false);
  assert.equal(call.master.gain.value, 0.8);
  // The same state isn't sent twice.
  const n = listens().length;
  call.sendListen();
  assert.equal(listens().length, n);
});

test("a screen share's sound is captured and sent only when shared", async () => {
  const { call, devices } = await started({ audio: true, video: true }, { screenAudio: false });
  await call.setScreen(true);
  assert.equal(devices.display.audio, false);
  assert.equal(call.tx['screen-audio'].sender.track, null);
  await call.setScreen(false);
  await call.setScreenAudio(true);
  await call.setScreen(true);
  assert.ok(devices.display.audio);
  assert.ok(call.tx['screen-audio'].sender.track);
  // Turned off mid-share: stopped at once.
  const track = call.local['screen-audio'];
  assert.equal(await call.setScreenAudio(false), false);
  assert.ok(track.stopped);
  assert.equal(call.tx['screen-audio'].sender.track, null);
});

// A frame of one kind can't be played as another kind of the same sender:
// the kind is bound into the SFrame metadata.
test('video loss is counted before retransmissions repair it, and only on enough packets', async () => {
  const { call, signals } = await started({ audio: true });
  const layerOf = () => signals.filter((x) => x[0] === 'layer').at(-1)[3];
  const video = (f) => [{ type: 'inbound-rtp', kind: 'video', ...f }];
  call.watch('bb', 'camera', 1000);
  await call.chooseLayers();
  assert.equal(layerOf(), 'h');
  // First sample: nothing to compare with yet.
  call.pc.stats = video({ packetsLost: 0, packetsReceived: 1000, retransmittedPacketsReceived: 0 });
  await call.chooseLayers();
  assert.equal(layerOf(), 'h');
  // 150 of 1010 packets were dropped; 140 of them arrived again and so no
  // longer count as lost in the engine's running total.
  call.pc.stats = video({ packetsLost: 10, packetsReceived: 2000, retransmittedPacketsReceived: 140 });
  await call.chooseLayers();
  assert.ok(Math.abs(call.loss - 150 / 1010) < 0.001, call.loss);
  assert.equal(layerOf(), 'm');
  // The last 10 arrive too: the running total of lost packets goes down,
  // which is not negative loss, and not loss at all.
  call.pc.stats = video({ packetsLost: 0, packetsReceived: 3000, retransmittedPacketsReceived: 150 });
  await call.chooseLayers();
  assert.equal(call.loss, 0);
  assert.equal(layerOf(), 'm');
  // Too few packets to judge: the estimate is left alone.
  call.pc.stats = video({ packetsLost: 5, packetsReceived: 3005, retransmittedPacketsReceived: 150 });
  await call.chooseLayers();
  assert.equal(call.loss, 0);
  // An engine without retransmission counts: NACKs sent count instead.
  const { call: c2, signals: s2 } = await started({ audio: true });
  c2.watch('bb', 'camera', 1000);
  c2.pc.stats = video({ packetsLost: 0, packetsReceived: 1000, nackCount: 0 });
  await c2.chooseLayers();
  c2.pc.stats = video({ packetsLost: 2, packetsReceived: 2000, nackCount: 100 });
  await c2.chooseLayers();
  assert.ok(c2.loss > 0.05, c2.loss);
  assert.equal(s2.filter((x) => x[0] === 'layer').at(-1)[3], 'm');
});

test('frames are bound to their kind of media', async () => {
  const { call, mod } = await started({ audio: true });
  // Receive as participant bb (slot 2) under the call key: a sender context for bb.
  const bb = new (load(fakeDevices({})).Call)(session({ participant: 'bb', slot: 2 }), async (k) => (k === 'join' ? ANSWER : null), () => {});
  await bb.setKeys(1, session().key, 2);
  const frame = (bytes, type) => ({ data: Uint8Array.from(bytes).buffer, type });
  const seal = async (kind, bytes, type = 'delta') => {
    const out = [];
    await bb.senderTransform(kind).transformer.transform(frame(bytes, type), { enqueue: (f) => out.push(f) });
    return new Uint8Array(out[0].data);
  };
  const open = async (kind, data, type = 'delta') => {
    const out = [];
    await call.receiverTransform({ kind, participant: 'bb' }).transformer.transform(frame(data, type), { enqueue: (f) => out.push(f) });
    return out.length ? Array.from(new Uint8Array(out[0].data)) : null;
  };
  const voice = await seal('audio', [1, 2, 3, 4]);
  const screenSound = await seal('screen-audio', [5, 6, 7, 8]);
  const camera = await seal('camera', [9, 9, 9, 1, 2, 3]);
  assert.equal(await open('screen-audio', voice), null);
  assert.equal(await open('audio', screenSound), null);
  assert.equal(await open('screen', camera), null);
  assert.equal(call.stats.failed, 3);
  assert.deepEqual(await open('audio', voice), [1, 2, 3, 4]);
  assert.deepEqual(await open('screen-audio', screenSound), [5, 6, 7, 8]);
  assert.deepEqual(await open('camera', camera), [9, 9, 9, 1, 2, 3]);
  // And each opens once.
  assert.equal(await open('audio', voice), null);
  assert.ok(mod.frameMetadata('audio', new Uint8Array(0)).length === 1);
  assert.throws(() => mod.frameMetadata('other', new Uint8Array(0)), /unknown media kind/);
});
