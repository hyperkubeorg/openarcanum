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
    setSidebarWidth(await api('ui_state_get', 'sidebar_width'));
    { const hgt = await api('ui_state_get', 'dm_call_height'); dmUI.callHeight = Number.isFinite(hgt) && hgt >= 160 ? hgt : null; }
    await loadRailLayout();
    const last = (await api('ui_state_get', 'last')) || {};
    await refreshCommunities();
    // The conversation list (and the direct messages icon's unread count) from this device's store.
    dmUI.refresh();
    if (st.view !== view0 || st.cid !== cid0) return;
    // Back where you were in a community; anywhere else (or the first
    // time), Direct messages: its Contacts page.
    if (last.view !== 'dms' && last.cid && st.communities.find((c) => c.id === last.cid)) { await selectCommunity(last.cid, last.channel); return; }
    st.view = 'dms';
    render();
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
  const byId = new Map(st.communities.map((c) => [c.id, c]));
  for (const e of railEntries()) {
    if (typeof e === 'string') rail.appendChild(railCommunity(byId.get(e)));
    else rail.appendChild(railFolder(e, e.items.map((id) => byId.get(id))));
  }
  rail.appendChild(h('button', { class: 'rail-item rail-add' + (st.view === 'add' ? ' sel' : ''), title: t('Add a community'), icon: 'plus', onclick: () => openAddCommunity() }));
  reportAttention();
}

function railCommunity(c, folder = null) {
  // Mention badge (a count) and unread marker (a dot), from the client's
  // read markers; for moderators, the moderator badge (top left, square):
  // reports, AutoMod alerts and join requests waiting, the first step of
  // the trail to them (community menu → Community Admin → the queue).
  const mentions = c.mentions || 0;
  const unread = !mentions && c.unread && !c.muted;
  const waiting = modQueue(c).total;
  const dot = ['removed', 'banned', 'device_revoked', 'device_limit', 'error', 'shut_down'].includes(c.connection) ? 'bad' : c.connection === 'pending' ? 'pending' : c.connection === 'online' ? '' : 'offline';
  return railDraggable(h('button', { class: 'rail-item' + (st.view === 'community' && c.id === st.cid ? ' sel' : '') + (unread ? ' has-unread' : ''),
    title: t('{community}: {status}', { community: c.name, status: connectionLabel(c) })
      + (mentions ? ' · ' + tn('{n} mention', '{n} mentions', mentions) : unread ? ' · ' + t('unread messages') : '')
      + (waiting ? ' · ' + tn('{n} waiting for review', '{n} waiting for review', waiting) : ''),
    'data-community': c.id, onclick: () => selectCommunity(c.id),
    oncontextmenu: (e) => { e.preventDefault(); communityMenu(e, c, true, folder); } },
    c.icon ? h('img', { src: 'data:image/png;base64,' + c.icon, alt: '' }) : initials(c.name),
    dot ? h('span', { class: 'dot ' + dot }) : null,
    mentions ? h('span', { class: 'badge mention-badge', text: String(Math.min(mentions, 99)) }) : null,
    modBadge(waiting)), c.id);
}

// ---------------------------------------------------------------- the rail's order and folders

// How the communities are arranged on the rail, kept on this device
// (ui_state "rail"): a list of community ids and folders, { folder: id,
// name, items: [community ids], open }. Communities not in it (new ones)
// go at the end; folders left empty go away.
const railUI = { layout: [], drag: null };

async function loadRailLayout() {
  try { const v = await api('ui_state_get', 'rail'); railUI.layout = Array.isArray(v) ? v : []; } catch (e) { railUI.layout = []; }
}

function railEntries() {
  const ids = new Set(st.communities.map((c) => c.id));
  const seen = new Set();
  const out = [];
  for (const e of railUI.layout) {
    if (typeof e === 'string') {
      if (ids.has(e) && !seen.has(e)) { seen.add(e); out.push(e); }
    } else if (e && typeof e.folder === 'string' && Array.isArray(e.items)) {
      const items = e.items.filter((id) => ids.has(id) && !seen.has(id));
      items.forEach((id) => seen.add(id));
      if (items.length) out.push({ folder: e.folder, name: String(e.name || ''), items, open: !!e.open });
    }
  }
  for (const c of st.communities) if (!seen.has(c.id)) out.push(c.id);
  return out;
}

function setRailLayout(layout) {
  railUI.layout = layout.filter((e) => typeof e === 'string' || e.items.length);
  api('ui_state_set', 'rail', railUI.layout).catch(() => {});
  renderRail();
}

function folderName(f) {
  if (f.name) return f.name;
  const names = f.items.map((id) => (st.communities.find((c) => c.id === id) || {}).name).filter(Boolean);
  return names.slice(0, 3).join(', ') + (names.length > 3 ? '…' : '');
}

// Where something is: [list it's in, index], the list being the rail or a folder's items.
function railFind(layout, id) {
  for (let i = 0; i < layout.length; i++) {
    const e = layout[i];
    if (e === id || (e.folder && e.folder === id)) return [layout, i];
    if (e.folder) { const j = e.items.indexOf(id); if (j >= 0) return [e.items, j, e]; }
  }
  return [null, -1];
}

// Move a community or folder `id` before, after or into (a folder, or a
// community: a new folder of the two) `target`. Folders don't nest.
function railMove(id, target, where) {
  if (id === target) return;
  const layout = railEntries();
  const [from, i] = railFind(layout, id);
  if (!from) return;
  const [moving] = from.splice(i, 1);
  const isFolder = typeof moving !== 'string';
  let [list, j, parent] = railFind(layout, target);
  if (!list) return;
  if (isFolder && parent) { [list, j] = railFind(layout, parent.folder); where = where === 'into' ? 'after' : where; }
  const there = list[j];
  if (where === 'into' && !isFolder) {
    if (there.folder) there.items.push(moving);
    else if (parent) list.splice(j + 1, 0, moving);
    else list.splice(j, 1, { folder: 'f' + Date.now().toString(36), name: '', items: [there, moving], open: true });
  } else {
    list.splice(where === 'after' ? j + 1 : j, 0, moving);
  }
  setRailLayout(layout);
}

// One step up or down within its list (the rail, or its folder).
function railStep(id, delta) {
  const layout = railEntries();
  const [list, i] = railFind(layout, id);
  if (!list || i + delta < 0 || i + delta >= list.length) return;
  [list[i], list[i + delta]] = [list[i + delta], list[i]];
  setRailLayout(layout);
}

function railToFolder(id, folder) {
  const layout = railEntries();
  const [from, i] = railFind(layout, id);
  if (!from) return;
  from.splice(i, 1);
  if (folder) {
    const f = layout.find((e) => e.folder === folder);
    if (f) f.items.push(id);
  } else {
    layout.push({ folder: 'f' + Date.now().toString(36), name: '', items: [id], open: true });
  }
  setRailLayout(layout);
}

function railOutOfFolder(id) {
  const layout = railEntries();
  const [from, i, parent] = railFind(layout, id);
  if (!parent) return;
  from.splice(i, 1);
  layout.splice(layout.indexOf(parent) + 1, 0, id);
  setRailLayout(layout);
}

function railUngroup(folder) {
  const layout = railEntries();
  const i = layout.findIndex((e) => e.folder === folder);
  if (i < 0) return;
  layout.splice(i, 1, ...layout[i].items);
  setRailLayout(layout);
}

function railEditFolder(folder, edit) {
  const layout = railEntries();
  const f = layout.find((e) => e.folder === folder);
  if (f) { edit(f); setRailLayout(layout); }
}

// Rail menu entries for a community (communityMenu, fromRail): its place.
function railMenuItems(id, folder) {
  const layout = railEntries();
  const [list, i] = railFind(layout, id);
  const folders = layout.filter((e) => e.folder && e.folder !== (folder && folder.folder));
  return [
    { separator: true },
    list && i > 0 ? { label: t('Move up'), run: () => railStep(id, -1) } : null,
    list && i < list.length - 1 ? { label: t('Move down'), run: () => railStep(id, 1) } : null,
    folder ? { label: t('Remove from folder'), run: () => railOutOfFolder(id) } : { label: t('New folder'), run: () => railToFolder(id, '') },
    folders.length ? { group: t('Add to folder'), items: folders.map((f) => ({ label: folderName(f), run: () => railToFolder(id, f.folder) })) } : null,
  ];
}

function folderMenu(e, f) {
  e.preventDefault();
  const layout = railEntries();
  const i = layout.findIndex((x) => x.folder === f.folder);
  openMenu(e.clientX, e.clientY, tidyMenu([
    { label: f.open ? t('Close folder') : t('Open folder'), run: () => railEditFolder(f.folder, (x) => { x.open = !x.open; }) },
    { label: t('Rename folder…'), run: async () => {
      const name = await promptModal(t('Rename folder'), t('Name'), f.name, { placeholder: folderName(f), hint: t('Leave it empty to show the names of its communities.') });
      if (name !== null) railEditFolder(f.folder, (x) => { x.name = name.trim().slice(0, 64); });
    } },
    i > 0 ? { label: t('Move up'), run: () => railStep(f.folder, -1) } : null,
    i < layout.length - 1 ? { label: t('Move down'), run: () => railStep(f.folder, 1) } : null,
    { separator: true },
    { label: t('Ungroup'), run: () => railUngroup(f.folder) },
  ]), { label: folderName(f) });
}

function railFolder(f, cs) {
  const name = folderName(f);
  const mentions = cs.reduce((n, c) => n + (c.mentions || 0), 0);
  const unread = !mentions && cs.some((c) => c.unread && !c.muted);
  const waiting = cs.reduce((n, c) => n + modQueue(c).total, 0);
  const sel = st.view === 'community' && cs.some((c) => c.id === st.cid);
  const btn = railDraggable(h('button', { class: 'rail-item rail-folder' + (unread ? ' has-unread' : '') + (sel && !f.open ? ' sel' : ''),
    title: name + (mentions ? ' · ' + tn('{n} mention', '{n} mentions', mentions) : unread ? ' · ' + t('unread messages') : ''),
    'aria-label': t('Folder {name}', { name }), 'aria-expanded': String(f.open), 'data-folder': f.folder,
    onclick: () => railEditFolder(f.folder, (x) => { x.open = !x.open; }), oncontextmenu: (e) => folderMenu(e, f) },
  f.open ? icon('chevron-down')
    : h('span', { class: 'folder-grid', 'aria-hidden': 'true' }, ...cs.slice(0, 4).map((c) => (c.icon ? h('img', { src: 'data:image/png;base64,' + c.icon, alt: '' }) : h('span', { text: (c.name || '?').slice(0, 1).toUpperCase() })))),
  !f.open && mentions ? h('span', { class: 'badge mention-badge', text: String(Math.min(mentions, 99)) }) : null,
  !f.open ? modBadge(waiting) : null), f.folder);
  return h('div', { class: 'rail-folder-box' + (f.open ? ' open' : '') }, btn, ...(f.open ? cs.map((c) => railCommunity(c, f)) : []));
}

// Dragging on the rail: the top or bottom quarter of an icon drops before
// or after it, the middle into it (a folder, or a new folder of the two).
function railDraggable(el, id) {
  const marks = ['drop-before', 'drop-after', 'drop-into'];
  const unmark = () => el.classList.remove(...marks);
  el.draggable = true;
  el.addEventListener('dragstart', (e) => { railUI.drag = id; e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', id); });
  el.addEventListener('dragend', () => { railUI.drag = null; $$('#rail .drop-before, #rail .drop-after, #rail .drop-into').forEach((x) => x.classList.remove(...marks)); });
  const where = (e) => {
    const r = el.getBoundingClientRect();
    const y = (e.clientY - r.top) / r.height;
    return y < 0.25 ? 'before' : y > 0.75 ? 'after' : 'into';
  };
  el.addEventListener('dragover', (e) => {
    if (!railUI.drag || railUI.drag === id) return;
    e.preventDefault();
    unmark();
    el.classList.add('drop-' + where(e));
  });
  el.addEventListener('dragleave', unmark);
  el.addEventListener('drop', (e) => {
    e.preventDefault();
    unmark();
    const moving = railUI.drag;
    railUI.drag = null;
    if (moving) railMove(moving, id, where(e));
  });
  return el;
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
  // Saved now too: a community whose channels haven't arrived yet (just
  // claimed) has no channel to select below, and is still where you are.
  saveLast();
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
  const renamed = st.status && status.state === 'unlocked' && st.status.display_name !== status.display_name;
  st.status = status;
  if (renamed && !$('#app').classList.contains('hidden')) render();
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
  return !!m && m.directory === true;
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

// ---------------------------------------------------------------- list column width

// The list column (channels, conversations) can be made wider or narrower
// by dragging its right edge, or with the arrow keys on the handle. The
// width is kept per profile (ui_state 'sidebar_width'); it can't be hidden.
const SIDEBAR_MIN = 200;
const SIDEBAR_MAX = 480;
const SIDEBAR_DEFAULT = 240;
let sidebarWidth = SIDEBAR_DEFAULT;

function setSidebarWidth(w) {
  const n = Number(w);
  sidebarWidth = Number.isFinite(n) && n > 0 ? Math.round(Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, n))) : SIDEBAR_DEFAULT;
  document.documentElement.style.setProperty('--sidebar-width', `${sidebarWidth}px`);
  const handle = $('.sidebar-resizer');
  if (handle) {
    handle.setAttribute('aria-valuenow', String(sidebarWidth));
    // Again here: the handle is made when the script loads, before the
    // language's strings are in.
    handle.setAttribute('aria-label', t('Width of the list'));
  }
}

function saveSidebarWidth() {
  api('ui_state_set', 'sidebar_width', sidebarWidth === SIDEBAR_DEFAULT ? null : sidebarWidth).catch(() => {});
}

(function sidebarResizer() {
  const handle = h('div', {
    class: 'sidebar-resizer', role: 'separator', tabindex: '0', 'aria-orientation': 'vertical',
    'aria-label': t('Width of the list'), 'aria-valuemin': String(SIDEBAR_MIN), 'aria-valuemax': String(SIDEBAR_MAX),
    'aria-valuenow': String(sidebarWidth),
  });
  let startX = 0;
  let startW = 0;
  handle.addEventListener('pointerdown', (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    startX = e.clientX;
    startW = sidebarWidth;
    handle.setPointerCapture(e.pointerId);
    handle.classList.add('dragging');
    document.body.classList.add('resizing-sidebar');
  });
  handle.addEventListener('pointermove', (e) => {
    if (handle.hasPointerCapture(e.pointerId)) setSidebarWidth(startW + e.clientX - startX);
  });
  const end = (e) => {
    if (!handle.hasPointerCapture(e.pointerId)) return;
    handle.releasePointerCapture(e.pointerId);
    handle.classList.remove('dragging');
    document.body.classList.remove('resizing-sidebar');
    saveSidebarWidth();
  };
  handle.addEventListener('pointerup', end);
  handle.addEventListener('pointercancel', end);
  handle.addEventListener('dblclick', () => { setSidebarWidth(SIDEBAR_DEFAULT); saveSidebarWidth(); });
  handle.addEventListener('keydown', (e) => {
    const step = e.shiftKey ? 48 : 16;
    if (e.key === 'ArrowLeft') setSidebarWidth(sidebarWidth - step);
    else if (e.key === 'ArrowRight') setSidebarWidth(sidebarWidth + step);
    else if (e.key === 'Home') setSidebarWidth(SIDEBAR_MIN);
    else if (e.key === 'End') setSidebarWidth(SIDEBAR_MAX);
    else return;
    e.preventDefault();
    saveSidebarWidth();
  });
  $('#app').appendChild(handle);
})();

