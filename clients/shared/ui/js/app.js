// App shell: global state, community rail, routing and Python events.
// Globals are intentional: `st` and the helpers can be poked at from
// DevTools (the page is plain scripts over file://, no modules).
'use strict';

const st = {
  status: null,
  communities: [],
  cid: null,
  channel: null,
  states: {},
  msgs: {},
  members: {},
  view: 'community',
  replyTo: null,
  showMembers: false,
  voiceChat: null, // the voice channel whose chat is open in the side panel
};

function applyAppearance(a) {
  if (!a) return;
  document.documentElement.dataset.theme = a.theme || 'dark';
  document.documentElement.dataset.text = a.text_size || 'medium';
  document.body.classList.toggle('compact', !!a.compact);
}

async function boot() {
  // The language first, so everything rendered from here on is in it.
  try { setTranslation(await api('ui_strings')); } catch (e) { /* English */ }
  translatePage();
  let status;
  try { status = await api('ready'); }
  catch (e) { $('#boot').textContent = t('Could not start: {error}', { error: e.message }); return; }
  st.status = status;
  applyAppearance(status.appearance);
  api('smoke_ready', { state: status.state, version: status.version, storage: storageCheck() }).catch(() => {});
  if (status.state === 'unlocked') enterApp(status);
  else if (status.state === 'locked' && status.remember && status.remember.stored) {
    // Staying signed in: open with the key kept on this device (the
    // keychain, or the key file); the passphrase is the fallback, with the reason.
    try { enterApp(await api('unlock_remembered')); }
    catch (e) { showLock(await api('status')); lockUI.unlock(await api('status'), e.message); }
  } else showLock(status);
  if (status.crashed_last_time) {
    toast(t('OpenArcanum closed unexpectedly last time. Its log stays on this computer, in {folder}; nothing was sent anywhere.', { folder: status.log_dir }),
      { title: t('Crash log'), ms: 15000 });
  }
}

// Uncaught page errors go to the local log: their type and where, not their
// message (which could quote content).
window.addEventListener('error', (e) => {
  const where = `${(e.filename || '').split('/').pop()}:${e.lineno || 0}`;
  api('report_page_error', (e.error && e.error.name) || 'Error', where).catch(() => {});
});
window.addEventListener('unhandledrejection', (e) => {
  api('report_page_error', (e.reason && e.reason.name) || 'UnhandledRejection', 'promise').catch(() => {});
});

// The webview must never persist anything. This only reports
// what the engine offers; the UI code never touches it (see openarcanum.guard).
function storageCheck() { return 'checked-in-python'; }

async function enterApp(status) {
  st.status = status;
  // Every change to the maintainer network is announced, so the last
  // announcement is never older than this status (which Python built before
  // an announcement that arrived while it was on its way).
  if (latestMaintainers) st.status.maintainers = latestMaintainers;
  applyAppearance(status.appearance);
  // Drop the lock screen's DOM so no typed passphrase lingers in it.
  clear($('#lock')).classList.add('hidden');
  $('#boot').classList.add('hidden');
  $('#app').classList.remove('hidden');
  updateAfterUnlock();
  if (status.remember && status.remember.offer) lockUI.offerStaySignedIn(status.remember.offer);
  // Where the app was left is restored below. Until then the rail and the
  // sidebar are still being drawn, so clicks wait (styles.css #app.loading).
  const view0 = st.view;
  const cid0 = st.cid;
  $('#app').classList.add('loading');
  try {
    chat.drafts = (await api('ui_state_get', 'drafts')) || {};
    st.showMembers = !!(await api('ui_state_get', 'show_members'));
    const last = (await api('ui_state_get', 'last')) || {};
    await refreshCommunities();
    // The conversation list (and the direct messages icon's unread count) from this device's store.
    dmUI.refresh();
    if (st.view !== view0 || st.cid !== cid0) return;
    if (last.view === 'dms') { st.view = 'dms'; render(); return; }
    if (last.cid && st.communities.find((c) => c.id === last.cid)) { await selectCommunity(last.cid, last.channel); return; }
    if (st.communities.length) await selectCommunity(st.communities[0].id);
    else render();
  } catch (e) { toastError(e); render(); }
  finally { $('#app').classList.remove('loading'); openPendingInviteLink().catch(toastError); }
}

async function refreshStatus() {
  try { st.status = await api('status'); render(); } catch (e) { /* ignore */ }
}

async function refreshCommunities() {
  st.communities = await api('list_communities');
  renderRail();
}

// Redrawn on every community update: a focused icon keeps the focus.
function renderRail() { return keepingFocus($('#rail'), renderRailNow); }

function renderRailNow() {
  const rail = clear($('#rail'));
  rail.appendChild(h('button', { class: 'rail-item' + (st.view === 'dms' ? ' sel' : ''), title: t('Direct messages'), onclick: () => { leavePage(); st.view = 'dms'; saveLast(); render(); } }, icon('messages-square'),
    dmUI.unreadTotal() ? h('span', { class: 'badge', text: String(dmUI.unreadTotal()) }) : null));
  rail.appendChild(h('div', { class: 'rail-sep' }));
  for (const c of st.communities) {
    // Mention badge (a count) and unread marker (a dot), from the client's
    // read markers; for moderators, the moderator badge (top left, square):
    // reports, AutoMod alerts and join requests waiting, the first step of
    // the trail to them (community menu → Community Admin → the queue).
    const mentions = c.mentions || 0;
    const unread = !mentions && c.unread && !c.muted;
    const waiting = modQueue(c).total;
    const dot = ['removed', 'banned', 'device_revoked', 'device_limit', 'error', 'shut_down'].includes(c.connection) ? 'bad' : c.connection === 'pending' ? 'pending' : c.connection === 'online' ? '' : 'offline';
    rail.appendChild(h('button', { class: 'rail-item' + (st.view === 'community' && c.id === st.cid ? ' sel' : '') + (unread ? ' has-unread' : ''),
      title: t('{community}: {status}', { community: c.name, status: connectionLabel(c) })
        + (mentions ? ' · ' + tn('{n} mention', '{n} mentions', mentions) : unread ? ' · ' + t('unread messages') : '')
        + (waiting ? ' · ' + tn('{n} waiting for review', '{n} waiting for review', waiting) : ''),
      'data-community': c.id, onclick: () => selectCommunity(c.id),
      oncontextmenu: (e) => { e.preventDefault(); communityMenu(e, c, true); } },
      c.icon ? h('img', { src: 'data:image/png;base64,' + c.icon, alt: '' }) : initials(c.name),
      dot ? h('span', { class: 'dot ' + dot }) : null,
      mentions ? h('span', { class: 'badge mention-badge', text: String(Math.min(mentions, 99)) }) : null,
      modBadge(waiting)));
  }
  rail.appendChild(h('button', { class: 'rail-item rail-add' + (st.view === 'add' ? ' sel' : ''), title: t('Add a community'), icon: 'plus', onclick: () => openAddCommunity() }));
  reportAttention();
}

// The tray icon's red dot and tooltip (openarcanum/tray.py) follow the
// window's badges, summed: unread mentions, unread direct messages, and
// what waits for review. Sent when they change.
let lastAttention = '';
function reportAttention() {
  const mentions = st.communities.reduce((n, c) => n + (c.mentions || 0), 0);
  const review = st.communities.reduce((n, c) => n + modQueue(c).total, 0);
  const a = [mentions, dmUI.unreadTotal(), review];
  if (a.join() === lastAttention) return;
  lastAttention = a.join();
  api('set_attention', ...a).catch(() => {});
}

function render() {
  renderRail();
  renderSidebar();
  renderMain();
  renderMembers();
}

function saveLast() {
  api('ui_state_set', 'last', { view: st.view, cid: st.cid, channel: st.channel }).catch(() => {});
}

async function selectCommunity(cid, channel) {
  leavePage();
  st.view = 'community';
  if (st.cid !== cid) st.voiceChat = null;
  st.cid = cid;
  st.replyTo = null;
  await loadState(cid);
  if (st.cid !== cid) return; // another community was chosen meanwhile
  const s = st.states[cid];
  const text = s ? s.channels.filter((c) => c.kind === 'text') : [];
  const want = text.find((c) => c.id === channel) || text[0];
  st.channel = want ? want.id : null;
  render();
  if (want) await selectChannel(want.id);
  loadMembers(cid);
}

async function loadState(cid) {
  try { st.states[cid] = await api('community_state', cid); }
  catch (e) { st.states[cid] = null; }
  if (cid === st.cid) { renderSidebar(); }
}

async function selectChannel(ch) {
  // A channel picked while the Add a community page is open leaves it.
  if (st.view === 'add') st.view = 'community';
  leavePage();
  st.channel = ch;
  st.stage = false;
  st.replyTo = null;
  chat.scrolled = false;
  // The "New" marker goes after what was read before opening the channel.
  const c = chanById(ch);
  chat.newSince[key(st.cid, ch)] = c && c.unread ? c.last_read || 0 : undefined;
  saveLast();
  render();
  const cid = st.cid;
  await loadHistory(cid, ch);
  if (st.cid === cid && st.channel === ch) markRead(cid, ch);
}

async function lockNow() {
  try {
    const status = await api('lock');
    // The 'profile' event may already have shown the lock screen.
    if ($('#lock').classList.contains('hidden')) { closeAllModals(); showLock(status); }
  } catch (e) { toastError(e); }
}

// ---------------------------------------------------------------- events from Python

on('profile', (status) => {
  st.status = status;
  applyAppearance(status.appearance);
  // Only re-render the lock screen when it isn't already showing.
  if (status.state !== 'unlocked' && $('#lock').classList.contains('hidden')) { closeAllModals(); showLock(status); }
  // Locked, the tray counts nothing (app.py): after unlocking, send again.
  if (status.state !== 'unlocked') lastAttention = '';
});

on('appearance', applyAppearance);

// Whether any maintainer node in use serves the public directory
// (maintainers_status().directory): when none does, finding and listing
// communities are hidden (docs/protocol.md §7.2).
function directoryAvailable() {
  const m = st.status && st.status.maintainers;
  return !m || m.directory !== false;
}

// The note shown where public listing would be offered.
function noDirectoryNote() {
  return h('p', { class: 'small muted no-directory-note', text: t("Public listing isn't available on your maintainer nodes. Share invites to let people join.") });
}

// The last announced state of the maintainer network: applied when the app
// is entered (the first check runs as the profile opens, and may finish
// before, or while, the status is on its way).
let latestMaintainers = null;
on('maintainers', (m) => {
  latestMaintainers = m;
  if (!st.status) return;
  const before = directoryAvailable();
  st.status.maintainers = m;
  if (before !== directoryAvailable() && st.view === 'add' && addUI.el) {
    // The ways in change: draw the page again.
    addUI.el = addPage(addUI.tab === 'find' && !directoryAvailable() ? 'join' : addUI.tab);
    render();
  }
});

let communityTimer = {};
on('community', (summary) => {
  const i = st.communities.findIndex((c) => c.id === summary.id);
  if (i >= 0) st.communities[i] = summary; else st.communities.push(summary);
  renderRail();
  adminBadgesChanged(summary);
  if (summary.id === st.cid) {
    clearTimeout(communityTimer[summary.id]);
    communityTimer[summary.id] = setTimeout(async () => {
      const hadChannel = st.channel;
      await loadState(summary.id);
      const s = st.states[summary.id];
      if (s && !s.channels.find((c) => c.id === st.channel)) {
        const first = s.channels.find((c) => c.kind === 'text');
        st.channel = first ? first.id : null;
      }
      if (st.voiceChat && !voiceChat.channel()) st.voiceChat = null;
      render();
      if (st.showMembers) loadMembers(summary.id);
      if (st.channel && st.channel !== hadChannel) loadHistory(st.cid, st.channel);
      else if (st.channel && summary.connection === 'online') loadHistory(st.cid, st.channel);
    }, 150);
  }
});

on('community_removed', ({ id }) => {
  st.communities = st.communities.filter((c) => c.id !== id);
  forgetCommunityMessages(id);
  if (st.cid === id) { st.cid = null; st.channel = null; }
  if (adminUI.shell && adminUI.cid === id) leavePage();
  render();
});

on('message', ({ community, message }) => {
  const pane = community === st.cid && st.view === 'community' ? paneOf(message.channel_id) : null;
  upsertMessage(community, message, { onScreen: !!pane });
  // A message ends its author's typing indicator.
  const typing = chat.typing[key(community, message.channel_id)];
  if (typing) delete typing[message.author];
  if (pane) {
    renderMessages(false, pane);
    renderTyping();
    markReadSoon();
  }
});

// Back to the window: what's on screen is read.
window.addEventListener('focus', () => markReadSoon());

on('toast', (msg) => toast(msg.text, { title: msg.title, error: !!msg.error }));

on('notify', (n) => {
  // New messages (communities and DMs) and reports show in the window as
  // badges only: unread marks, mention counts and the moderator queue's
  // counts. The desktop notification for them is separate (app.py, as set
  // in Settings → Notifications). Other notices (a device linked) show here.
  if (n.community || n.dm) return;
  toast(n.body, { title: n.community_name ? t('{title} · {place}', { title: n.title, place: n.community_name }) : n.title });
});

function closeAllModals() { clear($('#modal-root')); LAYERS.length = 0; closeMenu(); leavePage(); }

// Activity resets the auto-lock timer, at most every 20 s.
let lastActivity = 0;
for (const ev of ['mousemove', 'keydown', 'mousedown', 'wheel']) {
  document.addEventListener(ev, () => {
    const now = Date.now();
    if (now - lastActivity > 20000) { lastActivity = now; api('activity').catch(() => {}); }
  }, { passive: true, capture: true });
}

bridge.ready.then(boot);
