// Calls in the webview (docs/protocol.md §12).
//
// Chromium captures, processes (echo cancellation, noise suppression, gain
// control; none in studio mode) and encodes media; every encoded frame is
// end-to-end encrypted with SFrame (sframe.js) through createEncodedStreams
// before it leaves, and decrypted on arrival. Media goes over one WebRTC
// connection to the call's relay, whose SFU forwards it without keys.
// Signaling goes through Python (the page has no network access of its own).
//
// Nothing is sent that nobody takes: the relay says how many receivers each
// kind of this device's media has (CallWatchers), and a kind with none (a
// camera nobody watches, a voice everyone deafened or muted for
// themselves) stops being encoded and sent. In the other direction this
// device asks only for the videos its user watches (watch()) and tells the
// relay which audio it doesn't want (listenState()).
//
// The only secrets this code ever sees are per-call media keys: the call key
// for the current and previous epoch. A sender's SFrame key id is
// epoch << 8 | slot, so every key id is used by one sender under one key, and
// RFC 9605 derives a separate key and salt for each.
'use strict';

const CALL = {
  // Each kind of media is bound into its frames' SFrame metadata (the
  // authenticated data after the header) by this byte, so a frame can't be
  // moved to another kind of the same sender (a screen's sound played as the
  // voice, a screen as the camera). Kinds are the relay's track ids.
  KIND_TAGS: { audio: 1, camera: 2, screen: 3, 'screen-audio': 4 },
  // Frames left in the clear (and authenticated as SFrame metadata) so the SFU
  // can find VP8 keyframes: the VP8 payload header (RFC 7741 §4.3).
  VP8_CLEAR_KEY: 10,
  VP8_CLEAR_DELTA: 3,
  // A new key is installed for receiving at once, and used for sending a
  // little later, so everyone has it before frames under it arrive.
  SEND_KEY_DELAY_MS: 250,
  // Video loss is judged on samples of at least this many packets.
  LOSS_MIN_PACKETS: 20,
  CAMERA: { width: { ideal: 1280 }, height: { ideal: 720 }, frameRate: { ideal: 30, max: 30 } },
  // Simulcast layers, lowest first (rid, scale, max bitrate).
  LAYERS: [
    { rid: 'l', scaleResolutionDownBy: 4, maxBitrate: 150_000 },
    { rid: 'm', scaleResolutionDownBy: 2, maxBitrate: 500_000 },
    { rid: 'h', scaleResolutionDownBy: 1, maxBitrate: 1_500_000 },
  ],
  VAD_HANGOVER_MS: 400,
  // Voice (and a shared screen's sound) is Opus at a constant bitrate,
  // without DTX: every 20 ms packet the same size whatever is said, or
  // whether anything is (hardenSdp).
  OPUS_BITRATE: 32000,
  // The only RTP header extensions negotiated (they are in the clear for
  // the relay): bundling, simulcast and congestion control, none derived
  // from what is said or shown. Never the RFC 6464 audio level (a loudness
  // value on every packet), whatever the engine offers or a relay answers.
  HEADER_EXTENSIONS: [
    'urn:ietf:params:rtp-hdrext:sdes:mid',
    'urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id',
    'urn:ietf:params:rtp-hdrext:sdes:repaired-rtp-stream-id',
    'http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01',
  ],
  // A connection that hasn't come up this long after the relay answered
  // never will: the media path (UDP to the relay) is blocked.
  ICE_TIMEOUT_MS: 15000,
};

// What went wrong with a microphone or camera, from getUserMedia's error:
// 'none' (no such device), 'denied' (the system or the app refused),
// 'busy' (another app holds it, or the system couldn't open it) or
// 'failed' (anything else). docs/user-guide.md "Voice and video" says what
// to do about each.
function mediaProblem(e) {
  const name = e && e.name;
  if (name === 'NotFoundError' || name === 'OverconstrainedError' || name === 'DevicesNotFoundError') return 'none';
  if (name === 'NotAllowedError' || name === 'SecurityError' || name === 'PermissionDeniedError') return 'denied';
  if (name === 'NotReadableError' || name === 'AbortError' || name === 'TrackStartError') return 'busy';
  return 'failed';
}

// The words for a device problem, for the voice bar, settings and toasts.
function mediaProblemText(problem, kind) {
  const mic = kind === 'audio';
  if (problem === 'none') return mic ? t('No microphone found.') : t('No camera found.');
  if (problem === 'denied') {
    return mic ? t("OpenArcanum isn't allowed to use the microphone. Allow it in your system's privacy settings, then try again.")
      : t("OpenArcanum isn't allowed to use the camera. Allow it in your system's privacy settings, then try again.");
  }
  if (problem === 'busy') {
    return mic ? t("The microphone couldn't be opened. Another app may be using it.")
      : t("The camera couldn't be opened. Another app may be using it.");
  }
  return mic ? t("The microphone couldn't be started.") : t("The camera couldn't be started.");
}

// The relay's media address from its answer (its ICE candidates), for the
// "calls need UDP" message: what a firewall has to let through.
function relayMediaAddress(sdp) {
  const m = /^a=candidate:\S+ \d+ udp \d+ (\S+) (\d+) typ /im.exec(sdp || '');
  if (!m) return '';
  return m[1].includes(':') ? `[${m[1]}]:${m[2]}` : `${m[1]}:${m[2]}`;
}

// What the relay can learn from the media's clear parts, enforced on every
// description this page sets, its own and the relay's (a relay could
// otherwise ask for more): only the header extensions in
// CALL.HEADER_EXTENSIONS, and every Opus format at a constant bitrate
// (cbr=1, CALL.OPUS_BITRATE) without DTX (usedtx=0). The engine encodes
// with the format parameters the other side sent, so the relay's are the
// ones that count; ours say the same.
function hardenSdp(sdp) {
  const lines = sdp.split(/\r\n/);
  const opus = new Map(); // payload type -> has a format line
  for (const l of lines) {
    const m = /^a=rtpmap:(\d+) opus\//i.exec(l);
    if (m) opus.set(m[1], false);
  }
  const voice = (pt, params) => {
    const keep = params.split(';').map((p) => p.trim())
      .filter((p) => p && !['cbr', 'usedtx', 'maxaveragebitrate'].includes(p.split('=')[0]));
    return `a=fmtp:${pt} ${[...keep, 'cbr=1', 'usedtx=0', `maxaveragebitrate=${CALL.OPUS_BITRATE}`].join(';')}`;
  };
  const out = [];
  for (const l of lines) {
    const ext = /^a=extmap:\d+(?:\/\w+)? (\S+)/.exec(l);
    if (ext && !CALL.HEADER_EXTENSIONS.includes(ext[1])) continue;
    const fmtp = /^a=fmtp:(\d+) (.*)$/.exec(l);
    if (fmtp && opus.has(fmtp[1])) {
      opus.set(fmtp[1], true);
      out.push(voice(fmtp[1], fmtp[2]));
      continue;
    }
    out.push(l);
  }
  // An Opus format without parameters gets them after its rtpmap line.
  for (const [pt, has] of opus) {
    if (has) continue;
    const i = out.findIndex((l) => l.startsWith(`a=rtpmap:${pt} `));
    out.splice(i + 1, 0, voice(pt, ''));
  }
  return out.join('\r\n');
}

function b64bytes(s) {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function kidFor(epoch, slot) { return (BigInt(epoch) << 8n) | BigInt(slot); }

// The SFrame metadata of a frame: its kind's tag, then the bytes left in the
// clear. Throws for a kind that isn't one of ours.
function frameMetadata(kind, clear) {
  const tag = CALL.KIND_TAGS[kind];
  if (!tag) throw new Error('unknown media kind'); // i18n: not shown (drop reason, counted in the call's stats)
  const out = new Uint8Array(1 + clear.length);
  out[0] = tag;
  out.set(clear, 1);
  return out;
}

// Key ids this page has sent under, each with (a hash of) the key it was
// used with. A new call starts its SFrame counters at zero, so sending again
// under a key id and key used before would reuse nonces; the server rotates
// the key on every (re)join, and this refuses to send if it ever didn't.
// The same key id under a new key is fine (and common: alone in a channel,
// leave and join again, and the new call starts again at epoch 1).
const usedSendKids = new Set();

async function sendKeyTag(kid, key) {
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', key));
  let hex = '';
  for (const b of digest.subarray(0, 16)) hex += b.toString(16).padStart(2, '0');
  return `${kid}:${hex}`;
}

// Microphone level in dBFS from an analyser.
function levelDb(analyser, buf) {
  analyser.getFloatTimeDomainData(buf);
  let sum = 0;
  for (let i = 0; i < buf.length; i++) sum += buf[i] * buf[i];
  const rms = Math.sqrt(sum / buf.length);
  return rms > 0 ? 20 * Math.log10(rms) : -120;
}

class Call {
  // session: what Python sent (see calls.py CallManager._js_session).
  // signal(kind, ...args): async call into Python for relay signaling.
  // onChange(): the UI should redraw.
  constructor(session, signal, onChange) {
    this.s = session;
    this.signal = signal;
    this.onChange = onChange || (() => {});
    this.sframe = new SFrame.Context();
    this.epochs = []; // [{epoch, key}] newest last, at most two
    this.pc = null;
    this.tx = {}; // kind -> transceiver
    this.local = {}; // kind -> MediaStreamTrack being sent
    this.mic = null; // raw microphone track (the sent one is a clone)
    this.remote = new Map(); // participant hex -> {audio: {...}, camera: stream, screen: stream, level}
    // RTCRtpReceiver -> {kind, participant}: whose media it carries at present.
    this.bound = new WeakMap();
    this.ctx = null; // AudioContext
    this.master = null; // GainNode (deafen)
    this.selfMute = false;
    // Priority speaker: off until switched on (and only with the permission).
    this.priorityOn = false;
    this.selfDeaf = false;
    this.ptt = false; // push-to-talk key held
    this.vadOpen = false;
    this.vadUntil = 0;
    this.myLevel = -120;
    this.volumes = Object.assign({}, session.volumes || {}); // member id -> 0..2
    // Members muted on this device only ("Mute for me"): never played, and
    // the relay is told not to send them here.
    this.localMutes = new Set(session.localMutes || []);
    // A screen share's sound, per member, on this device: 0..2, and muted.
    this.streamVolumes = {};
    this.streamMuted = new Set();
    this.outputVolume = session.outputVolume ?? 1; // this device's output, 0..2
    this.layers = new Map(); // `${participant}/${kind}` -> rid asked for
    // What the relay says receives this device's media, per kind:
    // {receivers, layers}; null until it says (then everything is sent).
    this.watchers = null;
    this.lastListen = '';
    this.stats = { encrypted: 0, decrypted: 0, failed: 0, why: {}, from: {}, sentFrames: { audio: 0, camera: 0, screen: 0 } };
    this.closed = false;
    this.timers = [];
    // The microphone: 'off' (listen only), 'starting', 'ok', or what went
    // wrong (mediaProblem). Without one the call goes on: you hear
    // everyone, and the microphone starts when one appears or is chosen.
    this.micState = session.perms && session.perms.audio ? 'starting' : 'off';
    // The connection to the relay: 'connecting', 'connected',
    // 'reconnecting' (it was up and dropped) or 'failed' (see problem).
    this.phase = 'connecting';
    this.everConnected = false;
    this.problem = null; // {kind: 'udp', address} when the phase is 'failed'
    this.onDevices = () => this.devicesChanged().catch(() => {});
    // Relay offers wait for the join answer, then run one at a time.
    this.joined = new Promise((resolve) => { this.markJoined = resolve; });
    this.negotiation = this.joined;
  }

  // ---------------------------------------------------------------- keys

  async setKeys(epoch, keyB64, slot) {
    const key = b64bytes(keyB64);
    if (this.epochs.length && this.epochs[this.epochs.length - 1].epoch === epoch) return;
    // Who held which slot in this epoch: a frame must carry the slot its
    // track's participant held in the frame's epoch.
    this.epochs.push({ epoch, key, slots: new Map(this.s.participants.map((p) => [p.participant, p.slot])) });
    while (this.epochs.length > 2) {
      const old = this.epochs.shift();
      for (const kid of [...this.sframe.recv.keys()]) if (kid >> 8n === BigInt(old.epoch)) this.sframe.dropRecvKey(kid);
    }
    await this.installRecvKeys();
    const first = this.sframe.send === null;
    const apply = async () => {
      if (!this.epochs.some((e) => e.epoch === epoch)) return;
      const kid = kidFor(epoch, slot);
      const tag = await sendKeyTag(kid, key);
      if (usedSendKids.has(tag)) { this.sframe.send = null; console.error('call: key id already used with this key; not sending until the key changes'); return; }
      usedSendKids.add(tag);
      await this.sframe.setSendKey(kid, key);
    };
    if (first) await apply();
    else setTimeout(() => { apply().catch((e) => console.error('call: send key', e)); }, CALL.SEND_KEY_DELAY_MS);
  }

  async installRecvKeys() {
    const newest = this.epochs[this.epochs.length - 1];
    if (newest) newest.slots = new Map(this.s.participants.map((p) => [p.participant, p.slot]));
    for (const { epoch, key, slots } of this.epochs) {
      for (const [participant, slot] of slots) {
        if (participant === this.s.participant) continue;
        const kid = kidFor(epoch, slot);
        if (!this.sframe.recv.has(kid)) await this.sframe.setRecvKey(kid, key);
      }
    }
  }

  // ---------------------------------------------------------------- transforms

  clearBytes(frame, kind) {
    if (kind === 'audio' || kind === 'screen-audio') return 0;
    return frame.type === 'key' ? CALL.VP8_CLEAR_KEY : CALL.VP8_CLEAR_DELTA;
  }

  senderTransform(kind) {
    return new TransformStream({
      transform: async (frame, controller) => {
        if (!this.sframe.send) return; // no key yet: send nothing
        const data = new Uint8Array(frame.data);
        const n = Math.min(this.clearBytes(frame, kind), data.length);
        const clear = data.subarray(0, n);
        const out = await this.sframe.protect(data.subarray(n), frameMetadata(kind, clear));
        const buf = new Uint8Array(n + out.length);
        buf.set(clear, 0);
        buf.set(out, n);
        frame.data = buf.buffer;
        this.stats.encrypted++;
        const k = kind === 'screen-audio' ? 'screen' : kind;
        if (this.stats.sentFrames[k] !== undefined) this.stats.sentFrames[k]++;
        controller.enqueue(frame);
      },
    });
  }

  // Frames on a participant's track must be under that participant's key id
  // (its slot): with one call key for everyone, SFrame alone wouldn't stop a
  // participant sending as another (RFC 9605 §9).
  // `bind` ({kind, participant}) is read per frame: the relay may hand the
  // receiver over again (onTrack).
  receiverTransform(bind) {
    return new TransformStream({
      transform: async (frame, controller) => {
        const { kind, participant } = bind;
        const data = new Uint8Array(frame.data);
        // The engine passes empty frames along (nothing in them to protect).
        if (data.length === 0) { controller.enqueue(frame); return; }
        const n = Math.min(this.clearBytes(frame, kind), data.length);
        try {
          const { kid } = SFrame.decodeHeader(data.subarray(n));
          const ep = this.epochs.find((e) => BigInt(e.epoch) === kid >> 8n);
          if (!ep) throw new Error('unknown key id'); // i18n: not shown (drop reason, counted in the call's stats)
          const slot = ep.slots.get(participant);
          if (slot === undefined || (kid & 0xffn) !== BigInt(slot)) throw new Error('key id of another participant'); // i18n: not shown (drop reason, counted in the call's stats)
          const pt = await this.sframe.unprotect(data.subarray(n), frameMetadata(kind, data.subarray(0, n)));
          const buf = new Uint8Array(n + pt.length);
          buf.set(data.subarray(0, n), 0);
          buf.set(pt, n);
          frame.data = buf.buffer;
          this.stats.decrypted++;
          this.stats.from[participant] = (this.stats.from[participant] || 0) + 1;
          controller.enqueue(frame);
        } catch (e) {
          // Wrong or unknown key, or tampered: dropped (the decoder conceals it).
          this.stats.failed++;
          const why = (e && e.message) || 'decrypt failed'; // i18n: not shown (stats key)
          this.stats.why[why] = (this.stats.why[why] || 0) + 1;
        }
      },
    });
  }

  // ---------------------------------------------------------------- connection

  async start() {
    this.ctx = new AudioContext({ latencyHint: 'interactive' });
    this.master = this.ctx.createGain();
    this.master.gain.value = this.outputVolume;
    this.master.connect(this.ctx.destination);
    // Started from the click on Join; resume in case the engine
    // created it suspended anyway.
    await this.ctx.resume().catch(() => {});
    if (this.s.speaker && this.ctx.setSinkId) await this.ctx.setSinkId(this.s.speaker).catch(() => {});
    await this.setKeys(this.s.epoch, this.s.key, this.s.slot);
    this.pc = new RTCPeerConnection({ encodedInsertableStreams: true, bundlePolicy: 'max-bundle', rtcpMuxPolicy: 'require' });
    // Every transceiver this device might use, up front, so only the relay
    // renegotiates afterwards.
    const kinds = {};
    const add = (kind, media, encodings) => {
      const tr = this.pc.addTransceiver(media, { direction: 'sendonly', sendEncodings: encodings });
      const { readable, writable } = tr.sender.createEncodedStreams();
      readable.pipeThrough(this.senderTransform(kind)).pipeTo(writable).catch(() => {});
      this.tx[kind] = tr;
    };
    add('audio', 'audio');
    add('camera', 'video', CALL.LAYERS.map((l) => ({ ...l })));
    add('screen', 'video', CALL.LAYERS.map((l) => ({ ...l })));
    add('screen-audio', 'audio');
    this.pc.ontrack = (e) => this.onTrack(e);
    this.pc.onconnectionstatechange = () => this.connectionChanged();
    const offer = await this.pc.createOffer();
    await this.pc.setLocalDescription({ type: 'offer', sdp: hardenSdp(offer.sdp) });
    await this.iceGathered();
    for (const [kind, tr] of Object.entries(this.tx)) kinds[tr.mid] = kind;
    const answer = await this.signal('join', this.pc.localDescription.sdp, kinds);
    await this.pc.setRemoteDescription({ type: 'answer', sdp: hardenSdp(answer) });
    this.relayAddress = relayMediaAddress(answer);
    this.markJoined();
    // The relay forwards no screen audio, and every microphone, until told.
    this.sendListen();
    this.timers.push(setTimeout(() => { if (!this.everConnected && !this.closed) this.fail('udp'); }, CALL.ICE_TIMEOUT_MS));
    if (navigator.mediaDevices && navigator.mediaDevices.addEventListener) navigator.mediaDevices.addEventListener('devicechange', this.onDevices);
    // No microphone, or not allowed to use it: the call goes on, listening.
    if (this.s.perms.audio) await this.startMic().catch((e) => this.micFailed(e));
    this.timers.push(setInterval(() => this.tick(), 50));
    this.timers.push(setInterval(() => {
      this.chooseLayers().catch(() => {});
      // Again now and then: an encoder change refused mid-negotiation.
      this.applySending().catch(() => {});
    }, 2000));
    this.onChange();
  }

  connectionChanged() {
    if (!this.pc || this.closed) return;
    const cs = this.pc.connectionState;
    if (cs === 'connected') {
      this.everConnected = true;
      this.phase = 'connected';
    } else if (cs === 'failed') {
      // It was up: the relay or the network dropped; Python reconnects (or
      // moves the call). It never came up: UDP to the relay is blocked, and
      // reconnecting won't change that (each attempt rekeys everyone).
      if (this.everConnected) { this.phase = 'reconnecting'; this.signal('failed').catch(() => {}); } else this.fail('udp');
    } else if (cs === 'disconnected' && this.everConnected) this.phase = 'reconnecting';
    this.onChange();
  }

  // The call can't go on here; voice.js says why and offers to try again.
  fail(kind) {
    if (this.phase === 'failed' || this.closed) return;
    this.phase = 'failed';
    this.problem = { kind, address: this.relayAddress || '' };
    if (this.onProblem) this.onProblem(this.problem);
    this.onChange();
  }

  iceGathered() {
    if (this.pc.iceGatheringState === 'complete') return Promise.resolve();
    return new Promise((resolve) => {
      const done = () => { if (this.pc.iceGatheringState === 'complete') { this.pc.removeEventListener('icegatheringstatechange', done); resolve(); } };
      this.pc.addEventListener('icegatheringstatechange', done);
      setTimeout(resolve, 3000);
    });
  }

  // The relay offers when tracks come and go.
  onRemoteOffer(sdp) {
    this.negotiation = this.negotiation.then(async () => {
      if (this.closed) return;
      await this.pc.setRemoteDescription({ type: 'offer', sdp: hardenSdp(sdp) });
      const answer = await this.pc.createAnswer();
      await this.pc.setLocalDescription({ type: 'answer', sdp: hardenSdp(answer.sdp) });
      await this.signal('answer', this.pc.localDescription.sdp);
      // A renegotiation can put the encoders back on.
      await this.applySending();
    }).catch((e) => {
      // Hanging up can overtake a renegotiation (the app ends the call just
      // before the page hears of it): only an error in a call that's still
      // open is worth reporting.
      setTimeout(() => { if (!this.closed) console.error('call: renegotiation', e); }, 2000);
    });
    return this.negotiation;
  }

  onTrack(e) {
    const stream = e.streams[0];
    if (!stream) return;
    const participant = stream.id;
    const kind = e.track.id;
    // A renegotiation can deliver a receiver again (when the call's grants
    // change, say). Its encoded streams can be created only once, so the
    // first decryption pipe stays and follows the new binding.
    const bind = this.bound.get(e.receiver);
    if (bind) Object.assign(bind, { kind, participant });
    else {
      const fresh = { kind, participant };
      this.bound.set(e.receiver, fresh);
      const { readable, writable } = e.receiver.createEncodedStreams();
      readable.pipeThrough(this.receiverTransform(fresh)).pipeTo(writable).catch(() => {});
    }
    const r = this.remoteOf(participant);
    const had = r[kind];
    const hadStream = had ? (had.el ? had.el.srcObject : had) : null;
    const hadTrack = hadStream ? hadStream.getTracks()[0] : null;
    if (hadTrack === e.track) { this.onChange(); return; }
    if (had && had.el) { had.el.srcObject = null; had.src.disconnect(); had.gain.disconnect(); }
    if (kind === 'audio' || kind === 'screen-audio') {
      const ms = new MediaStream([e.track]);
      // Chromium only feeds remote WebRTC audio into WebAudio while an
      // element plays it; the element itself stays silent.
      const el = new Audio();
      el.muted = true;
      el.srcObject = ms;
      el.play().catch(() => {});
      const src = this.ctx.createMediaStreamSource(ms);
      const gain = this.ctx.createGain();
      const analyser = this.ctx.createAnalyser();
      analyser.fftSize = 512;
      src.connect(analyser);
      src.connect(gain);
      gain.connect(this.master);
      r[kind] = { el, src, gain, analyser, buf: new Float32Array(analyser.fftSize) };
      this.applyVolume(participant);
    } else {
      r[kind] = new MediaStream([e.track]);
    }
    e.track.onended = () => { delete r[kind]; this.onChange(); };
    this.onChange();
  }

  remoteOf(participant) {
    let r = this.remote.get(participant);
    if (!r) { r = { level: -120, speaking: false }; this.remote.set(participant, r); }
    return r;
  }

  // ---------------------------------------------------------------- local media

  // How this device talks now: 'vad', 'ptt' or 'studio'. Without Use Voice
  // Activity in the channel it is push to talk, whatever the setting says
  // (studio, always on, is voice activity with no gate).
  mode() {
    if (!this.s.perms.vad) return 'ptt';
    return ['ptt', 'studio'].includes(this.s.mode) ? this.s.mode : 'vad';
  }

  // Studio mode leaves the sound as the microphone (and any outboard
  // gate and processing) made it; otherwise the engine cancels echo,
  // suppresses noise and controls the gain.
  processing() { return this.mode() !== 'studio'; }

  async startMic(deviceId = this.s.mic) {
    const processing = this.processing();
    const ask = (id) => navigator.mediaDevices.getUserMedia({
      audio: {
        deviceId: id ? { exact: id } : undefined,
        echoCancellation: processing, noiseSuppression: processing, autoGainControl: processing,
      },
    });
    let stream;
    try {
      stream = await ask(deviceId);
    } catch (e) {
      // The chosen microphone is gone: the system's default instead.
      if (!deviceId || mediaProblem(e) !== 'none') throw e;
      stream = await ask('');
      deviceId = '';
    }
    if (this.closed) { stream.getTracks().forEach((x) => x.stop()); return; }
    const track = stream.getAudioTracks()[0];
    if (this.mic) { this.mic.onended = null; this.mic.stop(); }
    this.mic = track;
    // Unplugged mid-call: listening on, and another microphone is tried.
    track.onended = () => {
      if (this.mic !== track || this.closed) return;
      this.mic = null;
      this.micAnalyser = null;
      this.micState = 'none';
      this.onChange();
      this.devicesChanged().catch(() => {});
    };
    const src = this.ctx.createMediaStreamSource(new MediaStream([track]));
    this.micAnalyser = this.ctx.createAnalyser();
    this.micAnalyser.fftSize = 512;
    this.micBuf = new Float32Array(this.micAnalyser.fftSize);
    src.connect(this.micAnalyser);
    const send = track.clone();
    send.enabled = false;
    if (this.local.audio) this.local.audio.stop();
    this.local.audio = send;
    await this.tx.audio.sender.replaceTrack(send);
    await this.applySending();
    this.micProcessing = processing;
    this.s.mic = deviceId || '';
    this.micState = 'ok';
    this.micError = '';
    this.onChange();
  }

  micFailed(e) {
    this.micState = mediaProblem(e);
    this.micError = (e && e.message) || '';
    this.onChange();
  }

  // Another microphone, chosen in the voice bar or Settings. A failure is
  // shown and thrown (for a toast); the call goes on either way.
  async setMic(deviceId) {
    if (!this.s.perms.audio) return;
    try { await this.startMic(deviceId); } catch (e) { this.micFailed(e); throw new Error(mediaProblemText(this.micState, 'audio')); }
  }

  // "Try again" after a microphone problem.
  retryMic() { return this.setMic(this.s.mic); }

  // A device was plugged in or removed: a call without a working
  // microphone tries again (quietly: nothing changes if there still isn't
  // one); a camera that went away turns the camera off.
  async devicesChanged() {
    if (this.closed) return;
    if (this.s.perms.audio && !this.mic && this.micState !== 'denied') {
      await this.startMic(this.s.mic).catch((e) => this.micFailed(e));
    }
  }

  async setSpeaker(deviceId) {
    this.s.speaker = deviceId;
    if (this.ctx.setSinkId) await this.ctx.setSinkId(deviceId || '');
  }

  async setCamera(on, deviceId = this.s.camera) {
    if (on && !this.s.perms.video) throw new Error(t("You can't turn on video in this call."));
    if (this.local.camera) { this.local.camera.stop(); delete this.local.camera; }
    if (on) {
      const ask = (id) => navigator.mediaDevices.getUserMedia({ video: { ...CALL.CAMERA, deviceId: id ? { exact: id } : undefined } });
      let s;
      try {
        try {
          s = await ask(deviceId);
        } catch (e) {
          // The chosen camera is gone: the system's default instead.
          if (!deviceId || mediaProblem(e) !== 'none') throw e;
          s = await ask('');
          deviceId = '';
        }
      } catch (e) {
        // No camera: video stays off (as everyone already sees it), the call goes on.
        await this.tx.camera.sender.replaceTrack(null);
        await this.signal('state', this.mediaState()).catch(() => {});
        this.onChange();
        throw new Error(mediaProblemText(mediaProblem(e), 'video'));
      }
      const cam = s.getVideoTracks()[0];
      this.local.camera = cam;
      cam.contentHint = 'motion';
      // Unplugged: the camera goes off, and everyone sees that.
      cam.onended = () => {
        if (this.local.camera !== cam || this.closed) return;
        this.setCamera(false).catch(() => {});
        if (this.onProblem) this.onProblem({ kind: 'camera-gone' });
      };
      this.s.camera = deviceId || '';
    }
    await this.tx.camera.sender.replaceTrack(this.local.camera || null);
    await this.applySending();
    await this.signal('state', this.mediaState());
    this.onChange();
  }

  // The window or screen is picked in the app's own dialog (app.py), which
  // the engine shows when getDisplayMedia asks.
  // The shared screen's or window's sound goes too only when sharing it is
  // switched on (this.s.screenAudio, the screen_share_audio setting); it is
  // only captured then.
  async setScreen(on) {
    if (on && !this.s.perms.screen) throw new Error(t("You can't share your screen in this call."));
    for (const k of ['screen', 'screen-audio']) {
      if (this.local[k]) { this.local[k].stop(); delete this.local[k]; }
    }
    if (on) {
      let s;
      const audio = this.s.screenAudio !== false;
      try {
        s = await navigator.mediaDevices.getDisplayMedia({
          video: { frameRate: { ideal: 15, max: 30 } },
          audio: audio ? { echoCancellation: false, noiseSuppression: false, autoGainControl: false } : false,
          systemAudio: audio ? 'include' : 'exclude',
        });
      } catch (e) {
        throw new Error(e && e.name === 'NotAllowedError' ? t('Screen sharing was cancelled.')
          : e && e.message ? t("The system couldn't capture that screen or window. ({error})", { error: e.message })
            : t("The system couldn't capture that screen or window."));
      }
      this.local.screen = s.getVideoTracks()[0];
      this.local.screen.contentHint = 'detail';
      this.local.screen.onended = () => this.setScreen(false).catch(() => {});
      const a = s.getAudioTracks()[0];
      if (a && audio) this.local['screen-audio'] = a;
      else if (a) a.stop();
    }
    await this.tx.screen.sender.replaceTrack(this.local.screen || null);
    await this.tx['screen-audio'].sender.replaceTrack(this.local['screen-audio'] || null);
    await this.applySending();
    await this.signal('state', this.mediaState());
    this.onChange();
  }

  // Share the screen's sound, or not. Turning it off mid-share stops it at
  // once; turning it on applies from the next share (the sound has to be
  // picked with the screen). Returns whether the current share has sound.
  async setScreenAudio(on) {
    this.s.screenAudio = on;
    if (!on && this.local['screen-audio']) {
      this.local['screen-audio'].stop();
      delete this.local['screen-audio'];
      await this.tx['screen-audio'].sender.replaceTrack(null);
    }
    this.onChange();
    return !!this.local['screen-audio'];
  }

  mediaState() {
    return {
      self_mute: this.selfMute, self_deaf: this.selfDeaf,
      camera: !!this.local.camera, screen: !!this.local.screen,
      priority: this.priorityOn,
    };
  }

  async setPriority(on) {
    if (on && !this.s.perms.priority) throw new Error(t("You can't be a priority speaker in this call."));
    this.priorityOn = on;
    await this.signal('state', this.mediaState());
    this.onChange();
  }

  async setSelfMute(m) { this.selfMute = m; await this.signal('state', this.mediaState()); this.onChange(); }

  async setSelfDeaf(d) {
    this.selfDeaf = d;
    // Deafening also mutes, as everywhere else.
    if (d) this.selfMute = true;
    this.master.gain.value = d ? 0 : this.outputVolume;
    // Deafened, nothing is wanted from anyone: the relay stops sending it.
    this.sendListen();
    await this.signal('state', this.mediaState());
    this.onChange();
  }

  // This device's output level, 0..2.
  setOutputVolume(v) {
    this.outputVolume = Math.max(0, Math.min(2, v));
    if (this.master && !this.selfDeaf) this.master.gain.value = this.outputVolume;
  }

  // Per-user volume, 0..2 (200%), for one member on this device.
  setVolume(member, v) {
    this.volumes[member] = Math.max(0, Math.min(2, v));
    this.applyMember(member);
  }

  // Mute a member for this device only: not played, and not received.
  setLocalMute(member, on) {
    if (on) this.localMutes.add(member); else this.localMutes.delete(member);
    this.applyMember(member);
    this.sendListen();
    this.onChange();
  }

  // A member's screen-share sound on this device: its volume (0..2) and
  // whether it's muted (then it isn't received either).
  setStreamVolume(member, v) {
    this.streamVolumes[member] = Math.max(0, Math.min(2, v));
    this.applyMember(member);
  }

  setStreamMuted(member, on) {
    if (on) this.streamMuted.add(member); else this.streamMuted.delete(member);
    this.applyMember(member);
    this.sendListen();
    this.onChange();
  }

  applyMember(member) {
    for (const p of this.s.participants) if (p.member === member) this.applyVolume(p.participant);
  }

  applyVolume(participant) {
    const p = this.s.participants.find((x) => x.participant === participant);
    const r = this.remote.get(participant);
    if (!p || !r) return;
    // Others are quieter while a priority speaker talks.
    const prio = this.s.participants.some((x) => x.priority && x.participant !== participant && (this.remote.get(x.participant) || {}).speaking);
    const duck = prio && !p.priority ? 0.3 : 1;
    const voice = this.localMutes.has(p.member) ? 0 : (this.volumes[p.member] ?? 1);
    const stream = this.streamMuted.has(p.member) ? 0 : (this.streamVolumes[p.member] ?? 1);
    if (r.audio) r.audio.gain.gain.value = voice * duck;
    if (r['screen-audio']) r['screen-audio'].gain.gain.value = stream * duck;
  }

  // ---------------------------------------------------------------- sending only what's taken

  // What this device wants the relay to forward to it: nothing while
  // deafened; not the members muted here; screen sound only from screens
  // watched here and not muted (CallListen).
  listenState() {
    const noAudio = [];
    const screenAudio = [];
    for (const p of this.s.participants) {
      if (p.participant === this.s.participant) continue;
      if (this.localMutes.has(p.member)) noAudio.push(p.participant);
      if (this.layers.has(`${p.participant}/screen`) && !this.streamMuted.has(p.member)) screenAudio.push(p.participant);
    }
    return { deaf: !!this.selfDeaf, noAudio, screenAudio };
  }

  // Tells the relay when the listening state changed (and once at the
  // start).
  sendListen() {
    if (!this.pc || this.closed) return;
    const st = this.listenState();
    const key = JSON.stringify(st);
    if (key === this.lastListen) return;
    this.lastListen = key;
    this.signal('listen', st).catch(() => { if (this.lastListen === key) this.lastListen = ''; });
  }

  // The relay's CallWatchers: who receives each kind of this device's media.
  onWatchers(watches) {
    const w = {};
    for (const x of watches || []) w[x.kind] = { receivers: x.receivers || 0, layers: x.layers || [] };
    this.watchers = w;
    return this.applySending();
  }

  // Whether a kind of this device's media goes out (has receivers); until
  // the relay says, everything does.
  wanted(kind) {
    if (!this.watchers) return true;
    const x = this.watchers[kind];
    return !!(x && x.receivers > 0);
  }

  // Encode and send each kind only when someone receives it, and video only
  // in the layers someone asked for. The track stays (the camera's own
  // preview, the voice detector); its encoder stops.
  async applySending() {
    if (!this.pc || this.closed) return;
    for (const [kind, tr] of Object.entries(this.tx)) {
      const on = this.wanted(kind);
      const x = this.watchers && this.watchers[kind];
      const rids = x && x.layers && x.layers.length ? x.layers : null;
      await this.setActive(kind, tr.sender, (e) => on && (!rids || !e.rid || rids.includes(e.rid)));
    }
    this.onChange();
  }

  // Compared with the sender's own parameters each time: the engine can
  // put them back (a new track, a renegotiation).
  async setActive(kind, sender, active) {
    if (!sender.getParameters) return;
    const params = sender.getParameters();
    if (!params.encodings || !params.encodings.length) return; // not negotiated yet
    const flags = params.encodings.map((e) => !!active(e));
    if (params.encodings.every((e, i) => (e.active !== false) === flags[i])) return;
    params.encodings.forEach((e, i) => { e.active = flags[i]; });
    try {
      await sender.setParameters(params);
    } catch (e) { /* tried again on the next round */ }
  }

  // What goes out now, per kind: a track, and an encoding switched on.
  sending() {
    const out = {};
    for (const [kind, tr] of Object.entries(this.tx)) {
      const enc = (tr.sender.getParameters ? tr.sender.getParameters().encodings : null) || [{}];
      out[kind] = !!tr.sender.track && enc.some((e) => e.active !== false);
    }
    return out;
  }

  // ---------------------------------------------------------------- talking

  setPushToTalk(down) { this.ptt = down; }

  // Whether this device's microphone is open right now (the gate; whether
  // anyone receives it is sending()).
  transmitting() {
    if (this.selfMute || !this.mic || !this.s.perms.audio || this.s.serverMuted) return false;
    const mode = this.mode();
    if (mode === 'ptt') return this.ptt;
    if (mode === 'studio') return true;
    return this.vadOpen;
  }

  tick() {
    const now = performance.now();
    if (this.micAnalyser) {
      this.myLevel = levelDb(this.micAnalyser, this.micBuf);
      if (this.myLevel > this.s.vadThreshold) { this.vadOpen = true; this.vadUntil = now + CALL.VAD_HANGOVER_MS; } else if (now > this.vadUntil) this.vadOpen = false;
    }
    const tx = this.transmitting();
    if (this.local.audio && this.local.audio.enabled !== tx) { this.local.audio.enabled = tx; this.onChange(); }
    let changed = false;
    for (const [participant, r] of this.remote) {
      const a = r.audio;
      if (!a) continue;
      r.level = levelDb(a.analyser, a.buf);
      if (r.level > -50) r.speakingUntil = now + CALL.VAD_HANGOVER_MS;
      const speaking = (r.speakingUntil || 0) > now;
      if (speaking !== r.speaking) { r.speaking = speaking; changed = true; }
    }
    if (changed) {
      for (const p of this.s.participants) this.applyVolume(p.participant);
      this.onChange();
    }
  }

  speaking(participant) {
    if (participant === this.s.participant) return !!(this.local.audio && this.local.audio.enabled);
    const r = this.remote.get(participant);
    return !!(r && r.speaking);
  }

  // ---------------------------------------------------------------- layers

  // Each video is asked for at the layer its tile can show (tiles report
  // their size through watch()), capped by how the link copes: loss over
  // 5% (counted before repair, videoLoss) lowers the cap one layer for
  // everyone, and after 10 s under 1% it goes back up a layer. (A bandwidth estimate alone can't drive this: a
  // receiver only sees as much bandwidth as the layers it asked for.)
  // Only what is being watched here is asked for (and a watched screen's
  // sound); the first size starts it at once.
  watch(participant, kind, width) {
    const key = `${participant}/${kind}`;
    const was = this.layers.get(key);
    this.layers.set(key, { width, rid: was ? was.rid : undefined });
    if (!was) {
      if (kind === 'screen') this.sendListen();
      this.chooseLayers().catch(() => {});
    }
  }

  unwatch(participant, kind) {
    const key = `${participant}/${kind}`;
    const was = this.layers.get(key);
    this.layers.delete(key);
    if (was && was.rid) this.signal('layer', participant, kind, '').catch(() => {});
    if (was && kind === 'screen') this.sendListen();
  }

  // The share of video packets dropped on the way since the last sample,
  // counted before repair: packetsLost alone goes back down when a packet
  // asked for again (NACK) arrives and fills its gap, so a link losing 15%
  // can look clean. Packets received again (or, where the engine doesn't
  // count those, NACKs sent) are dropped packets too. Null until enough
  // packets came to tell.
  async videoLoss() {
    const stats = await this.pc.getStats();
    const now = { lost: 0, got: 0, again: 0, nacks: 0, counted: false };
    stats.forEach((s) => {
      if (s.type !== 'inbound-rtp' || s.kind !== 'video') return;
      now.lost += s.packetsLost || 0;
      now.got += s.packetsReceived || 0;
      now.nacks += s.nackCount || 0;
      if (s.retransmittedPacketsReceived !== undefined) { now.again += s.retransmittedPacketsReceived; now.counted = true; }
    });
    const prev = this.lastLoss || now;
    this.lastLoss = now;
    const dl = now.lost - prev.lost;
    const expected = now.got - prev.got + dl;
    if (expected < CALL.LOSS_MIN_PACKETS) return null;
    const dropped = dl + (now.counted ? now.again - prev.again : now.nacks - prev.nacks);
    return Math.min(1, Math.max(0, dropped) / expected);
  }

  async chooseLayers() {
    if (!this.pc || this.layers.size === 0) return;
    const order = ['l', 'm', 'h'];
    const now = performance.now();
    const loss = await this.videoLoss();
    if (loss !== null) this.loss = loss;
    if (this.cap === undefined) { this.cap = 2; this.capAt = now; }
    if (loss !== null) {
      if (loss > 0.05 && this.cap > 0) { this.cap -= 1; this.capAt = now; } else if (loss < 0.01 && this.cap < 2 && now - this.capAt > 10000) { this.cap += 1; this.capAt = now; }
    }
    for (const [key, v] of this.layers) {
      const want = v.width >= 640 ? 2 : v.width >= 280 ? 1 : 0;
      const rid = order[Math.min(want, this.cap)];
      if (rid !== v.rid) {
        v.rid = rid;
        const [participant, kind] = key.split('/');
        await this.signal('layer', participant, kind, rid);
      }
    }
  }

  // ---------------------------------------------------------------- updates

  // New participants, keys or permissions from the community server.
  async update(session) {
    const before = this.s;
    this.s = Object.assign({}, before, session);
    if (session.epoch !== undefined) await this.setKeys(session.epoch, session.key, session.slot);
    else await this.installRecvKeys();
    if (!this.s.perms.video && this.local.camera) await this.setCamera(false);
    if (!this.s.perms.screen && this.local.screen) await this.setScreen(false);
    if (!this.s.perms.priority && this.priorityOn) await this.setPriority(false);
    for (const p of this.s.participants) this.applyVolume(p.participant);
    for (const id of [...this.remote.keys()]) if (!this.s.participants.some((p) => p.participant === id)) this.dropRemote(id);
    // Someone muted here may have (re)joined.
    this.sendListen();
    this.onChange();
  }

  dropRemote(id) {
    const r = this.remote.get(id);
    if (!r) return;
    for (const k of ['audio', 'screen-audio']) if (r[k]) { r[k].el.srcObject = null; r[k].src.disconnect(); r[k].gain.disconnect(); }
    this.remote.delete(id);
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    for (const timer of this.timers) { clearInterval(timer); clearTimeout(timer); }
    if (navigator.mediaDevices && navigator.mediaDevices.removeEventListener) navigator.mediaDevices.removeEventListener('devicechange', this.onDevices);
    for (const track of Object.values(this.local)) track.stop();
    if (this.mic) this.mic.stop();
    for (const id of [...this.remote.keys()]) this.dropRemote(id);
    if (this.pc) this.pc.close();
    if (this.ctx) this.ctx.close().catch(() => {});
    this.pc = null;
    this.sframe = new SFrame.Context(); // forget the keys
    this.epochs = [];
  }

  // For tests and the call's connection details panel (no keys).
  debug() {
    return {
      connection: this.pc ? this.pc.connectionState : 'closed',
      phase: this.phase,
      problem: this.problem ? { ...this.problem } : null,
      mic: this.micState,
      transmitting: this.transmitting(),
      mode: this.mode(),
      processing: this.micProcessing ?? null,
      sending: this.sending(),
      watchers: this.watchers ? JSON.parse(JSON.stringify(this.watchers)) : null,
      listen: this.listenState(),
      stats: { ...this.stats, why: { ...this.stats.why }, from: { ...this.stats.from }, sentFrames: { ...this.stats.sentFrames } },
      remote: [...this.remote].map(([id, r]) => ({ id, audio: !!r.audio, camera: !!r.camera, screen: !!r.screen, speaking: r.speaking, level: r.level })),
      layers: [...this.layers].map(([k, v]) => ({ key: k, rid: v.rid || '' })),
      myLevel: this.myLevel,
      epochs: this.epochs.map((e) => e.epoch),
    };
  }
}

if (typeof module !== 'undefined') module.exports = { Call, CALL, frameMetadata, hardenSdp, kidFor, mediaProblem, mediaProblemText, relayMediaAddress };
