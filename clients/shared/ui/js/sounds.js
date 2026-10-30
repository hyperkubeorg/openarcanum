// Notification and call sounds (docs/user-guide.md "Sounds"; Settings →
// Notifications → Sounds). Every sound is made here with the Web Audio API
// while the app runs: a few short sine and triangle notes with smooth attack
// and release, quiet, each under 250 ms. There are no audio files.
//
// When a sound plays:
// - message: a new message in a channel you aren't reading; mention: one
//   that mentions you (also in a muted channel, as notifications do); dm: a
//   direct message in a conversation you aren't reading. They follow the
//   community and channel mutes and the notification levels (Nothing,
//   Mentions only), not the desktop notifications switch.
// - join / leave: someone joins or leaves the voice channel you're in.
// - mute / unmute, deafen / undeafen, connect / disconnect: your own call.
// Do not disturb and deafen silence everything except your own call's
// sounds (mute, deafen, connected, disconnected): they answer something you
// did. Nothing plays before the first click or key press in the window
// (browsers' autoplay rule), and the AudioContext is made at the first sound.
//
// Every sound that passes these rules is announced as a window event
// ("app-sound", detail {name}) before it plays; the UI tests record it.
'use strict';

// The notes of each sound: [frequency (Hz), start (s), length (s), wave,
// level (0..1)]. One family: the same soft envelope, notes from one scale.
const SOUND_NOTES = {
  message: [[880, 0, 0.16, 'sine', 0.9], [1760, 0, 0.09, 'sine', 0.12]],
  dm: [[784, 0, 0.12, 'sine', 0.85], [1047, 0.075, 0.14, 'sine', 0.85]],
  mention: [[988, 0, 0.12, 'triangle', 1], [1319, 0.08, 0.14, 'triangle', 1]],
  join: [[523, 0, 0.13, 'triangle', 0.9], [784, 0.09, 0.13, 'triangle', 0.9]],
  leave: [[784, 0, 0.13, 'triangle', 0.9], [523, 0.09, 0.13, 'triangle', 0.9]],
  mute: [[1175, 0, 0.035, 'triangle', 0.6], [880, 0.055, 0.04, 'triangle', 0.6]],
  unmute: [[880, 0, 0.035, 'triangle', 0.6], [1175, 0.055, 0.04, 'triangle', 0.6]],
  deafen: [[659, 0, 0.04, 'triangle', 0.7], [494, 0.06, 0.05, 'triangle', 0.7]],
  undeafen: [[494, 0, 0.04, 'triangle', 0.7], [659, 0.06, 0.05, 'triangle', 0.7]],
  connect: [[659, 0, 0.09, 'sine', 0.8], [880, 0.06, 0.09, 'sine', 0.8], [1319, 0.12, 0.1, 'sine', 0.7]],
  disconnect: [[1319, 0, 0.09, 'sine', 0.7], [880, 0.06, 0.09, 'sine', 0.8], [659, 0.12, 0.1, 'sine', 0.8]],
};

// The switch in Settings that turns each sound on and off (settings.py
// SOUND_SWITCHES; sounds_off lists the ones turned off).
const SOUND_SWITCH = {
  message: 'message', dm: 'dm', mention: 'mention', join: 'join', leave: 'leave',
  mute: 'mute', unmute: 'mute', deafen: 'deafen', undeafen: 'deafen', connect: 'call', disconnect: 'call',
};
const SOUND_SWITCHES = ['message', 'dm', 'mention', 'join', 'leave', 'mute', 'deafen', 'call'];
// What each switch's Play button plays.
const SOUND_PREVIEW = { message: 'message', dm: 'dm', mention: 'mention', join: 'join', leave: 'leave', mute: 'mute', deafen: 'deafen', call: 'connect' };
// Your own call's sounds: they still play in Do not disturb and deafened.
const OWN_SOUNDS = new Set(['mute', 'unmute', 'deafen', 'undeafen', 'connect', 'disconnect']);
// Sounds for something that arrived: a burst of them (a catch-up after
// reconnecting, a busy channel) plays one.
const ARRIVAL_SOUNDS = new Set(['message', 'dm', 'mention']);
const ARRIVAL_GAP_MS = 1000;
// The least time between two plays of the same sound.
const SOUND_COOLDOWN_MS = { message: 2000, dm: 1500, mention: 1000, join: 400, leave: 400 };
const OWN_COOLDOWN_MS = 60;
// The loudest a note gets at 100 % (the sum of a sound's notes stays well
// under full scale), and the attack of every note.
const SOUND_PEAK = 0.25;
const SOUND_ATTACK = 0.008;
// Messages older than this when they arrive are history, not news.
const SOUND_RECENT_MS = 2 * 60000;

// Why a sound must not play now, or '' when it may. `s`: prefs (the vault's
// sounds, sound_volume, sounds_off), dnd, deafened, now (ms), last (sound ->
// last played, ms), lastArrival (ms).
function soundBlocked(name, s) {
  if (!SOUND_NOTES[name]) return 'unknown';
  // Settings not read yet (still locked since the start): no sound.
  const p = s.prefs;
  if (!p) return 'unset';
  if (p.sounds === false) return 'off';
  if (!((p.sound_volume ?? 50) > 0)) return 'silent';
  if ((p.sounds_off || []).includes(SOUND_SWITCH[name])) return 'switch';
  const own = OWN_SOUNDS.has(name);
  if (!own && s.dnd) return 'dnd';
  if (!own && s.deafened) return 'deafened';
  const last = (s.last || {})[name];
  if (last !== undefined && s.now - last < (own ? OWN_COOLDOWN_MS : SOUND_COOLDOWN_MS[name] || 0)) return 'cooldown';
  if (ARRIVAL_SOUNDS.has(name) && s.now - (s.lastArrival ?? -Infinity) < ARRIVAL_GAP_MS) return 'burst';
  return '';
}

// The sound for a community message, or ''. env: muted (the community),
// level (all, mentions, nothing), channelMuted, reading (the channel is on
// screen in the focused window).
// Reading the channel (env.reading) still plays it: the desktop
// notification is what's left out then (core.set_reading), not the sound.
function communityMessageSound(m, env) {
  if (!m || m.deleted || env.muted || env.level === 'nothing') return '';
  if (m.mentions_me) return 'mention';
  if (env.channelMuted || env.level === 'mentions') return '';
  return 'message';
}

// The sound for a direct message, or ''. env: mode (notify_mode), reading.
function dmMessageSound(m, env) {
  if (!m || m.mine) return '';
  if (m.mentions_me) return 'mention';
  return env.mode === 'mentions' ? '' : 'dm';
}

// Who joined and who left a voice room: before and after are lists of
// participants ({member}); `me` is left out.
function voiceRoomChanges(before, after, me) {
  const was = new Set((before || []).map((p) => p.member));
  const now = new Set((after || []).map((p) => p.member));
  was.delete(me); now.delete(me);
  return { joined: [...now].filter((x) => !was.has(x)), left: [...was].filter((x) => !now.has(x)) };
}

// Plays sounds. deps: prefs() (the vault), state() ({dnd, deafened}), now(),
// activated() (a click or key press has happened in the window), makeContext(), announce(name),
// sinkId(label) (a promise of the output device's id, '' for the default).
class SoundBoard {
  constructor(deps) {
    this.deps = deps;
    this.last = {};
    this.lastArrival = -Infinity;
    this.ctx = null;
    this.sinkLabel = null;
  }

  // Plays `name` if the settings and the moment allow it. True when it was
  // requested (whether or not the page may make sound yet).
  play(name) {
    const d = this.deps;
    const now = d.now();
    const why = soundBlocked(name, { prefs: d.prefs(), ...d.state(), now, last: this.last, lastArrival: this.lastArrival });
    if (why) return false;
    this.last[name] = now;
    if (ARRIVAL_SOUNDS.has(name)) this.lastArrival = now;
    this.output(name, d.prefs().sound_volume ?? 50);
    return true;
  }

  // A switch's Play button: whatever the switches say, at `volume` percent.
  preview(name, volume) { this.output(name, volume); }

  output(name, volume) {
    const d = this.deps;
    try { d.announce(name); } catch (e) { /* nobody listening */ }
    if (!d.activated() || !(volume > 0)) return;
    try { this.synth(SOUND_NOTES[name], Math.min(100, volume) / 100); } catch (e) { console.error('sound', e); }
  }

  context() {
    // Made once; where the page can't make one, sounds stay silent (said once).
    if (this.ctx === null) {
      try { this.ctx = this.deps.makeContext() || false; } catch (e) { this.ctx = false; console.error('sound', e); }
    }
    if (!this.ctx) return null;
    if (this.ctx.state === 'suspended' && this.ctx.resume) this.ctx.resume().catch(() => {});
    const label = (this.deps.prefs() || {}).voice_output || 'default';
    if (label !== this.sinkLabel && this.ctx.setSinkId && this.deps.sinkId) {
      this.sinkLabel = label;
      const ctx = this.ctx;
      this.deps.sinkId(label).then((id) => ctx.setSinkId(id || '')).catch(() => {});
    }
    return this.ctx;
  }

  synth(notes, level) {
    const ctx = this.context();
    if (!ctx || !notes) return;
    const start = ctx.currentTime + 0.01;
    for (const [freq, at, len, wave, gainLevel] of notes) {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = wave;
      osc.frequency.value = freq;
      const t0 = start + at;
      const peak = Math.max(0.0002, SOUND_PEAK * level * gainLevel);
      gain.gain.setValueAtTime(0.0001, t0);
      gain.gain.linearRampToValueAtTime(peak, t0 + SOUND_ATTACK);
      gain.gain.exponentialRampToValueAtTime(0.0001, t0 + len);
      osc.connect(gain).connect(ctx.destination);
      osc.start(t0);
      osc.stop(t0 + len + 0.02);
    }
  }
}

// ---------------------------------------------------------------- in the page

const soundsUI = {
  vault: null, // the vault settings (get_settings), shared with Settings while it's open
  board: null,
  activated: false,
  pending: {}, // `${cid}:${channel}` -> the newest message, until its unread count arrives
  seen: new Set(), // messages already considered
  rooms: {}, // cid -> its voice rooms as last seen
  call: null, // {call, mute, deaf}: the engine's state, for your own mute and deafen
  connected: null, // the room whose "connected" sound has played
};

function windowReading() { return document.hasFocus() && !document.hidden; }

function soundPrefs() { return soundsUI.vault; }

function playSound(name) { return soundsUI.board ? soundsUI.board.play(name) : false; }

if (typeof window !== 'undefined' && typeof on === 'function') {
  soundsUI.board = new SoundBoard({
    prefs: soundPrefs,
    state: () => ({
      dnd: typeof myStatus === 'function' && myStatus().state === 'dnd',
      deafened: !!(typeof voiceUI !== 'undefined' && voiceUI.call && voiceUI.call.selfDeaf),
    }),
    now: () => Date.now(),
    activated: () => soundsUI.activated || !!(navigator.userActivation && navigator.userActivation.hasBeenActive),
    makeContext: () => (typeof AudioContext === 'function' ? new AudioContext({ latencyHint: 'interactive' }) : null),
    announce: (name) => window.dispatchEvent(new CustomEvent('app-sound', { detail: { name } })),
    sinkId: async (label) => {
      if (!label || label === 'default') return '';
      const devs = await navigator.mediaDevices.enumerateDevices();
      const d = devs.find((x) => x.kind === 'audiooutput' && x.label === label);
      return d ? d.deviceId : '';
    },
  });
  for (const ev of ['pointerdown', 'keydown']) {
    window.addEventListener(ev, (e) => { if (e.isTrusted) soundsUI.activated = true; }, { capture: true, passive: true });
  }

  on('profile', (s) => {
    // Kept while locked: messages and calls go on, and so do their sounds.
    if (s && s.state === 'unlocked') api('get_settings').then((d) => { soundsUI.vault = d.vault; }).catch(() => {});
  });

  // A community message counts once its channel's unread count says it
  // wasn't read: the core marks what you post (also from your other
  // devices) as read before it says so.
  on('message', ({ community, message: m }) => {
    const k = `${community}:${m.channel_id}:${m.id}`;
    if (soundsUI.seen.has(k)) return;
    if (soundsUI.seen.size > 5000) soundsUI.seen.clear();
    soundsUI.seen.add(k);
    if (m.deleted || m.system || !(Date.now() - (m.created_at || 0) < SOUND_RECENT_MS)) return;
    soundsUI.pending[`${community}:${m.channel_id}`] = m;
  });

  on('unread', (u) => {
    const ck = `${u.community}:${u.channel}`;
    const m = soundsUI.pending[ck];
    delete soundsUI.pending[ck];
    if (!m || m.id <= (u.last_read || 0)) return;
    const summary = st.communities.find((c) => c.id === u.community) || {};
    const s = st.states[u.community];
    const chan = s && s.channels.find((c) => c.id === u.channel);
    const v = soundPrefs() || {};
    const name = communityMessageSound(m, {
      muted: !!summary.muted,
      level: summary.notify_level || v.notify_mode || 'all',
      channelMuted: chan ? !!chan.muted : (v.muted_channels || []).includes(ck),
      reading: windowReading() && st.view === 'community' && st.cid === u.community && (st.channel === u.channel || st.voiceChat === u.channel),
    });
    if (name) playSound(name);
  });

  on('dm_message', ({ conv, message: m }) => {
    if (!m || !(Date.now() - (m.recv_at || m.at || 0) < SOUND_RECENT_MS)) return;
    const name = dmMessageSound(m, {
      mode: (soundPrefs() || {}).notify_mode || 'all',
      reading: windowReading() && st.view === 'dms' && typeof dmUI !== 'undefined' && dmUI.current === conv,
    });
    if (name) playSound(name);
  });

  // Someone joining or leaving the room you're in.
  on('voice_rooms', (d) => {
    const room = (rooms, channel) => { const r = (rooms || []).find((x) => x.channel === channel); return r ? r.participants : []; };
    const before = soundsUI.rooms[d.community];
    soundsUI.rooms[d.community] = d.rooms || [];
    const v = voiceUI.state;
    if (!before || !v.active || d.community !== v.community) return;
    const s = st.states[v.community];
    const { joined, left } = voiceRoomChanges(room(before, v.channel), room(d.rooms, v.channel), s && s.self ? s.self.key : '');
    if (joined.length) playSound('join');
    else if (left.length) playSound('leave');
  });

  on('voice', (s) => {
    if (s.active) return;
    if (soundsUI.connected) playSound('disconnect');
    soundsUI.call = null;
    soundsUI.connected = null;
  });
}

// The call engine changed (voice.js voiceUI.changed): connected, and your
// own mute and deafen.
function soundsCallChanged(call, state) {
  if (!call || !state || !state.active) return;
  const roomKey = `${state.community}:${state.channel}`;
  if (call.phase === 'connected' && soundsUI.connected !== roomKey) {
    soundsUI.connected = roomKey;
    playSound('connect');
  }
  const prev = soundsUI.call;
  soundsUI.call = { call, mute: !!call.selfMute, deaf: !!call.selfDeaf };
  if (!prev || prev.call !== call) return;
  if (prev.deaf !== !!call.selfDeaf) playSound(call.selfDeaf ? 'deafen' : 'undeafen');
  else if (prev.mute !== !!call.selfMute) playSound(call.selfMute ? 'mute' : 'unmute');
}

// Settings → Notifications → Sounds.
function soundsSection(v) {
  const names = {
    message: [t('New message'), t("In a channel you aren't reading.")],
    dm: [t('Direct message'), t("In a conversation you aren't reading.")],
    mention: [t('Mention'), t('A message that mentions you, also in a muted channel.')],
    join: [t('Someone joins your call'), null],
    leave: [t('Someone leaves your call'), null],
    mute: [t('Mute and unmute'), t('Your microphone, in a call.')],
    deafen: [t('Deafen and undeafen'), null],
    call: [t('Call connected and disconnected'), null],
  };
  const vol = h('input', { type: 'range', min: '0', max: '100', step: '5', value: String(v.sound_volume), 'aria-label': t('Sound volume'),
    'aria-valuetext': `${v.sound_volume}%` });
  const volText = h('span', { class: 'small muted range-value', text: `${v.sound_volume}%` });
  vol.addEventListener('input', () => { volText.textContent = `${vol.value}%`; vol.setAttribute('aria-valuetext', `${vol.value}%`); });
  const switches = {};
  const rows = SOUND_SWITCHES.map((key) => {
    const [label, desc] = names[key];
    const box = toggle(!(v.sounds_off || []).includes(key));
    box.setAttribute('aria-label', label);
    box.dataset.sound = key;
    switches[key] = box;
    const play = h('button', { class: 'btn small sound-preview', type: 'button', text: t('Play'), 'aria-label': t('Play the sound: {sound}', { sound: label }),
      onclick: () => soundsUI.board && soundsUI.board.preview(SOUND_PREVIEW[key], Number(vol.value)) });
    return settingRow(label, desc, h('div', { class: 'row sound-switch', role: 'group' }, play, box), async () => {
      const r = await api('set_setting', 'sounds_off', SOUND_SWITCHES.filter((k) => !switches[k].checked));
      v.sounds_off = r.value;
    });
  });
  return formSection(t('Sounds'), [
    vaultSetting('sounds', t('Play sounds'), t('Short, quiet tones made by the app as it runs. They follow your mutes and notification levels above, also with desktop notifications off.'),
      toggle(v.sounds !== false), (c) => c.checked),
    vaultSetting('sound_volume', t('Sound volume'), t('Through the speaker chosen under Voice & video.'),
      h('div', { class: 'row range-row', role: 'group' }, vol, volText), () => Number(vol.value)),
    ...rows,
    h('p', { class: 'small muted', text: t('Do not disturb and deafen silence every sound except those of your own call: mute, deafen, connected and disconnected. Sounds never play for the channel or conversation you are reading.') })]);
}
