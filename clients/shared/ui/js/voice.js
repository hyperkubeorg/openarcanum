// Voice and video calls: the channel list's voice rooms, the voice bar (every
// call control, each with a menu of its options), the call stage (click to
// watch, click to focus), people's menus, push to talk, and Settings →
// Voice & video. Media and encryption run in call.js; Python (voice.py)
// carries signaling and hands over the per-call key.
'use strict';

const voiceUI = {
  state: { active: false }, // the call, from Python (no keys)
  rooms: {}, // cid -> [{channel, participants}]
  call: null, // the running Call (call.js)
  generation: -1,
  videos: new Map(), // `${participant}/${kind}` -> <video>, kept across redraws
  pttDown: false,
  joining: null, // {cid, channel} while the server answers a join
  failure: null, // {cid, channel, text} after a call couldn't connect: shown until dismissed or retried
  reconnecting: false, // the page reconnects a call that was up (a new relay, a rejoin)
  // Videos picked for watching (`${participant}/${kind}`): only these
  // are received. The one in focus is shown large, the others in a strip.
  watching: new Set(),
  focus: null,
  videosOnly: false, // the stage shows only video tiles (stageControls)
  popouts: new Map(),
  railVoice: {}, // community -> its rail badge as last drawn (voice_rooms) // `${participant}/${kind}` -> its pop-out window (popout)

  get current() { return this.state.active ? { cid: this.state.community, channel: this.state.channel, dm: this.state.dm } : null; },

  // In a direct message call (dmcalls.py), in that conversation.
  inDmCall(gid) { return !!(this.state.active && this.state.dm && this.state.dm === gid); },

  // The call's name: the voice channel, or the conversation.
  callName() {
    const s = this.state;
    if (s.dm) {
      const c = typeof dmUI !== 'undefined' && dmUI.list.find((x) => x.id === s.dm);
      return c ? t('Call with {name}', { name: c.name }) : t('Call');
    }
    return this.channelName(s.community, s.channel);
  },

  // Join or start the call in a conversation; any other call is left (one at a time).
  async joinDm(gid, video = false) {
    if (this.inDmCall(gid)) { this.showDmOrStage(); return; }
    if (this.joining) return;
    this.failure = null;
    this.joining = { dm: gid };
    if (typeof dmCallUI !== 'undefined') delete dmCallUI.ringing[gid];
    render();
    try {
      this.state = await api('dm_call_join', gid, video);
      if (st.view !== 'dms' || dmUI.current !== gid) await dmUI.open(gid);
      if (video) setTimeout(() => { if (this.call && !this.call.local.camera) this.call.setCamera(true).catch(toastError); }, 1500);
    } catch (e) {
      this.joining = null;
      toastError(e);
    } finally {
      this.joining = null;
    }
    render();
  },

  // The voice bar shows while in a call, joining one, or after one failed.
  barVisible() {
    // A failure for a channel that is gone (voice turned off) goes with it.
    if (this.failure && this.failure.cid) {
      const s = st.states[this.failure.cid];
      if (s && !s.channels.some((c) => c.id === this.failure.channel)) this.failure = null;
    }
    return !!(this.current || this.joining || this.failure);
  },

  roomOf(cid, channel) {
    const r = (this.rooms[cid] || []).find((x) => x.channel === channel);
    return r ? r.participants : [];
  },

  channelName(cid, channel) {
    const s = st.states[cid];
    const c = s && s.channels.find((x) => x.id === channel);
    return c ? c.name : t('Call');
  },

  // Live speaking, for someone in this device's call.
  speaking(member) {
    if (!this.call || !this.state.active) return false;
    const p = this.call.s.participants.find((x) => x.member === member);
    return !!(p && this.call.speaking(p.participant));
  },

  async join(cid, channel) {
    if (this.current && this.current.cid === cid && this.current.channel === channel) { this.showStage(); return; }
    if (this.joining) return;
    this.failure = null;
    this.joining = { cid, channel };
    renderSidebar();
    try {
      this.state = await api('voice_join', cid, channel);
      this.showStage();
    } catch (e) {
      this.failed(cid, channel, t("Couldn't join {channel}: {error}", { channel: this.channelName(cid, channel), error: e.message || String(e) }));
    } finally {
      this.joining = null;
    }
    renderSidebar();
  },

  async leave() {
    this.failure = null;
    try { await api('voice_leave'); } catch (e) { toastError(e); }
    renderSidebar();
  },

  // A call that couldn't connect: the reason stays in the voice bar (with
  // Try again) and goes in an error toast.
  failed(cid, channel, text) {
    this.failure = { cid, channel, text };
    toastError(new Error(text));
    renderSidebar();
  },

  retry() {
    const f = this.failure;
    if (!f) return;
    this.failure = null;
    if (f.dm) this.joinDm(f.dm);
    else this.join(f.cid, f.channel);
  },

  showStage() { st.stage = true; render(); },

  // From the voice bar: a DM call's stage is in its conversation.
  showDmOrStage() {
    if (this.state.dm) { dmUI.open(this.state.dm); return; }
    this.showStage();
  },

  // ---------------------------------------------------------------- the engine

  async onCall(msg) {
    if (msg.type === 'start') return this.start(msg.session);
    if (msg.type === 'stop') return this.stop();
    if (!this.call || msg.generation !== this.generation) return;
    try {
      if (msg.type === 'offer') await this.call.onRemoteOffer(msg.sdp);
      else if (msg.type === 'ptt') this.call.setPushToTalk(msg.down || this.pttDown);
      else if (msg.type === 'watchers') await this.call.onWatchers(msg.watches);
      else if (msg.type === 'update') await this.call.update(await withDevices(msg.session));
      else if (msg.type === 'settings') await this.applySettings(await withDevices(msg.session));
    } catch (e) { console.error('call', msg.type, e); }
  },

  async start(session) {
    // A new connection for a call that was up (another relay, a rejoin)
    // reads "Reconnecting…", not "Connecting…".
    this.reconnecting = !!(this.call && this.call.everConnected);
    this.stop();
    this.generation = session.generation;
    const gen = session.generation;
    const call = new Call(await withDevices(session), (kind, ...args) => api('call_signal', gen, kind, args), () => this.changed());
    call.onProblem = (p) => this.onProblem(call, p);
    call.onEnded = (kind) => { if (this.call === call) this.streamEnded(kind); };
    call.onNotice = (text) => { if (this.call === call) toast(text); };
    this.call = call;
    try {
      await call.start();
    } catch (e) {
      if (this.call !== call) return;
      this.giveUp(t("Couldn't connect to the call: {error}", { error: e.message || String(e) }));
    }
  },

  // The call can't go on from this device: leave it (nobody sees a ghost in
  // the call), and say why.
  giveUp(text) {
    const s = this.state;
    const cid = s.community;
    const channel = s.channel;
    const dm = s.dm;
    this.stop();
    api('voice_leave').catch(() => {});
    if (dm) { this.failure = { dm, text }; toastError(new Error(text)); render(); return; }
    this.failed(cid, channel, text);
  },

  onProblem(call, p) {
    if (this.call !== call) return;
    if (p.kind === 'udp') {
      this.giveUp(p.address
        ? t("Can't reach the call's media server at {address}. Calls need UDP network traffic, and something is blocking it: often a firewall, a VPN, or a work, school or hotel network. Try another network or turn the VPN off, or ask whoever runs the network to allow UDP to {address}.", { address: p.address })
        : t("Can't reach the call's media server. Calls need UDP network traffic, and something is blocking it: often a firewall, a VPN, or a work, school or hotel network. Try another network or turn the VPN off."));
    } else if (p.kind === 'camera-gone') {
      toast(t('Your camera was disconnected, so it was turned off.'), { title: t('Voice') });
    }
  },

  stop() {
    if (this.call) this.call.close();
    this.call = null;
    for (const k of [...this.popouts.keys()]) this.closePopout(k);
    this.videos.clear();
    this.pttDown = false;
  },

  async applySettings(s) {
    const c = this.call;
    const micChanged = s.mic !== c.s.mic;
    const camChanged = s.camera !== c.s.camera;
    const wasProcessing = c.processing();
    Object.assign(c.s, { mode: s.mode, vadThreshold: s.vadThreshold, pttKey: s.pttKey, screenAudio: s.screenAudio });
    if (s.cameraQuality !== c.s.cameraQuality) await c.setCameraQuality(s.cameraQuality);
    if (s.screenQuality !== c.s.screenQuality) await c.setScreenQuality(s.screenQuality);
    for (const [m, v] of Object.entries(s.volumes || {})) c.setVolume(m, v);
    for (const m of Object.keys(c.volumes)) if (!(m in (s.volumes || {}))) c.setVolume(m, 1);
    const mutes = new Set(s.localMutes || []);
    for (const m of mutes) if (!c.localMutes.has(m)) c.setLocalMute(m, true);
    for (const m of [...c.localMutes]) if (!mutes.has(m)) c.setLocalMute(m, false);
    c.setOutputVolume(s.outputVolume ?? 1);
    // A microphone that can't start is shown in the voice bar; the call
    // goes on. Studio mode opens it again without the engine's processing.
    if (micChanged || wasProcessing !== c.processing()) await c.setMic(s.mic).catch(() => {});
    if (s.speaker !== c.s.speaker) await c.setSpeaker(s.speaker);
    if (camChanged && c.local.camera) await c.setCamera(true, s.camera).catch(toastError);
    c.s.camera = s.camera;
    c.onChange();
  },

  // The engine changed (tracks, speaking, connection): update in place.
  changed() {
    // Connected, and your own mute and deafen, have sounds (sounds.js).
    soundsCallChanged(this.call, this.state);
    clearTimeout(this.redrawTimer);
    this.redrawTimer = setTimeout(() => {
      for (const el of document.querySelectorAll('[data-voice-member]')) {
        el.classList.toggle('speaking', this.speaking(el.dataset.voiceMember));
      }
      // The stage only changes when its tiles do (not on every speaking flip).
      const sig = this.stageSignature();
      // A DM call shows in its conversation whenever that's open; a voice
      // channel's when its stage is asked for.
      const here = this.state.dm ? st.view === 'dms' && dmUI.current === this.state.dm && !!document.getElementById('dm-call')
        : st.stage && st.cid === this.state.community;
      if (this.state.active && here && sig !== this.lastStage) this.renderStage();
      // The bar compares with what it shows: a bar drawn afresh (a rejoin
      // redraws the sidebar) records its own signature.
      if (this.barSignature() !== this.lastBar) {
        const bar = document.querySelector('.voice-bar');
        if (bar) keepingFocus(bar, () => bar.replaceWith(this.bar()));
        else this.lastBar = this.barSignature();
        const ctl = document.querySelector('.stage-controls-host');
        if (ctl && this.state.active) keepingFocus(ctl, () => ctl.replaceChildren(this.stageControls()));
        this.refreshStageHead();
      }
    }, 50);
  },

  barSignature() {
    const c = this.call;
    return JSON.stringify([this.state.active, c && c.phase, c && c.micState, c && c.selfMute, c && c.selfDeaf, !!(c && c.local.camera),
      !!(c && c.local.screen), !!(c && c.priorityOn), this.state.can_priority, this.reconnecting, this.state.server_mute, this.state.listen_only, this.state.can_vad,
      this.state.channel, st.voiceChat, c && c.mode()]);
  },

  // ---------------------------------------------------------------- status words

  // Where the call is, in words: connecting, connected, reconnecting.
  statusText() {
    const c = this.call;
    if (c && c.phase === 'connected') return t('Voice connected');
    if ((c && c.phase === 'reconnecting') || this.reconnecting) return t('Reconnecting…');
    return t('Connecting…');
  },

  micProblem() {
    const c = this.call;
    return c && ['none', 'denied', 'busy', 'failed'].includes(c.micState) ? c.micState : null;
  },

  // ---------------------------------------------------------------- the voice bar

  // Every control of the call is here, in the channel list's voice bar:
  // mute, deafen, camera, screen and priority speaker, each icon with a
  // small arrow (or its right-click) for its options; settings and leave.
  bar() {
    this.lastBar = this.barSignature();
    if (!this.state.active) return this.joining ? this.joiningCard() : this.failureCard();
    const s = this.state;
    const c = this.call;
    const connected = c && c.phase === 'connected';
    const mode = c ? c.mode() : 'vad';
    const note = s.server_mute ? t('server muted') : s.listen_only ? t('listening only') : s.can_vad === false ? t('push to talk only')
      : mode === 'studio' ? t('studio mode') : mode === 'ptt' ? t('push to talk') : '';
    const mic = this.micProblem();
    return h('div', { class: 'voice-bar', role: 'group', 'aria-label': t('Voice call') },
      h('div', { class: 'row' },
        h('span', { class: (connected ? 'ok-text' : 'muted') + ' small voice-status' + (connected ? ' live' : ''), role: 'status', text: this.statusText() }), h('div', { class: 'spacer' }),
        h('button', { class: 'icon-btn', title: t('Voice & video settings'), 'aria-label': t('Voice & video settings'), icon: 'settings', onclick: () => openSettings('voice') }),
        h('button', { class: 'icon-btn leave', title: t('Leave call'), 'aria-label': t('Leave call'), onclick: () => this.leave() }, icon('phone-off'))),
      h('div', { class: 'small muted clickable', title: t('Show the call'), text: this.callName() + (note ? ' · ' + note : ''), onclick: () => this.showDmOrStage() }),
      mic ? this.micNotice(mic) : null,
      h('div', { class: 'row voice-controls' }, ...this.controls()));
  },

  // The call's controls (mute, deafen, camera, screen, priority speaker),
  // each icon with a small arrow (or its right-click) for its options: in
  // the voice bar and under the stage.
  controls() {
    const s = this.state;
    const c = this.call;
    const btn = (title, ic, on, run, extra = {}) => h('button', { class: 'icon-btn' + (on ? ' on' : ''), title, 'aria-label': title, 'aria-pressed': String(!!on), onclick: run, ...extra }, ic);
    const split = (cls, main, label, popup, menu) => {
      main.addEventListener('contextmenu', (e) => { e.preventDefault(); menu(e); });
      return h('div', { class: 'split ' + cls }, main,
        h('button', { class: 'icon-btn split-more', title: label, 'aria-label': label, 'aria-haspopup': popup, onclick: (e) => menu(e) }, icon('chevron-down')));
    };
    return [
      s.listen_only ? null : split('mic',
        btn(c && c.selfMute ? t('Unmute') : t('Mute'), c && c.selfMute ? icon('mic-off') : icon('mic'), c && c.selfMute, () => c && c.setSelfMute(!c.selfMute).catch(toastError)),
        t('Microphone options'), 'dialog', (e) => this.micMenu(e)),
      split('sound',
        btn(c && c.selfDeaf ? t('Undeafen') : t('Deafen'), c && c.selfDeaf ? icon('headphone-off') : icon('headphones'), c && c.selfDeaf, () => c && c.setSelfDeaf(!c.selfDeaf).catch(toastError)),
        t('Sound options'), 'dialog', (e) => this.soundMenu(e)),
      s.can_video ? split('camera',
        btn(c && c.local.camera ? t('Turn off camera') : t('Turn on camera'), icon('video'), c && c.local.camera, () => this.toggleCamera()),
        t('Camera options'), 'menu', (e) => this.cameraMenu(e)) : null,
      s.can_screen ? split('screen',
        btn(c && c.local.screen ? t('Stop sharing') : t('Share your screen'), icon('monitor-up'), c && c.local.screen, () => this.toggleScreen()),
        t('Screen share options'), 'menu', (e) => this.screenMenu(e)) : null,
      s.can_priority ? btn(c && c.priorityOn ? t('Turn off priority speaker') : t('Turn on priority speaker'), icon('megaphone'), c && c.priorityOn, () => c && c.setPriority(!c.priorityOn).catch(toastError)) : null,
    ];
  },

  // Turning the camera or a screen share off asks first: everyone stops
  // seeing it, and a click on the wrong button shouldn't do that unnoticed.
  async toggleCamera() {
    const c = this.call;
    if (!c) return;
    if (c.local.camera && !(await confirmModal(t('Turn off your camera?'), t('Everyone in the call stops seeing you.'), t('Turn off'), false))) return;
    if (this.call === c) c.setCamera(!c.local.camera).catch(toastError);
  },
  async toggleScreen() {
    const c = this.call;
    if (!c) return;
    if (c.local.screen && !(await confirmModal(t('Stop sharing your screen?'), t('Everyone in the call stops seeing it.'), t('Stop sharing'), false))) return;
    if (this.call === c) c.setScreen(!c.local.screen).catch(toastError);
  },
  // A share that ended outside the app (call.js onEnded).
  streamEnded(kind) {
    if (kind === 'screen') toast(t('Your screen share ended: nobody sees it now.'));
  },

  // A video in a window of its own, the same stream (nothing is sent again):
  // an empty window the page fills, which closes with the video.
  popout(key, what) {
    const old = this.popouts.get(key);
    if (old && !old.closed) { old.focus(); return; }
    const v = this.videos.get(key);
    const stream = v && v.srcObject;
    if (!stream) return;
    const w = window.open('', 'openarcanum-' + key.replace(/[^A-Za-z0-9_-]/g, '_'), 'popup,width=960,height=540');
    if (!w) { toast(t("Couldn't open a window for the video: allow pop-ups for this site."), { error: true }); return; }
    // Filled now, and again if the window's blank page loads after opening
    // (the desktop's window does), which replaces what was put in it.
    const fill = () => {
      const d = w.document;
      if (!d || !d.body || d.querySelector('video')) return;
      d.title = what;
      Object.assign(d.documentElement.style, { height: '100%', background: '#000' });
      Object.assign(d.body.style, { margin: '0', height: '100%', background: '#000' });
      const pv = d.createElement('video');
      pv.autoplay = true;
      pv.playsInline = true;
      pv.muted = true; // its sound plays in the call, once
      Object.assign(pv.style, { width: '100%', height: '100%', objectFit: 'contain', display: 'block' });
      d.body.replaceChildren(pv);
      pv.srcObject = stream;
      pv.play().catch(() => {});
    };
    fill();
    this.popouts.set(key, w);
    // Closed by its own close button: the tile's Pop out comes back. A
    // check every second (unload events fire on the first blank page too).
    const watch = setInterval(() => {
      if (this.popouts.get(key) !== w) { clearInterval(watch); return; }
      if (w.closed) { clearInterval(watch); this.popouts.delete(key); this.renderStage(); return; }
      fill();
    }, 1000);
    this.renderStage();
  },
  closePopout(key) {
    const w = this.popouts.get(key);
    this.popouts.delete(key);
    if (w && !w.closed) w.close();
  },

  // Under the stage: the same controls, and leaving, where the call is. A DM
  // call also has Hide chat (the call takes the conversation's room); every
  // call has Videos only (the people without video go from the stage).
  stageControls() {
    const dm = this.state.dm;
    return h('div', { class: 'stage-controls', role: 'group', 'aria-label': t('Call controls') }, ...this.controls(),
      dm ? h('button', { class: 'icon-btn' + (dmUI.chatHidden ? ' on' : ''), title: dmUI.chatHidden ? t('Show chat') : t('Hide chat'),
        'aria-label': dmUI.chatHidden ? t('Show chat') : t('Hide chat'), 'aria-pressed': String(!!dmUI.chatHidden), onclick: () => dmUI.setChatHidden(!dmUI.chatHidden) }, icon('message-square')) : null,
      h('button', { class: 'icon-btn' + (this.videosOnly ? ' on' : ''), title: this.videosOnly ? t('Show everyone') : t('Videos only'),
        'aria-label': this.videosOnly ? t('Show everyone') : t('Videos only'), 'aria-pressed': String(!!this.videosOnly),
        onclick: () => { this.videosOnly = !this.videosOnly; this.renderStage(); } }, icon('users')),
      h('button', { class: 'btn danger stage-leave', onclick: () => this.leave() }, icon('phone-off'), h('span', { text: t('Leave') })));
  },

  // No microphone (or not allowed): you're in the call listening, with a
  // way to fix it without leaving.
  micNotice(problem) {
    const c = this.call;
    return h('div', { class: 'voice-notice small', role: 'status' },
      h('div', { text: mediaProblemText(problem, 'audio') + ' ' + t('You can still hear everyone.') }),
      h('div', { class: 'row' },
        h('button', { class: 'btn small', text: t('Try again'), onclick: () => c && c.retryMic().catch(toastError) }),
        h('button', { class: 'btn small', text: t('Choose microphone'), 'aria-haspopup': 'dialog', onclick: (e) => this.micMenu(e) })));
  },

  joiningCard() {
    const j = this.joining;
    return h('div', { class: 'voice-bar', role: 'group', 'aria-label': t('Voice call') },
      h('div', { class: 'small muted voice-status', role: 'status', text: j.dm ? t('Connecting…') : t('Joining {channel}…', { channel: this.channelName(j.cid, j.channel) }) }));
  },

  failureCard() {
    const f = this.failure;
    return h('div', { class: 'voice-bar failed', role: 'group', 'aria-label': t('Voice call') },
      h('div', { class: 'small danger-text voice-status', role: 'alert', text: f.text }),
      h('div', { class: 'row voice-controls' },
        h('button', { class: 'btn small primary', text: t('Try again'), onclick: () => this.retry() }),
        h('button', { class: 'btn small', text: t('Copy details'), onclick: () => api('copy_text', f.text).then(() => toast(t('Copied.'))).catch(toastError) }),
        h('button', { class: 'btn small', text: t('Dismiss'), onclick: () => { this.failure = null; render(); } })));
  },

  // ---------------------------------------------------------------- the voice bar's menus

  // Where a menu opens: at the pointer for a right-click, else under what
  // was clicked, or above it when there's no room below (the voice bar is
  // at the bottom of the window). Taken before anything is awaited.
  anchor(e) {
    if (e && e.type === 'contextmenu') return { x: e.clientX, y: e.clientY };
    const el = e && e.currentTarget;
    return el && el.getBoundingClientRect ? { rect: el.getBoundingClientRect() } : { x: 0, y: 0 };
  },

  openAt(at, items) {
    if (!at.rect) { openMenu(at.x, at.y, items); return; }
    openMenu(at.rect.left, at.rect.bottom, items);
    const m = document.getElementById('ctx-menu');
    if (!m) return;
    const height = m.getBoundingClientRect().height;
    if (at.rect.bottom + height > window.innerHeight - 8) m.style.top = Math.max(8, at.rect.top - height - 4) + 'px';
  },

  // A device list: System default first, the current one checked. pick
  // gets the device id and its name (saved by name, like Settings).
  async deviceGroup(kind, label, current, pick) {
    let devs = [];
    try { devs = (await navigator.mediaDevices.enumerateDevices()).filter((d) => d.kind === kind && d.deviceId && d.deviceId !== 'default'); } catch (err) { /* none listed */ }
    const name = (d, i) => d.label || (kind === 'audioinput' ? t('Microphone {n}', { n: i + 1 }) : kind === 'audiooutput' ? t('Speaker {n}', { n: i + 1 }) : t('Camera {n}', { n: i + 1 }));
    return { group: label, items: [
      { label: t('System default'), checked: !current, run: () => pick('', 'default') },
      ...devs.map((d, i) => ({ label: name(d, i), checked: current === d.deviceId, run: () => pick(d.deviceId, d.label || 'default') })),
    ] };
  },

  // The microphone: which one, how you talk (voice activity, studio, push
  // to talk), and voice activity's sensitivity. Choices are saved like the
  // ones in Settings.
  async micMenu(e) {
    const at = this.anchor(e);
    const c = this.call;
    if (!c) return;
    const inputs = await this.deviceGroup('audioinput', t('Input device'), c.s.mic, async (id, label) => {
      try {
        await c.setMic(id);
        await api('set_setting', 'voice_input', label);
      } catch (err) { toastError(err); }
    });
    const mode = c.s.mode || 'vad';
    const setMode = (m) => api('set_setting', 'voice_mode', m).catch(toastError);
    this.openAt(at, [
      mode === 'vad' && c.s.perms.vad ? { range: {
        label: t('Voice activity sensitivity'), value: c.s.vadThreshold, min: -100, max: 0, step: 1,
        format: (v) => t('{n} dB', { n: v }),
        input: (v) => { c.s.vadThreshold = v; },
        change: (v) => api('set_setting', 'voice_sensitivity', v).catch(toastError),
      } } : null,
      inputs,
      { group: t('Input mode'), items: [
        { label: t('Voice activity'), checked: mode === 'vad', run: () => setMode('vad') },
        { label: t('Studio'), checked: mode === 'studio', run: () => setMode('studio') },
        { label: t('Push to talk'), checked: mode === 'ptt', run: () => setMode('ptt') },
      ] },
      { separator: true },
      { label: t('Voice & video settings…'), run: () => openSettings('voice') },
    ]);
  },

  // What you hear: the speaker and its volume.
  async soundMenu(e) {
    const at = this.anchor(e);
    const c = this.call;
    if (!c) return;
    const outputs = await this.deviceGroup('audiooutput', t('Output device'), c.s.speaker, async (id, label) => {
      try {
        await c.setSpeaker(id);
        const r = await api('set_setting', 'voice_output', label);
        if (soundsUI.vault) soundsUI.vault.voice_output = r.value;
      } catch (err) { toastError(err); }
    });
    this.openAt(at, [
      { range: {
        label: t('Output volume'), value: Math.round(100 * c.outputVolume), min: 0, max: 200, step: 5,
        format: (v) => `${v}%`,
        input: (v) => c.setOutputVolume(v / 100),
        change: (v) => api('set_setting', 'voice_output_volume', v).catch(toastError),
      } },
      outputs,
      { separator: true },
      { label: t('Voice & video settings…'), run: () => openSettings('voice') },
    ]);
  },

  async cameraMenu(e) {
    const at = this.anchor(e);
    const c = this.call;
    if (!c) return;
    const cams = await this.deviceGroup('videoinput', t('Camera'), c.s.camera, async (id, label) => {
      try {
        if (c.local.camera) await c.setCamera(true, id);
        else c.s.camera = id;
        await api('set_setting', 'video_input', label);
      } catch (err) { toastError(err); }
    });
    const q = c.s.cameraQuality || '720p';
    this.openAt(at, [
      { label: c.local.camera ? t('Turn off camera') : t('Turn on camera'), run: () => this.toggleCamera() },
      cams,
      { separator: true },
      ...cameraQualities().map(([id, label]) => ({ label, toggle: true, checked: q === id, run: () => this.setQuality('camera_quality', id) })),
      { separator: true },
      { label: t('Voice & video settings…'), run: () => openSettings('voice') },
    ]);
  },

  screenMenu(e) {
    const at = this.anchor(e);
    const c = this.call;
    if (!c) return;
    const sound = c.s.screenAudio !== false;
    this.openAt(at, [
      { label: c.local.screen ? t('Stop sharing') : t('Share your screen'), run: () => this.toggleScreen() },
      { separator: true },
      ...screenQualities().map(([id, label]) => ({ label, toggle: true, checked: (c.s.screenQuality || 'balanced') === id, run: () => this.setQuality('screen_quality', id) })),
      { separator: true },
      { label: t('Share sound'), toggle: true, checked: sound, run: async () => {
        try {
          const has = await c.setScreenAudio(!sound);
          await api('set_setting', 'screen_share_audio', !sound);
          if (!sound && c.local.screen && !has) toast(t('The sound is shared from your next screen share.'), { title: t('Voice') });
        } catch (err) { toastError(err); }
      } },
    ]);
  },

  // A quality chosen in the camera or screen menu: applied to the call at
  // once and kept as the setting.
  async setQuality(key, value) {
    const c = this.call;
    try {
      if (c && key === 'camera_quality') await c.setCameraQuality(value);
      if (c && key === 'screen_quality') await c.setScreenQuality(value);
      await api('set_setting', key, value);
    } catch (err) { toastError(err); }
  },

  // ---------------------------------------------------------------- the stage

  // What the stage shows: who, which videos, which are watched.
  stageSignature() {
    const c = this.call;
    return JSON.stringify([this.state.participants ? this.state.participants.map((p) => {
      const me = p.participant === this.state.participant;
      const r = c && c.remote.get(p.participant);
      return [p.participant, p.name, p.selfMute, p.selfDeaf, p.serverMute, p.serverDeaf, p.priority,
        me ? !!(c && c.local.camera) : !!p.camera, me ? !!(c && c.local.screen) : !!p.screen,
        !!(r && r.camera), !!(r && r.screen), !!(c && c.streamMuted.has(p.member)), !!(c && c.localMutes.has(p.member))];
    }) : [], [...this.watching], this.focus]);
  },

  // The stage: the whole main panel for a voice channel; for a DM call, a
  // panel at the top of the conversation (dm.js), its messages below.
  renderStage() {
    const panel = this.state.dm ? document.getElementById('dm-call') : null;
    const host = panel || $('#main');
    let stage = host.querySelector('.stage');
    if (!stage) {
      clear(host);
      if (!panel) {
        host.appendChild(h('div', { class: 'main-head' }, h('span', { class: 'muted' }, icon('volume-2')), h('span', { class: 'title', text: this.callName() }),
          h('span', { class: 'small muted stage-status', role: 'status' }),
          h('div', { class: 'spacer' }), h('span', { class: 'stage-head-tools' })));
      }
      stage = h('div', { class: 'stage', role: 'region', 'aria-label': t('Call') });
      host.append(stage, h('div', { class: 'stage-controls-host' }));
      if (!this.resize) {
        this.resize = new ResizeObserver(() => this.reportSizes());
      }
      this.resize.disconnect(); // the stage before this one is gone
      this.resize.observe(stage);
      this.refreshStageHead();
    }
    const c = this.call;
    const tiles = [];
    const keys = new Set();
    for (const p of this.state.participants) {
      const me = p.participant === this.state.participant;
      const camera = me ? !!(c && c.local.camera) : !!p.camera;
      const screen = me ? !!(c && c.local.screen) : !!p.screen;
      // A camera takes the person's place; a screen is a block of its own.
      tiles.push(camera ? this.streamTile(p, 'camera', me) : this.avatarTile(p, me));
      if (screen) tiles.push(this.streamTile(p, 'screen', me));
      if (camera) keys.add(`${p.participant}/camera`);
      if (screen) keys.add(`${p.participant}/screen`);
    }
    // A video that ended isn't watched any more (or focused), and its element goes.
    for (const k of [...this.popouts.keys()]) if (!keys.has(k)) this.closePopout(k);
    for (const k of [...this.watching]) if (!keys.has(k)) this.watching.delete(k);
    for (const k of [...this.videos.keys()]) if (!keys.has(k)) { this.videos.get(k).srcObject = null; this.videos.delete(k); }
    if (this.focus && !keys.has(this.focus)) this.focus = null;
    // Videos only: the people without one leave the stage (all of them stay
    // when nobody has a video, or the stage would be empty).
    if (this.videosOnly && tiles.some((x) => x.classList.contains('video-tile'))) tiles.splice(0, tiles.length, ...tiles.filter((x) => x.classList.contains('video-tile')));
    this.lastStage = this.stageSignature();
    const focused = this.focus && tiles.find((x) => x.dataset.key === this.focus);
    // The keyboard stays on the same tile (its button's name changes with
    // what a click would do next).
    const was = document.activeElement;
    const wasTile = was && stage.contains(was) && was.closest('[data-key]');
    const wasLabel = was && was.classList.contains('tile-main') ? null : was && was.getAttribute('aria-label');
    if (focused) {
      stage.classList.add('has-focus');
      stage.replaceChildren(focused, h('div', { class: 'stage-strip', role: 'list', 'aria-label': t('Everyone else') },
        ...tiles.filter((x) => x !== focused).map((x) => { x.setAttribute('role', 'listitem'); return x; })));
    } else {
      stage.classList.remove('has-focus');
      stage.replaceChildren(...tiles);
    }
    if (wasTile && !stage.contains(was)) {
      const tile = stage.querySelector(`[data-key="${CSS.escape(wasTile.dataset.key)}"]`);
      const again = tile && ([...tile.querySelectorAll('button')].find((b) => wasLabel && b.getAttribute('aria-label') === wasLabel) || tile.querySelector('.tile-main'));
      if (again) again.focus({ preventScroll: true });
    }
    stage.dataset.count = String(tiles.length);
    const ctl = host.querySelector('.stage-controls-host');
    if (ctl) keepingFocus(ctl, () => ctl.replaceChildren(this.stageControls()));
    this.reportSizes();
  },

  // The stage's status line, and its Chat button (every call control is in
  // the voice bar).
  refreshStageHead() {
    const status = document.querySelector('#main .stage-status');
    if (status) {
      const mic = this.micProblem();
      status.textContent = this.statusText() + (mic ? ' · ' + mediaProblemText(mic, 'audio') : '');
    }
    const tools = document.querySelector('#main .stage-head-tools');
    if (!tools || !this.state.active) return;
    const s = this.state;
    if (s.dm) {
      // A DM call: back to the conversation (the call goes on).
      keepingFocus(tools, () => tools.replaceChildren(h('button', { class: 'icon-btn', title: t('Show messages'), 'aria-label': t('Show messages'),
        onclick: () => { st.stage = false; render(); } }, icon('message-square'))));
      return;
    }
    const chatOpen = st.voiceChat === s.channel && voiceChat.shown();
    keepingFocus(tools, () => tools.replaceChildren(h('button', { class: 'icon-btn' + (chatOpen ? ' on' : ''), title: t('Chat'), 'aria-label': t('Chat'),
      'aria-pressed': String(!!chatOpen), onclick: () => voiceChat.toggle(s.channel) }, icon('message-square'))));
  },

  // Click a video to watch it and bring it to the front; click the one in
  // front (or press Escape) to put it back among the others.
  tileClick(key, me) {
    if (!me && !this.watching.has(key)) { this.watching.add(key); this.focus = key; } else this.focus = this.focus === key ? null : key;
    this.renderStage();
  },

  stopWatching(key) {
    this.closePopout(key);
    this.watching.delete(key);
    if (this.focus === key) this.focus = null;
    this.renderStage();
  },

  // A camera (in the person's place) or a shared screen (a block of its
  // own, marked Live). Someone else's is only received once clicked: until
  // then it shows who and "Click to watch". Your own shows its preview.
  streamTile(p, kind, me) {
    const key = `${p.participant}/${kind}`;
    const c = this.call;
    const r = c && c.remote.get(p.participant);
    const watched = me || this.watching.has(key);
    const focused = this.focus === key;
    const name = p.name || t('Someone');
    const stream = me ? (c && c.local[kind] ? new MediaStream([c.local[kind]]) : null) : r && r[kind];
    let media;
    if (watched && stream) {
      let v = this.videos.get(key);
      if (!v) {
        v = h('video', { autoplay: true, playsinline: true, muted: true });
        this.videos.set(key, v);
      }
      if (v.srcObject !== stream && (!v.srcObject || v.srcObject.getTracks()[0] !== stream.getTracks()[0])) v.srcObject = stream;
      v.play().catch(() => {});
      media = v;
    } else if (watched) {
      media = h('div', { class: 'tile-wait muted small', text: t('Loading…') });
    } else {
      media = h('div', { class: 'tile-cover' }, kind === 'screen' ? h('span', { class: 'tile-cover-icon' }, icon('monitor')) : avatarEl(name, p.member, 'lg'),
        h('span', { class: 'tile-hint small', 'aria-hidden': 'true' }, icon('play'), h('span', { text: t('Click to watch') })));
    }
    const what = kind === 'screen' ? t("{name}'s screen", { name }) : t("{name}'s camera", { name });
    const label = !watched ? t('Watch {what}', { what }) : focused ? t('Show {what} with everyone', { what }) : t('Focus on {what}', { what });
    const tools = [];
    if (!me && watched && kind === 'camera') {
      tools.push(h('button', { class: 'icon-btn tile-btn', title: t('Video quality'), 'aria-label': t('Video quality'), 'aria-haspopup': 'menu',
        onclick: (e) => this.openAt(this.anchor(e), this.receiveQualityEntries(p, kind)) }, icon('settings')));
    }
    if (watched && stream) tools.push(h('button', { class: 'icon-btn tile-btn' + (this.popouts.has(key) ? ' on' : ''), title: t('Pop out'), 'aria-label': t('Pop out {what}', { what }), onclick: () => this.popout(key, what) }, icon('monitor')));
    if (!me && watched) tools.push(h('button', { class: 'icon-btn tile-btn', title: t('Stop watching'), 'aria-label': t('Stop watching {what}', { what }), onclick: () => this.stopWatching(key) }, icon('eye-off')));
    if (!me && kind === 'screen' && watched) {
      const muted = !!(c && c.streamMuted.has(p.member));
      tools.push(h('button', { class: 'icon-btn tile-btn' + (muted ? ' on' : ''), title: muted ? t('Unmute stream') : t('Mute stream'), 'aria-label': muted ? t('Unmute stream') : t('Mute stream'),
        'aria-pressed': String(muted), onclick: () => c && c.setStreamMuted(p.member, !muted) }, muted ? icon('volume-x') : icon('volume-2')));
      tools.push(h('button', { class: 'icon-btn tile-btn', title: t('Stream volume'), 'aria-label': t('Stream volume'), 'aria-haspopup': 'dialog',
        onclick: (e) => this.openAt(this.anchor(e), [this.streamVolumeEntry(p), { label: muted ? t('Unmute stream') : t('Mute stream'), toggle: true, checked: muted, run: () => c && c.setStreamMuted(p.member, !muted) },
          { separator: true }, ...this.receiveQualityEntries(p, kind)]) }, icon('chevron-down')));
    }
    return h('div', { class: 'tile video-tile' + (kind === 'screen' ? ' screen' : '') + (watched ? ' watched' : ' unwatched') + (focused ? ' focused' : '') + (kind === 'camera' && this.speaking(p.member) ? ' speaking' : ''),
      ...(kind === 'camera' ? { 'data-voice-member': p.member } : {}), 'data-participant': p.participant, 'data-kind': kind, 'data-me': String(me), 'data-key': key,
      oncontextmenu: (e) => { e.preventDefault(); if (!me) this.memberMenu(e, p); } },
    media,
    h('button', { class: 'tile-main', 'aria-label': label, onclick: () => this.tileClick(key, me) }),
    h('div', { class: 'tile-name' }, kind === 'screen' ? liveBadge() : null, h('span', { text: kind === 'screen' ? t('{name}\'s screen', { name }) : name }), ...(kind === 'camera' ? flagIcons(p) : [])),
    tools.length ? h('div', { class: 'tile-tools' }, ...tools) : null);
  },

  avatarTile(p, me) {
    return h('div', { class: 'tile' + (this.speaking(p.member) ? ' speaking' : ''), 'data-voice-member': p.member, 'data-key': `${p.participant}/avatar`,
      oncontextmenu: (e) => { e.preventDefault(); if (!me) this.memberMenu(e, p); } },
    avatarEl(p.name || '?', p.member, 'lg'),
    h('div', { class: 'tile-name' }, h('span', { text: p.name || t('Someone') }), ...flagIcons(p)));
  },

  // Each watched remote video asks the relay for the layer its tile can
  // show; videos nobody here watches aren't received at all.
  reportSizes() {
    const c = this.call;
    if (!c) return;
    const seen = new Set();
    for (const tile of document.querySelectorAll('.stage .video-tile.watched[data-me="false"]')) {
      const k = `${tile.dataset.participant}/${tile.dataset.kind}`;
      seen.add(k);
      c.watch(tile.dataset.participant, tile.dataset.kind, tile.clientWidth * (window.devicePixelRatio || 1));
    }
    for (const k of [...c.layers.keys()]) if (!seen.has(k)) { const [pp, kk] = k.split('/'); c.unwatch(pp, kk); }
  },

  // ---------------------------------------------------------------- people

  // A person in the call (right-click, or their ⋯): their volume and
  // stream's volume, Mute for me (this device only, not a server mute), and
  // what a moderator may do.
  // A participant's menu: everything their menu has elsewhere (Message,
  // profile, ...), then what's about the call.
  async memberMenu(e, p) {
    const x = e.clientX;
    const y = e.clientY;
    let person = [];
    try {
      person = this.state.dm ? await userMenuItems({ person: { id: p.member, name: p.name } }, { dm: dmUI.info }) : await userMenuItems({ cid: st.cid, key: p.member }, {});
    } catch (err) { console.error('menu', err); }
    const summary = st.communities.find((x) => x.id === st.cid);
    const can = (summary && summary.can) || {};
    const inMyCall = this.state.active && this.state.participants.some((x) => x.member === p.member);
    const self = cstate() && cstate().self;
    const mine = !!self && p.member === self.key;
    const voiceChannels = (cstate() ? cstate().channels : []).filter((c) => c.kind === 'voice');
    const c = this.call;
    const local = inMyCall && !mine && c;
    const mutedForMe = !!(local && c.localMutes.has(p.member));
    const streaming = local && this.state.participants.some((x) => x.member === p.member && x.screen);
    const streamMuted = !!(local && c.streamMuted.has(p.member));
    openMenu(x, y, [
      ...person,
      person.length ? { separator: true } : null,
      local ? this.volumeEntry(p) : null,
      streaming ? this.streamVolumeEntry(p) : null,
      local ? { label: t('Mute for me'), toggle: true, checked: mutedForMe, run: () => this.muteForMe(p, !mutedForMe) } : null,
      streaming ? { label: t('Mute stream'), toggle: true, checked: streamMuted, run: () => c.setStreamMuted(p.member, !streamMuted) } : null,
      local && (can.mute_members || can.deafen_members || can.move_members) ? { separator: true } : null,
      can.mute_members ? { label: p.serverMute ? t('Server unmute') : t('Server mute'), run: () => this.moderate(p, 'mute', !p.serverMute) } : null,
      can.deafen_members ? { label: p.serverDeaf ? t('Server undeafen') : t('Server deafen'), run: () => this.moderate(p, 'deafen', !p.serverDeaf) } : null,
      can.move_members ? { label: t('Move to…'), run: () => this.moveDialog(p, voiceChannels) } : null,
      can.move_members ? { label: t('Disconnect'), danger: true, run: () => this.moderate(p, 'disconnect') } : null,
    ]);
  },

  // Mute someone on this device only: not played, not even received, and
  // kept (on this device) for later calls. Nobody else is told.
  async muteForMe(p, on) {
    if (this.call) this.call.setLocalMute(p.member, on);
    try { await api('voice_local_mute', p.member, on); } catch (e) { toastError(e); }
    renderSidebar();
  },

  async moderate(p, action, value) {
    try { await api('voice_moderate', st.cid, p.member, action, value === undefined ? null : value); } catch (e) { toastError(e); }
  },

  moveDialog(p, channels) {
    const sel = selectEl(channels.map((c) => [String(c.id), c.name]), '');
    sel.setAttribute('aria-label', t('Voice channel'));
    modal({
      title: t('Move {name}', { name: p.name || t('them') }),
      body: h('div', { class: 'stack' }, field(t('Voice channel'), sel)),
      actions: [{ label: t('Cancel') }, { label: t('Move'), kind: 'primary', run: () => this.moderate(p, 'move', Number(sel.value)) }],
    });
  },

  // The volume slider right in the person's menu: heard as it moves, kept
  // (on this device) when let go.
  volumeEntry(p) {
    const cur = Math.round(100 * ((this.call && this.call.volumes[p.member]) ?? 1));
    return { range: {
      label: t('Volume for {name}', { name: p.name || t('them') }), value: cur, min: 0, max: 200, step: 5,
      format: (v) => `${v}%`,
      input: (v) => { if (this.call) this.call.setVolume(p.member, v / 100); },
      change: (v) => api('voice_volume', p.member, v / 100).catch(toastError),
    } };
  },

  // Their screen share's sound, on this device, for this call.
  // What's received of someone's video: automatic (the tile's size and the
  // connection decide), or a quality chosen here.
  receiveQualityEntries(p, kind) {
    const c = this.call;
    const now = c ? c.maxLayer.get(`${p.participant}/${kind}`) : undefined;
    return [[null, t('Quality: automatic')], [2, t('Quality: high')], [1, t('Quality: medium')], [0, t('Quality: low')]]
      .map(([level, label]) => ({ label, toggle: true, checked: (now === undefined ? null : now) === level, run: () => c && c.setReceiveQuality(p.participant, kind, level) }));
  },

  streamVolumeEntry(p) {
    const cur = Math.round(100 * ((this.call && this.call.streamVolumes[p.member]) ?? 1));
    return { range: {
      label: t('Stream volume for {name}', { name: p.name || t('them') }), value: cur, min: 0, max: 200, step: 5,
      format: (v) => `${v}%`,
      input: (v) => { if (this.call) this.call.setStreamVolume(p.member, v / 100); },
      change: () => {},
    } };
  },
};

// A participant's state as an icon, named for screen readers (and on hover).
// The choices for what's sent (settings camera_quality and screen_quality).
function cameraQualities() { return [['480p', t('480p')], ['720p', t('720p (HD)')], ['1080p', t('1080p (Full HD)')]]; }
function screenQualities() {
  return [['text', t('Sharp text (5 frames a second)')], ['balanced', t('Balanced (15 frames a second)')], ['motion', t('Smooth motion (30 frames a second)')]];
}

function flag(label, ic, cls = '') {
  return h('span', { class: 'flag' + cls, role: 'img', title: label, 'aria-label': label }, ic);
}

// Sharing their screen: the Live badge (in the channel list and on the
// stream's block).
function liveBadge() {
  return h('span', { class: 'live-badge', title: t('Sharing their screen'), text: t('Live') });
}

function flagIcons(p, opts = {}) {
  const f = [];
  if (p.serverMute) f.push(flag(t('Server muted'), icon('mic-off'), ' danger-text'));
  else if (p.selfMute) f.push(flag(t('Muted'), icon('mic-off')));
  if (opts.mutedForMe) f.push(flag(t('Muted for you'), icon('volume-x')));
  if (p.serverDeaf) f.push(flag(t('Server deafened'), icon('headphone-off'), ' danger-text'));
  else if (p.selfDeaf) f.push(flag(t('Deafened'), icon('headphone-off')));
  if (p.camera) f.push(flag(t('Camera on'), icon('video')));
  if (p.priority) f.push(flag(t('Priority speaker'), icon('megaphone')));
  return f;
}

// Device settings are stored by label (device ids differ every run in a
// private profile); here they become this run's ids.
async function withDevices(session) {
  const out = Object.assign({}, session);
  let devs = [];
  try { devs = await navigator.mediaDevices.enumerateDevices(); } catch (e) { /* no devices */ }
  const pick = (kind, label) => {
    if (!label || label === 'default') return '';
    const d = devs.find((x) => x.kind === kind && x.label === label);
    return d ? d.deviceId : '';
  };
  out.micLabel = session.mic;
  out.mic = pick('audioinput', session.mic);
  out.speaker = pick('audiooutput', session.speaker);
  out.camera = pick('videoinput', session.camera);
  return out;
}

// Rooms in the channel list (under each voice channel). Someone sharing
// their screen has the Live badge.
function voiceRoomRows(cid, channel) {
  const parts = voiceUI.roomOf(cid, channel);
  if (!parts.length) return null;
  const c = voiceUI.call;
  return h('div', { class: 'voice-users', role: 'list', 'aria-label': t('In the call') }, ...parts.map((p) => h('div', {
    role: 'listitem', class: 'voice-user' + (voiceUI.speaking(p.member) ? ' speaking' : ''), 'data-voice-member': p.member,
    oncontextmenu: (e) => { e.preventDefault(); voiceUI.memberMenu(e, p); },
  }, avatarEl(p.name || '?', p.member, 'xs'), h('span', { class: 'spacer', text: p.name || t('Someone') }), p.screen ? liveBadge() : null,
  ...flagIcons(p, { mutedForMe: !!(c && voiceUI.state.active && voiceUI.state.community === cid && c.localMutes.has(p.member)) }),
  h('button', { class: 'icon-btn hover-only', title: t('Options for {name}', { name: String(p.name) }), 'aria-label': t('Options for {name}', { name: String(p.name) }), icon: 'ellipsis', onclick: (e) => { e.stopPropagation(); voiceUI.memberMenu(e, p); } }))));
}

on('voice', (s) => {
  const was = voiceUI.state.active;
  voiceUI.state = s;
  if (!s.active) {
    voiceUI.reconnecting = false;
    voiceUI.watching.clear();
    voiceUI.focus = null;
  }
  if (!s.active && st.stage) st.stage = false;
  if (s.active || was) render();
});
on('voice_rooms', (d) => {
  voiceUI.rooms[d.community] = d.rooms;
  // Every community's rail icon shows who's in voice (app.js voiceBadge):
  // drawn again only when that changes (not on every mute).
  const c = st.communities.find((x) => x.id === d.community);
  const sig = JSON.stringify(c ? voiceActivity(c) : null);
  if (voiceUI.railVoice[d.community] !== sig) { voiceUI.railVoice[d.community] = sig; renderRail(); }
  if (d.community !== st.cid) return;
  renderSidebar();
  if (voiceChat.shown()) voiceChat.refreshWho();
});
on('call', (msg) => { voiceUI.onCall(msg).catch((e) => console.error('call', e)); });
on('screen_pick', (d) => screenPicker(d));

// Escape puts the video in focus back among the others (when nothing else,
// a menu or a dialog, is there to take it).
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape' || !voiceUI.focus || !st.stage || e.defaultPrevented) return;
  if (document.querySelector('#ctx-menu, #modal-root .backdrop')) return;
  const target = e.target;
  if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return;
  voiceUI.focus = null;
  voiceUI.renderStage();
});

// The app's own screen and window picker. The engine waits (app.py) while
// it's open; the list grows as the engine finds windows.
let picker = null;
// The last choice, by kind and name, and a choice to make again by itself:
// a capture with sound that the system refused is tried once more without
// it (call.js setScreen), on the same screen or window.
const screenChoice = { last: null, again: null };
function screenPicker(d) {
  if (d.closed) { if (picker) picker.close(); picker = null; return; }
  const again = screenChoice.again;
  if (again && !picker) {
    const i = (again.kind === 'screen' ? d.screens : d.windows).indexOf(again.name);
    if (i >= 0) { screenChoice.again = null; api('screen_pick', again.kind, i).catch(() => {}); return; }
    // Windows show up in the list as the engine finds them: wait a moment
    // for it before asking again.
    if (Date.now() - again.at < 5000) { setTimeout(() => { if (screenChoice.again === again && !picker) screenPicker(d); }, 500); return; }
    screenChoice.again = null;
  }
  const pick = (kind, i) => {
    const p = picker;
    picker = null;
    screenChoice.last = { kind, name: (kind === 'screen' ? d.screens : d.windows)[i] };
    api('screen_pick', kind, i).catch(() => {});
    if (p) p.close();
  };
  const items = [
    ...d.screens.map((name, i) => h('button', { class: 'btn', text: t('Screen: {name}', { name: name || i + 1 }), onclick: () => pick('screen', i) })),
    ...d.windows.map((name, i) => h('button', { class: 'btn', text: t('Window: {name}', { name: name || t('untitled') }), onclick: () => pick('window', i) })),
  ];
  if (picker) { picker.list.replaceChildren(...items); return; }
  const list = h('div', { class: 'stack' }, ...items);
  // Whether the screen's sound goes too, chosen with the screen.
  const c = voiceUI.call;
  const sound = checkbox(t('Share sound'), !c || c.s.screenAudio !== false, t('What the screen or window plays, for everyone watching.'));
  sound.input.addEventListener('change', () => {
    const on = sound.input.checked;
    if (voiceUI.call) voiceUI.call.s.screenAudio = on;
    api('set_setting', 'screen_share_audio', on).catch(toastError);
  });
  const box = modal({ title: t('Share your screen'), body: h('div', { class: 'stack' }, list, sound), actions: [{ label: t('Cancel') }],
    onclose: () => { if (picker) { picker = null; api('screen_pick', '', 0).catch(() => {}); } } });
  picker = { list, close: () => box.close() };
}

// ---------------------------------------------------------------- push to talk

function keyName(e) { return e.key === ' ' ? 'Space' : e.key.length === 1 ? e.key.toUpperCase() : e.key; }

// The extra mouse buttons (back, forward) as key names; null for the left,
// right and middle buttons, which are never push to talk (hotkey.py).
function mouseButtonName(e) { return e.button === 3 ? 'Mouse4' : e.button === 4 ? 'Mouse5' : null; } // i18n: not shown (setting values, hotkey.py MOUSE_BUTTONS)

function isPtt(e) {
  const c = voiceUI.call;
  if (!c || c.mode() !== 'ptt') return false;
  const target = e.target;
  if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return false;
  return keyName(e) === (c.s.pttKey || 'Space');
}
document.addEventListener('keydown', (e) => { if (isPtt(e)) { e.preventDefault(); if (!voiceUI.pttDown) { voiceUI.pttDown = true; voiceUI.call.setPushToTalk(true); } } });
document.addEventListener('keyup', (e) => { if (isPtt(e) && voiceUI.pttDown) { voiceUI.pttDown = false; voiceUI.call.setPushToTalk(false); } });
// An extra mouse button as the key: held while the pointer is over the app
// (hotkey.py watches it elsewhere). Its default (back, forward) is cancelled.
function isPttButton(e) {
  const c = voiceUI.call;
  const name = mouseButtonName(e);
  return !!(c && name && c.mode() === 'ptt' && name === (c.s.pttKey || 'Space'));
}
document.addEventListener('pointerdown', (e) => {
  if (!isPttButton(e)) return;
  e.preventDefault();
  if (!voiceUI.pttDown) { voiceUI.pttDown = true; voiceUI.call.setPushToTalk(true); }
});
document.addEventListener('pointerup', (e) => {
  if (!isPttButton(e)) return;
  e.preventDefault();
  if (voiceUI.pttDown) { voiceUI.pttDown = false; voiceUI.call.setPushToTalk(false); }
});
for (const ev of ['mouseup', 'auxclick']) document.addEventListener(ev, (e) => { if (isPttButton(e)) e.preventDefault(); });
window.addEventListener('blur', () => { if (voiceUI.pttDown && voiceUI.call) { voiceUI.pttDown = false; voiceUI.call.setPushToTalk(false); } });

// ---------------------------------------------------------------- settings

async function renderVoiceSettings(sec) {
  const v = settingsUI.data.vault;
  // Device names need permission; the page has it while this section is open.
  await api('media_access', true);
  settingsUI.onClose(() => { if (!voiceUI.call) api('media_access', false).catch(() => {}); });
  // Each kind on its own: no microphone still lists the speakers and
  // cameras (and the other way round).
  const probe = async (kind) => {
    try {
      const s = await navigator.mediaDevices.getUserMedia({ [kind]: true });
      s.getTracks().forEach((track) => track.stop());
      return null;
    } catch (e) { return mediaProblem(e); }
  };
  const micProblem = await probe('audio');
  if (micProblem) {
    sec.appendChild(h('div', { class: 'notice voice-device-notice', role: 'note', text: mediaProblemText(micProblem, 'audio') + ' ' + t('You can still join calls and listen.') }));
    await probe('video');
  }
  let devs = [];
  try { devs = await navigator.mediaDevices.enumerateDevices(); } catch (e) { /* none listed */ }
  const options = (kind, cur) => {
    const list = [['default', t('System default')], ...devs.filter((d) => d.kind === kind && d.label && d.deviceId !== 'default').map((d) => [d.label, d.label])];
    if (cur && cur !== 'default' && !list.some((o) => o[0] === cur)) list.push([cur, t('{device} (not connected)', { device: cur })]);
    const sel = selectEl(list, cur);
    sel.dataset.devices = kind; // the options are the system's device names
    return sel;
  };
  const outVol = h('input', { type: 'range', min: '0', max: '200', step: '5', value: String(v.voice_output_volume), 'aria-label': t('Output volume'),
    'aria-valuetext': `${v.voice_output_volume}%` });
  const outVolText = h('span', { class: 'small muted range-value', text: `${v.voice_output_volume}%` });
  outVol.addEventListener('input', () => {
    outVolText.textContent = `${outVol.value}%`;
    outVol.setAttribute('aria-valuetext', `${outVol.value}%`);
    if (voiceUI.call) voiceUI.call.setOutputVolume(Number(outVol.value) / 100);
  });
  sec.appendChild(formSection(t('Devices'), [
    vaultSetting('voice_input', t('Microphone'), null, options('audioinput', v.voice_input)),
    vaultSetting('voice_output', t('Speaker'), null, options('audiooutput', v.voice_output)),
    vaultSetting('voice_output_volume', t('Output volume'), t('How loud calls play on this device.'),
      h('div', { class: 'row range-row', role: 'group' }, outVol, outVolText), () => Number(outVol.value)),
    vaultSetting('video_input', t('Camera'), null, options('videoinput', v.video_input)),
    vaultSetting('camera_quality', t('Camera quality'), t('What your camera sends in calls. Higher needs a faster connection.'),
      selectEl(cameraQualities(), v.camera_quality)),
    vaultSetting('screen_quality', t('Screen share quality'), t('Sharp text sends few frames a second; smooth motion is for video and games.'),
      selectEl(screenQualities(), v.screen_quality))]));
  const input = [];
  input.push(vaultSetting('voice_mode', t('Input mode'),
    t('Voice activity sends when you speak louder than the sensitivity below. Studio sends any sound at all, with no threshold and none of the processing below: for microphones and interfaces with their own gate and processing (silence, or a muted microphone, sends nothing). Push to talk sends while you hold the key.'),
    selectEl([['vad', t('Voice activity')], ['studio', t('Studio')], ['ptt', t('Push to talk')]], v.voice_mode)));
  // A key, or an extra mouse button (back, forward) pressed on the box; the
  // left, right and middle buttons just click it.
  const pick = (name) => { keyBox.value = name; keyBox.dispatchEvent(new Event('change')); };
  const keyBox = h('input', { type: 'text', class: 'key-box', value: v.ptt_key, readOnly: true, 'aria-label': t('Push-to-talk key'),
    onkeydown: (e) => { e.preventDefault(); pick(keyName(e)); },
    onmousedown: (e) => { const name = mouseButtonName(e); if (name) { e.preventDefault(); pick(name); } },
    onauxclick: (e) => { if (mouseButtonName(e)) e.preventDefault(); } });
  // When the system can't watch this key in other windows, it says so.
  const keyNote = h('div', { class: 'notice ptt-key-unavailable', role: 'note' });
  const showKeyNote = (why) => { keyNote.textContent = why || ''; keyNote.hidden = !why; };
  showKeyNote(settingsUI.data.ptt_key_unavailable);
  input.push(settingRow(t('Push-to-talk key'), t('Click the box and press a key, or an extra mouse button (back or forward).'), keyBox, async () => {
    const r = await api('set_setting', 'ptt_key', keyBox.value);
    settingsUI.data.vault.ptt_key = r.value;
    keyBox.value = r.value;
    settingsUI.data.ptt_key_unavailable = (await api('get_settings')).ptt_key_unavailable;
    showKeyNote(settingsUI.data.ptt_key_unavailable);
  }), keyNote);
  const pttGlobal = toggle(v.ptt_global);
  input.push(vaultSetting('ptt_global', t('Push to talk in other windows'),
    t('Hold the key while another app is focused. Works on Windows, X11 and macOS (after allowing OpenArcanum under Privacy & Security → Input Monitoring); Wayland only allows it while OpenArcanum is focused.'),
    pttGlobal, (c) => c.checked));
  // Where the system can't do it at all (a Wayland session, no X11), why
  // (translated by Python).
  const pttWhy = settingsUI.data.ptt_global_unavailable;
  if (pttWhy) {
    pttGlobal.disabled = true;
    input.push(h('div', { class: 'notice ptt-global-unavailable', role: 'note', text: pttWhy }));
  }
  // Sensitivity: the level above which voice activity sends, with a meter.
  const meter = h('i');
  const mark = h('b', { class: 'level-mark' });
  const slider = h('input', { type: 'range', min: '-100', max: '0', step: '1', value: String(v.voice_sensitivity), 'aria-label': t('Voice activity sensitivity') });
  const place = () => { mark.style.left = `${100 + Number(slider.value)}%`; };
  place();
  slider.addEventListener('input', place);
  input.push(vaultSetting('voice_sensitivity', t('Voice activity sensitivity'),
    t('Sound louder than the mark counts as speaking. Speak normally and set the mark just below your voice, and above the room’s noise when you are quiet.'),
    h('div', { class: 'stack sensitivity' }, slider, h('div', { class: 'level' }, meter, mark)), () => Number(slider.value)));
  input.push(vaultSetting('screen_share_audio', t('Share sound with your screen'),
    t('A screen share also sends what the screen or window plays. You can change it when you share, and in the screen button’s menu.'),
    toggle(v.screen_share_audio), (c) => c.checked));
  sec.appendChild(formSection(null, input));

  // Checks: the microphone's level, a sound through the speaker, and the
  // camera's picture, with the devices chosen above.
  let test = null;
  const testBtn = h('button', { class: 'btn small', text: t('Test microphone'), onclick: async () => {
    if (test) { test.stop(); test = null; testBtn.textContent = t('Test microphone'); return; }
    try { test = await micMeter(settingsUI.data.vault.voice_input, meter, settingsUI.data.vault.voice_mode !== 'studio'); testBtn.textContent = t('Stop test'); } catch (e) { toastError(new Error(mediaProblemText(mediaProblem(e), 'audio'))); }
  } });
  const checks = [h('div', { class: 'setting-row' }, h('div', null, h('div', { text: t('Microphone check') }),
    h('div', { class: 'desc', text: t('Speak: the meter above moves with your voice.') })), testBtn)];
  const soundBtn = h('button', { class: 'btn small', text: t('Play test sound'), onclick: () => testSound(settingsUI.data.vault.voice_output).catch(toastError) });
  checks.push(h('div', { class: 'setting-row' }, h('div', null, h('div', { text: t('Speaker check') }),
    h('div', { class: 'desc', text: t('You should hear a short chime.') })), soundBtn));
  const preview = h('video', { class: 'camera-preview hidden', autoplay: true, playsinline: true, muted: true, 'aria-label': t('Camera preview') });
  let cam = null;
  const camBtn = h('button', { class: 'btn small', text: t('Test camera'), onclick: async () => {
    if (cam) { cam.stop(); cam = null; camBtn.textContent = t('Test camera'); return; }
    try { cam = await cameraPreview(settingsUI.data.vault.video_input, preview); camBtn.textContent = t('Stop camera'); } catch (e) { toastError(new Error(mediaProblemText(mediaProblem(e), 'video'))); }
  } });
  checks.push(h('div', { class: 'setting-row' }, h('div', { text: t('Camera check') }), camBtn), preview);
  sec.appendChild(formSection(t('Device checks'), checks));
  settingsUI.onClose(() => { if (test) test.stop(); if (cam) cam.stop(); });
  sec.appendChild(h('p', { class: 'small muted', text: t('Calls are end-to-end encrypted: only the people in the call can hear and see it, not the servers that carry it. Echo cancellation, noise suppression and automatic volume are on, except in studio mode.') }));
  sec.appendChild(h('p', { class: 'small muted', text: t('Your camera or screen is sent only while someone is watching it, and your voice only while someone can hear it.') }));
  sec.appendChild(h('p', { class: 'small muted', text: t('Calls need a kind of network traffic (UDP) that some firewalls, VPNs and networks block. If calls never connect, that may be why.') }));
}

async function deviceId(kind, label) {
  const devs = await navigator.mediaDevices.enumerateDevices();
  const d = devs.find((x) => x.kind === kind && x.label === label);
  return d ? d.deviceId : '';
}

// A live input meter for the settings page (the same processing as calls:
// none in studio mode).
async function micMeter(label, bar, processing = true) {
  const id = await deviceId('audioinput', label);
  const stream = await navigator.mediaDevices.getUserMedia({ audio: { deviceId: id ? { exact: id } : undefined, echoCancellation: processing, noiseSuppression: processing, autoGainControl: processing } });
  const ctx = new AudioContext();
  const an = ctx.createAnalyser();
  an.fftSize = 512;
  ctx.createMediaStreamSource(stream).connect(an);
  const buf = new Float32Array(an.fftSize);
  const timer = setInterval(() => { bar.style.width = `${Math.max(0, 100 + levelDb(an, buf))}%`; }, 50);
  return { stop() { clearInterval(timer); stream.getTracks().forEach((track) => track.stop()); ctx.close().catch(() => {}); bar.style.width = '0'; } };
}

// A short two-note chime through the chosen speaker.
async function testSound(label) {
  const ctx = new AudioContext();
  const id = await deviceId('audiooutput', label);
  if (id && ctx.setSinkId) await ctx.setSinkId(id).catch(() => {});
  await ctx.resume().catch(() => {});
  const now = ctx.currentTime;
  for (const [i, freq] of [660, 880].entries()) {
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.frequency.value = freq;
    gain.gain.setValueAtTime(0.0001, now + i * 0.25);
    gain.gain.exponentialRampToValueAtTime(0.3, now + i * 0.25 + 0.02);
    gain.gain.exponentialRampToValueAtTime(0.0001, now + i * 0.25 + 0.4);
    osc.connect(gain).connect(ctx.destination);
    osc.start(now + i * 0.25);
    osc.stop(now + i * 0.25 + 0.45);
  }
  setTimeout(() => ctx.close().catch(() => {}), 1200);
}

// The chosen camera's picture in the settings page.
async function cameraPreview(label, video) {
  const id = await deviceId('videoinput', label);
  const stream = await navigator.mediaDevices.getUserMedia({ video: { ...CALL.CAMERA, deviceId: id ? { exact: id } : undefined } });
  video.srcObject = stream;
  video.classList.remove('hidden');
  video.play().catch(() => {});
  return { stop() { stream.getTracks().forEach((track) => track.stop()); video.srcObject = null; video.classList.add('hidden'); } };
}
