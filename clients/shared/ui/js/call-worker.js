// SFrame for calls in a worker (RTCRtpScriptTransform: Firefox, Safari and
// other browsers with the standard encoded transform). call.js runs the same
// transforms in the page where the engine has createEncodedStreams instead.
// The page sends this worker every key it installs or drops, which epochs
// are current with each participant's slot, and which participant each
// receiver carries; it never decrypts on its own.
'use strict';

importScripts('sframe.js');

const sframe = new SFrame.Context();
let consts = { kindTags: {}, clearKey: 10, clearDelta: 3, keyWait: 3000 };
let epochs = []; // [{epoch: BigInt, slots: Map(participant -> slot)}]
let firstEpoch = null;
let joinMode = false; // a DM call: an epoch per join, in no order
const keyMissing = new Set();
let keyWaiters = [];
const binds = new Map(); // receiver id -> {kind, participant}
let stats = fresh();

function fresh() {
  return { encrypted: 0, decrypted: 0, failed: 0, beforeKey: 0, why: {}, from: {}, sentFrames: { audio: 0, camera: 0, screen: 0 } };
}

function frameMetadata(kind, clear) {
  const tag = consts.kindTags[kind];
  if (!tag) throw new Error('unknown media kind'); // i18n: not shown (drop reason, counted in the call's stats)
  const out = new Uint8Array(1 + clear.length);
  out[0] = tag;
  out.set(clear, 1);
  return out;
}

function clearBytes(frame, kind) {
  if (kind === 'audio' || kind === 'screen-audio') return 0;
  return frame.type === 'key' ? consts.clearKey : consts.clearDelta;
}

function keyArrival(epoch) {
  return new Promise((resolve) => {
    const w = { epoch, resolve };
    keyWaiters.push(w);
    setTimeout(() => { keyWaiters = keyWaiters.filter((x) => x !== w); resolve(); }, consts.keyWait);
  });
}

function sender(kind) {
  return new TransformStream({
    async transform(frame, controller) {
      if (!sframe.send) return; // no key yet: send nothing
      const data = new Uint8Array(frame.data);
      const n = Math.min(clearBytes(frame, kind), data.length);
      const clear = data.subarray(0, n);
      const out = await sframe.protect(data.subarray(n), frameMetadata(kind, clear));
      const buf = new Uint8Array(n + out.length);
      buf.set(clear, 0);
      buf.set(out, n);
      frame.data = buf.buffer;
      stats.encrypted++;
      const k = kind === 'screen-audio' ? 'screen' : kind;
      if (stats.sentFrames[k] !== undefined) stats.sentFrames[k]++;
      controller.enqueue(frame);
    },
  });
}

// As call.js receiverTransform: frames on a participant's track must be
// under that participant's key id (its slot in the frame's epoch).
function receiver(id) {
  return new TransformStream({
    async transform(frame, controller) {
      const bind = binds.get(id);
      if (!bind) return;
      const { kind, participant } = bind;
      const data = new Uint8Array(frame.data);
      if (data.length === 0) { controller.enqueue(frame); return; }
      const n = Math.min(clearBytes(frame, kind), data.length);
      try {
        const { kid } = SFrame.decodeHeader(data.subarray(n));
        const epoch = kid >> 8n;
        let ep = epochs.find((e) => e.epoch === epoch);
        if (!ep && !keyMissing.has(epoch) && (joinMode || !epochs.length || epoch > epochs[epochs.length - 1].epoch)) {
          await keyArrival(epoch);
          ep = epochs.find((e) => e.epoch === epoch);
          if (!ep) keyMissing.add(epoch);
        }
        if (!ep) {
          if (firstEpoch !== null && epoch < firstEpoch) { stats.beforeKey++; return; }
          throw new Error('unknown key id'); // i18n: not shown (drop reason, counted in the call's stats)
        }
        const slot = ep.slots.get(participant);
        if (slot === undefined || (kid & 0xffn) !== BigInt(slot)) throw new Error('key id of another participant'); // i18n: not shown (drop reason, counted in the call's stats)
        const pt = await sframe.unprotect(data.subarray(n), frameMetadata(kind, data.subarray(0, n)));
        const buf = new Uint8Array(n + pt.length);
        buf.set(data.subarray(0, n), 0);
        buf.set(pt, n);
        frame.data = buf.buffer;
        stats.decrypted++;
        stats.from[participant] = (stats.from[participant] || 0) + 1;
        controller.enqueue(frame);
      } catch (e) {
        stats.failed++;
        const why = (e && e.message) || 'decrypt failed'; // i18n: not shown (stats key)
        stats.why[why] = (stats.why[why] || 0) + 1;
      }
    },
  });
}

self.onrtctransform = (event) => {
  const tf = event.transformer;
  const o = tf.options || {};
  const stream = o.side === 'send' ? sender(o.kind) : receiver(o.id);
  // A 'bind' (a receiver reused for someone else) can arrive before this
  // event: it's the newer of the two.
  if (o.side !== 'send' && !binds.has(o.id)) binds.set(o.id, { kind: o.kind, participant: o.participant });
  tf.readable.pipeThrough(stream).pipeTo(tf.writable).catch(() => {});
};

self.onmessage = async (e) => {
  const m = e.data;
  switch (m.type) {
    case 'init': consts = m.consts; break;
    case 'sendKey': await sframe.setSendKey(BigInt(m.kid), m.key); break;
    case 'sendOff': sframe.send = null; break;
    case 'recvKey': await sframe.setRecvKey(BigInt(m.kid), m.key); break;
    case 'dropRecv': sframe.dropRecvKey(BigInt(m.kid)); break;
    case 'epochs':
      epochs = m.epochs.map((x) => ({ epoch: BigInt(x.epoch), slots: new Map(x.slots) }));
      firstEpoch = m.firstEpoch === null ? null : BigInt(m.firstEpoch);
      joinMode = !!m.joinMode;
      keyWaiters = keyWaiters.filter((w) => {
        if (joinMode || (epochs.length && w.epoch <= epochs[epochs.length - 1].epoch)) { w.resolve(); return false; }
        return true;
      });
      break;
    case 'bind': binds.set(m.id, { kind: m.kind, participant: m.participant }); break;
    default: break;
  }
};

// The page shows the call's counters (Copy details): sent as they change.
setInterval(() => {
  if (!stats.encrypted && !stats.decrypted && !stats.failed && !stats.beforeKey) return;
  self.postMessage({ type: 'stats', stats });
  stats = fresh();
}, 1000);
