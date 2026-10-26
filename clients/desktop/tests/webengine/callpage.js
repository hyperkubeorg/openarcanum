// Test-side glue for callharness.py (not shipped).
'use strict';
// Signals from call.js go to the harness through an outbox it polls; its
// answers come back through resolveSignal.
const outbox = [];
const waiting = new Map();
let nextSignal = 1;
let call = null;
const log = [];

function signal(kind, ...args) {
  const id = nextSignal++;
  outbox.push({ id, kind, args });
  return new Promise((resolve, reject) => waiting.set(id, { resolve, reject }));
}
function drainOutbox() { return outbox.splice(0); }
function resolveSignal(id, value, error) {
  const w = waiting.get(id);
  waiting.delete(id);
  if (!w) return;
  if (error) w.reject(new Error(error)); else w.resolve(value);
}

// Latency mode: what this page sends is a tone it switches on, with push-
// to-talk held (so voice-activity gating isn't part of the measurement), so
// the time a burst leaves and the time it is heard can be compared.
let burstGain = null;
async function useTone() {
  const ctx = call.ctx;
  const osc = ctx.createOscillator();
  osc.frequency.value = 1000;
  burstGain = ctx.createGain();
  burstGain.gain.value = 0;
  const dest = ctx.createMediaStreamDestination();
  osc.connect(burstGain).connect(dest);
  osc.start();
  window.toneCtx = ctx;
  const track = dest.stream.getAudioTracks()[0];
  if (call.local.audio) call.local.audio.stop();
  call.local.audio = track;
  await call.tx.audio.sender.replaceTrack(track);
  call.s.mode = 'ptt';
  call.setPushToTalk(true);
}
// Sends a 200 ms burst; returns its wall-clock start (ms).
function burst() {
  const t = performance.timeOrigin + performance.now();
  burstGain.gain.setValueAtTime(1, window.toneCtx.currentTime);
  burstGain.gain.setValueAtTime(0, window.toneCtx.currentTime + 0.2);
  return t;
}
// Watches remote audio for bursts; records wall-clock onsets (ms), plus
// the playout latency still ahead of the speaker.
const onsets = [];
function listenForBursts() {
  const proc = call.ctx.createScriptProcessor(256, 1, 1);
  call.master.connect(proc);
  proc.connect(call.ctx.destination);
  let loud = false;
  proc.onaudioprocess = (e) => {
    const d = e.inputBuffer.getChannelData(0);
    let peak = 0;
    for (let i = 0; i < d.length; i++) peak = Math.max(peak, Math.abs(d[i]));
    const now = performance.timeOrigin + performance.now();
    if (!loud && peak > 0.1) { onsets.push(now + 1000 * ((call.ctx.baseLatency || 0) + (call.ctx.outputLatency || 0))); loud = true; }
    if (loud && peak < 0.02) loud = false;
  };
}

// A generated "microphone" (the engine runs at most 16 capture streams per
// process, so big calls in one harness use this beyond that): a tone that
// is on half of each second, made in the call's own audio context.
function useSynthMic() {
  const real = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
  navigator.mediaDevices.getUserMedia = async (c) => {
    if (!c.audio) return real(c);
    const ctx = call.ctx;
    const osc = ctx.createOscillator();
    osc.frequency.value = 330 + Math.random() * 300;
    const gate = ctx.createGain();
    const lfo = ctx.createOscillator();
    lfo.type = 'square';
    lfo.frequency.value = 1;
    lfo.connect(gate.gain);
    const dest = ctx.createMediaStreamDestination();
    osc.connect(gate).connect(dest);
    osc.start();
    lfo.start();
    return dest.stream;
  };
}

// Wire mode: what this page sends is one of a few known sounds, chosen
// with setSignal: 'speech' (a voice-like sound: a buzzing glottal source
// with a wandering pitch through two moving formant filters, in syllables
// with pauses, like talking), 'tone' (440 Hz), 'noise' (white) or
// 'silence'. Generated in the call's own audio context, sent in place of
// the microphone (call.js still gates it by mode and push to talk).
let signalGains = null;
async function useSignal() {
  const ctx = call.ctx;
  const dest = ctx.createMediaStreamDestination();
  const make = () => { const g = ctx.createGain(); g.gain.value = 0; g.connect(dest); return g; };
  signalGains = { speech: make(), tone: make(), noise: make() };
  const tone = ctx.createOscillator();
  tone.frequency.value = 440;
  const toneLevel = ctx.createGain();
  toneLevel.gain.value = 0.3;
  tone.connect(toneLevel).connect(signalGains.tone);
  tone.start();
  const buf = ctx.createBuffer(1, ctx.sampleRate * 2, ctx.sampleRate);
  const d = buf.getChannelData(0);
  for (let i = 0; i < d.length; i++) d[i] = (Math.random() * 2 - 1) * 0.3;
  const noise = ctx.createBufferSource();
  noise.buffer = buf;
  noise.loop = true;
  noise.connect(signalGains.noise);
  noise.start();
  const voice = ctx.createOscillator();
  voice.type = 'sawtooth';
  const f1 = ctx.createBiquadFilter();
  const f2 = ctx.createBiquadFilter();
  f1.type = f2.type = 'bandpass';
  f1.Q.value = f2.Q.value = 5;
  const env = ctx.createGain();
  env.gain.value = 0;
  voice.connect(f1).connect(env);
  voice.connect(f2).connect(env);
  env.connect(signalGains.speech);
  // Three minutes of syllables, scheduled ahead: 120..250 ms each, pitch
  // 100..220 Hz, vowels from a few formant pairs, and a pause now and then.
  const vowels = [[730, 1090], [270, 2290], [530, 1840], [570, 840], [300, 870]];
  let t = ctx.currentTime + 0.05;
  while (t < ctx.currentTime + 180) {
    const len = 0.12 + Math.random() * 0.13;
    const [a, b] = vowels[Math.floor(Math.random() * vowels.length)];
    voice.frequency.setValueAtTime(100 + Math.random() * 120, t);
    voice.frequency.linearRampToValueAtTime(100 + Math.random() * 120, t + len);
    f1.frequency.setValueAtTime(a, t);
    f2.frequency.setValueAtTime(b, t);
    env.gain.setValueAtTime(0, t);
    env.gain.linearRampToValueAtTime(0.6 + Math.random() * 0.6, t + 0.03);
    env.gain.linearRampToValueAtTime(0, t + len);
    t += len + (Math.random() < 0.2 ? 0.2 + Math.random() * 0.3 : 0.02);
  }
  voice.start();
  const track = dest.stream.getAudioTracks()[0];
  if (call.local.audio) call.local.audio.stop();
  call.local.audio = track;
  await call.tx.audio.sender.replaceTrack(track);
}
function setSignal(name, mode, ptt) {
  for (const [k, g] of Object.entries(signalGains)) g.gain.value = k === name ? 1 : 0;
  call.s.mode = mode;
  call.setPushToTalk(ptt);
}

async function startCall(session, opts) {
  try {
    if (opts.synthMic) useSynthMic();
    call = new Call(session, signal, () => {});
    await call.start();
    if (opts.tone) await useTone();
    if (opts.signal) await useSignal();
    if (opts.smallCamera) CALL.CAMERA = { width: { ideal: 320 }, height: { ideal: 180 }, frameRate: { ideal: 15, max: 15 } };
    if (opts.camera) await call.setCamera(true);
    if (opts.screen) await call.setScreen(true);
    if (opts.listen) listenForBursts();
    return 'ok';
  } catch (e) { log.push(String(e && e.stack || e)); return 'error: ' + e; }
}

// The microphone's level as call.js measures it (dBFS RMS over 512
// samples, after whatever processing the call asked the engine for), for
// every 512 samples over ms milliseconds.
function micLevels(ms) {
  return new Promise((done) => {
    const proc = call.ctx.createScriptProcessor(512, 1, 1);
    const src = call.ctx.createMediaStreamSource(new MediaStream([call.mic]));
    const silent = call.ctx.createGain();
    silent.gain.value = 0;
    src.connect(proc);
    proc.connect(silent).connect(call.ctx.destination);
    const out = [];
    proc.onaudioprocess = (e) => {
      const d = e.inputBuffer.getChannelData(0);
      let sum = 0;
      for (let i = 0; i < d.length; i++) sum += d[i] * d[i];
      const rms = Math.sqrt(sum / d.length);
      out.push(rms > 0 ? 20 * Math.log10(rms) : -120);
    };
    setTimeout(() => { proc.disconnect(); src.disconnect(); done({ levels: out, rate: call.ctx.sampleRate }); }, ms);
  });
}

// call.js's voice gate (open, transmitting) every 50 ms for ms milliseconds.
function gateStates(ms) {
  return new Promise((done) => {
    const out = [];
    const t = setInterval(() => out.push({ open: call.vadOpen, tx: call.transmitting(), level: call.myLevel }), 50);
    setTimeout(() => { clearInterval(t); done(out); }, ms);
  });
}

async function inboundVideo() {
  const out = [];
  const stats = await call.pc.getStats();
  stats.forEach((s) => { if (s.type === 'inbound-rtp' && s.kind === 'video') out.push({ mid: s.mid, w: s.frameWidth || 0, h: s.frameHeight || 0, frames: s.framesDecoded || 0 }); });
  return out;
}

// What each remote participant's audio sounds like on this device right
// now: its level as received (before the volume stage) and as played (after
// the participant's volume and the deafen stage, measured on the audio
// itself with analysers this glue adds), plus the volume call.js set and
// whether it counts them as speaking. Priority-speaker ducking and deafen
// show up as the gap between the two levels.
const taps = new Map();
function tap(node) {
  let t = taps.get(node);
  if (!t) {
    const analyser = call.ctx.createAnalyser();
    analyser.fftSize = 512;
    node.connect(analyser);
    t = { analyser, buf: new Float32Array(analyser.fftSize) };
    taps.set(node, t);
  }
  return t;
}
function playedLevels() {
  const out = {};
  for (const [id, r] of call.remote) {
    if (!r.audio) continue;
    // After the participant's own gain, and after the master (deafen) gain:
    // a per-participant tap after the master would mix everyone, so the
    // master's effect is reported as its gain and as the mixed level.
    const post = tap(r.audio.gain);
    out[id] = {
      pre: levelDb(r.audio.analyser, r.audio.buf),
      post: levelDb(post.analyser, post.buf),
      gain: r.audio.gain.gain.value,
      speaking: !!r.speaking,
    };
  }
  const m = tap(call.master);
  return { remote: out, master: { gain: call.master.gain.value, level: levelDb(m.analyser, m.buf) } };
}
