// The community view: channels, messages, composer, members.
'use strict';

const chat = {
  drafts: {}, draftTimer: null, previews: {}, uploading: null, collapsed: {},
  // channel key -> {member key: {name, until}}
  typing: {}, typingSent: 0,
  // channel key -> the last read message id when the channel was opened
  // (where the "New" marker goes).
  newSince: {},
  readTimer: null,
};

const TYPING_SHOWN_MS = 6000;

// Where messages show: the main view (a text channel) and the side panel (a
// voice channel's chat, voicechat.js). Each has its own list, typing line,
// composer and upload line; both can be on screen at once.
const PANES = {
  main: { list: 'messages', typing: 'typing', input: 'composer-input', reply: 'composer-reply', progress: 'upload-progress', scrolled: 'scrolled', channel: () => st.channel },
  side: { list: 'vc-messages', typing: 'vc-typing', input: 'vc-composer-input', reply: 'vc-composer-reply', progress: 'vc-upload-progress', scrolled: 'sideScrolled', channel: () => st.voiceChat },
};

// The pane showing a channel's messages right now, if any.
function paneOf(channelId) {
  for (const [name, p] of Object.entries(PANES)) if (p.channel() === channelId && document.getElementById(p.list)) return name;
  return null;
}

function key(cid, ch) { return cid + ':' + ch; }
function cstate() { return st.states[st.cid]; }
function chanById(id) { const s = cstate(); return s && s.channels.find((c) => c.id === id); }
function features(c) { return (c && c.features) || {}; }

// The formatter's view of this community: who and what mentions refer to.
function mdCtx(cid = st.cid) {
  const s = st.states[cid];
  const members = st.members[cid] || [];
  const me = s && s.self ? s.self.key : '';
  return {
    me,
    memberName: (k) => {
      const m = members.find((x) => x.key === k);
      if (m) return m.nickname || m.display_name;
      if (k === me) return s.self.nickname || s.self.display_name;
      refreshMembersSoon(cid);
      return '';
    },
    roleName: (id) => { const r = s && s.roles.find((x) => String(x.id) === String(id)); return r ? r.name : ''; },
    roleColor: (id) => { const r = s && s.roles.find((x) => String(x.id) === String(id)); return r ? r.color : ''; },
    onLink: linkDialog,
    // A right-click on a mentioned member: their menu (people.js).
    memberMenu: (e, k) => userMenu(e, { cid, key: k }),
    // Plain text runs: this community's custom emoji (":name:id") become images.
    text: (parent, v) => appendWithEmoji(parent, v, cid),
  };
}

// A mention of someone not in the loaded member list (they just joined):
// reload the list, at most every 5 s, and redraw.
const membersRefreshed = {};
function refreshMembersSoon(cid) {
  if (Date.now() - (membersRefreshed[cid] || 0) < 5000) return;
  membersRefreshed[cid] = Date.now();
  setTimeout(() => loadMembers(cid), 0);
}

// Links are never opened or loaded in the app: show the address and offer to copy it.
function linkDialog(url) {
  modal({ title: t('Link'), body: h('div', { class: 'stack' },
    h('p', { class: 'small muted', text: t('OpenArcanum never opens links itself, so linked sites don\'t learn your address from the app. Copy it to open it in your browser if you trust it.') }),
    codeBlock(url, { copyLabel: t('Copy link') })),
  actions: [{ label: t('Close') }] });
}

// Mention tokens back into @names (for editing and copying).
function decodeMentions(text, cid = st.cid) {
  const ctx = mdCtx(cid);
  return String(text || '')
    .replace(/<@&(\d+)>/g, (tok, id) => { const n = ctx.roleName(id); return n ? '@' + n : tok; })
    .replace(/<@([A-Za-z0-9_-]+)>/g, (tok, k) => { const n = ctx.memberName(k); return n ? '@' + n : tok; });
}

// ---------------------------------------------------------------- sidebar

function renderSidebar() { return keepingFocus($('#sidebar'), renderSidebarNow); }

function renderSidebarNow() {
  const side = clear($('#sidebar'));
  // The Add a community page keeps the sidebar it was opened from: opened
  // from the DMs, the DM list stays (not the last community's channels).
  if (st.view === 'dms' || (st.view === 'add' && addUI.prev && addUI.prev.view === 'dms')) return renderDmSidebar(side);
  const summary = st.communities.find((c) => c.id === st.cid);
  if (!summary) {
    side.appendChild(h('div', { class: 'side-head' }, h('span', { class: 'name', text: 'OpenArcanum' })));
    side.appendChild(h('div', { class: 'side-body muted small', text: t('Add a community with the + button.') }));
    side.appendChild(userPanel(null));
    return;
  }
  const s = cstate();
  const waiting = modQueue(summary).total;
  side.appendChild(h('div', { class: 'side-head', 'aria-haspopup': 'menu',
    title: waiting ? tn('Community menu ({n} waiting for review)', 'Community menu ({n} waiting for review)', waiting) : t('Community menu'),
    onclick: (e) => communityMenu(e, summary) },
  h('span', { class: 'name', text: summary.name }),
  modBadge(waiting),
  icon('chevron-down')));
  const body = h('div', { class: 'side-body' });
  if (s && s.channels.length) {
    renderChannelTree(body, s, summary);
  } else if (summary.connection === 'pending') {
    body.appendChild(h('div', { class: 'muted small', text: t('Waiting for an admin to approve you.') }));
  }
  side.appendChild(body);
  if (voiceUI.barVisible()) side.appendChild(voiceUI.bar());
  side.appendChild(userPanel(summary));
}

// The channel list: uncategorized channels, then each category (collapsible)
// with its channels in order. Voice channels are hidden when the community
// turned voice off (the server refuses them too).
function renderChannelTree(body, s, summary) {
  const voiceOn = summary.features ? summary.features.voice !== false : true;
  const visible = s.channels.filter((c) => c.kind !== 'voice' || voiceOn);
  const manage = summary.can.manage_channels;
  const addButtons = (categoryId) => manage ? [
    h('button', { class: 'icon-btn', title: t('Create text channel'), 'aria-label': t('Create text channel'), icon: 'plus', onclick: (e) => { e.stopPropagation(); createChannelDialog('text', categoryId); } }),
    voiceOn ? h('button', { class: 'icon-btn', title: t('Create voice channel'), 'aria-label': t('Create voice channel'), icon: 'volume-2', onclick: (e) => { e.stopPropagation(); createChannelDialog('voice', categoryId); } }) : null,
  ] : [];
  const top = visible.filter((c) => !c.category_id);
  if (top.length || manage) body.appendChild(h('div', { class: 'side-group' }, h('span', { class: 'spacer', text: t('Channels') }), ...addButtons(0)));
  for (const c of top) channelRow(body, c);
  for (const cat of (s.categories || []).slice().sort((a, b) => a.position - b.position)) {
    const chans = visible.filter((c) => c.category_id === cat.id);
    if (!chans.length && !manage) continue;
    const ck = key(st.cid, 'cat' + cat.id);
    const collapsed = !!chat.collapsed[ck];
    body.appendChild(h('div', { class: 'side-group side-cat', role: 'button', 'aria-expanded': String(!collapsed), 'data-category': String(cat.id),
      onclick: () => { chat.collapsed[ck] = !collapsed; renderSidebar(); } },
    h('span', { class: 'caret' }, collapsed ? icon('chevron-right') : icon('chevron-down')), h('span', { class: 'spacer', text: cat.name }), ...addButtons(cat.id)));
    if (collapsed) {
      // A collapsed category still shows its selected channel, like Discord.
      for (const c of chans) if (c.id === st.channel) channelRow(body, c);
      continue;
    }
    for (const c of chans) channelRow(body, c);
  }
}

// A muted channel's mark in the channel list.
function mutedFlag() {
  return h('span', { class: 'small muted muted-flag', role: 'img', title: t('Muted'), 'aria-label': t('Muted') }, icon('bell-off'));
}

function channelRow(body, c) {
  if (c.kind === 'voice') {
    const inCall = voiceUI.current && voiceUI.current.cid === st.cid && voiceUI.current.channel === c.id;
    // Its text chat: unread marks while the chat isn't open, and a Chat
    // button (always shown when there's something new).
    const chatOpen = st.voiceChat === c.id && voiceChat.shown();
    const unread = !chatOpen && c.unread > 0;
    const mentions = chatOpen ? 0 : c.mentions || 0;
    // The row holds two controls side by side (join, and Chat), so it isn't
    // a button itself (nested controls confuse screen readers): the join
    // control is the channel's name, and a click anywhere else on the row
    // joins too.
    const join = h('span', { class: 'chan-join', onclick: (e) => { e.stopPropagation(); voiceUI.join(st.cid, c.id); } },
      h('span', { class: 'hash' }, icon('volume-2')), h('span', { class: 'cname', text: c.name }));
    const row = h('div', { class: 'chan voice' + (inCall ? ' sel' : '') + (unread ? ' unread' : ''), 'data-channel': String(c.id),
      oncontextmenu: (e) => { e.preventDefault(); channelMenu(e, c); } },
    join, c.muted ? mutedFlag() : null,
    mentions ? h('button', { class: 'pill mention', title: t('Jump to the first unread mention'), 'aria-label': tn('{n} unread mention in {channel}', '{n} unread mentions in {channel}', mentions, { channel: c.name }),
      text: '@' + mentions, onclick: (e) => { e.stopPropagation(); jumpToFirstMention(c.id); } }) : null,
    unread && !mentions ? h('span', { class: 'unread-dot', 'aria-label': t('unread') }) : null,
    h('button', { class: 'icon-btn chat-btn' + (chatOpen ? ' on' : '') + (unread || mentions || chatOpen ? '' : ' hover-only'),
      title: chatOpen ? t('Close chat') : t('Open chat'), 'aria-label': t('Chat in {channel}', { channel: c.name }), 'aria-pressed': String(chatOpen), icon: 'message-square',
      onclick: (e) => { e.stopPropagation(); voiceChat.toggle(c.id); } }));
    row.addEventListener('click', () => voiceUI.join(st.cid, c.id));
    body.appendChild(row);
    const rows = voiceRoomRows(st.cid, c.id);
    if (rows) body.appendChild(rows);
    return;
  }
  // Unread marker (a dot) and unread mentions (a count that jumps to the
  // first one), from the client's read markers.
  const unread = c.id !== st.channel && c.unread > 0;
  const mentions = c.id !== st.channel ? c.mentions || 0 : 0;
  body.appendChild(h('div', {
    class: 'chan' + (c.id === st.channel ? ' sel' : '') + (unread ? ' unread' : ''),
    'data-channel': String(c.id),
    onclick: () => selectChannel(c.id).then(focusComposer),
    oncontextmenu: (e) => { e.preventDefault(); channelMenu(e, c); },
  }, h('span', { class: 'hash' }, icon('hash')), h('span', { class: 'cname', text: c.name }), c.muted ? mutedFlag() : null,
  mentions ? h('button', { class: 'pill mention', title: t('Jump to the first unread mention'), 'aria-label': tn('{n} unread mention in #{channel}', '{n} unread mentions in #{channel}', mentions, { channel: c.name }),
    text: '@' + mentions, onclick: (e) => { e.stopPropagation(); jumpToFirstMention(c.id); } }) : null,
  unread && !mentions ? h('span', { class: 'unread-dot', 'aria-label': t('unread') }) : null));
}

function featureOn(summary, name) { return !summary || !summary.features || summary.features[name] !== false; }

function userPanel(summary) {
  const s = summary ? st.states[summary.id] : null;
  const shown = s && s.self ? s.self.nickname || summary.display_name : summary ? summary.display_name : '';
  // Avatar and name open the status picker (status.js); under the name, the
  // custom text, else the connection.
  const me = myStatus();
  const connection = summary ? connectionLabel(summary) : '';
  return h('div', { class: 'user-panel', 'data-connection': connection },
    h('button', { class: 'me-btn', type: 'button', 'aria-haspopup': 'menu', title: t('You: status, contact code, name'),
      'aria-label': t('Your status: {status}. Your options', { status: statusSentence(me) }), onclick: (e) => meMenu(e) },
    withStatus(avatarEl(shown || (st.status && st.status.display_name) || t('You'), summary ? summary.identity : 'me', 'sm'), me),
    h('div', { class: 'who' }, h('b', { text: summary ? shown || t('You') : (st.status && st.status.display_name) || t('You') }),
      h('span', { class: 'small muted me-status', text: statusLine(me) || connection || statusLabel(me.state) }))),
    h('button', { class: 'icon-btn', title: t('Your contact code'), 'aria-label': t('Your contact code'), icon: 'id-card', onclick: () => myContactCodeDialog() }),
    h('button', { class: 'icon-btn', title: t('Lock now'), icon: 'lock', onclick: lockNow }),
    h('button', { class: 'icon-btn', title: t('Settings'), icon: 'settings', onclick: () => openSettings() }));
}

// "Jump to latest" under a message list, shown once it's scrolled more than
// about a screen up from the newest message.
function jumpToLatest(list) {
  const btn = h('button', { class: 'btn small jump-latest hidden', onclick: () => list.scrollTo({ top: list.scrollHeight, behavior: 'smooth' }) },
    icon('arrow-down'), h('span', { text: t('Jump to latest') }));
  const check = () => btn.classList.toggle('hidden', list.scrollHeight - list.scrollTop - list.clientHeight < 200);
  list.addEventListener('scroll', check, { passive: true });
  new MutationObserver(check).observe(list, { childList: true });
  return btn;
}

// Your own menu, from your name and avatar in the user panel.
function meMenu(e) {
  const r = e.currentTarget.getBoundingClientRect();
  openMenu(r.left, r.top, [
    { label: t('Set status…'), run: () => statusPicker() },
    { label: t('Your contact code…'), run: () => myContactCodeDialog() },
    { label: t('Your name…'), run: () => openSettings('identity') },
    { separator: true },
    { label: t('Settings'), run: () => openSettings() },
  ], { label: t('You') });
}

function connectionLabel(s) {
  // What the maintainer nodes say about its server, when this device can't
  // reach it (hosting.js): offline since when, degraded, shut down.
  const why = serverStatusLine(s);
  if (why) return why;
  return { online: t('Connected'), connecting: t('Connecting…'), offline: t('Offline'), pending: t('Pending approval'), error: t('Error'),
    removed: t('Removed'), banned: t('Banned'), device_revoked: t('Device removed'), device_limit: t('Device limit'), shut_down: t('Shut down') }[s.connection] || s.connection;
}

// Members with any admin or moderator permission get Community Admin: the
// red entry in the community menu and the rail's right-click menu. Inviting alone doesn't count
// (Invite people is its own entry).
function isCommunityAdmin(summary) {
  const can = summary.can || {};
  const s = st.states[summary.id];
  return !!(can.manage_community || can.manage_channels || can.manage_roles || can.manage_messages || can.kick || can.ban
    || can.timeout_members || can.manage_nicknames || can.manage_emoji || can.view_audit_log || can.approve_members
    || summary.is_owner || (s && s.self && s.self.owner_invited));
}

// What waits in the moderator queues this member can see (the server's
// counts, docs/protocol.md §4.9).
function modQueue(summary) {
  const q = (summary && summary.mod_queue) || {};
  const reports = q.reports || 0;
  const pending = q.pending || 0;
  return { reports, pending, total: reports + pending };
}

const NOTIFY_LEVELS = [['all', N_('All messages')], ['mentions', N_('Mentions only')], ['nothing', N_('Nothing')]];

// The community menu: from the name at the top of the channel list, and
// (fromRail) the right-click menu or Menu key on the community's rail icon.
function communityMenu(e, summary, fromRail = false, folder = null) {
  // The newest summary: permissions may have changed since the sidebar was drawn.
  summary = st.communities.find((c) => c.id === summary.id) || summary;
  let x = e.clientX;
  let y = e.clientY;
  if (!fromRail) { const r = e.currentTarget.getBoundingClientRect(); x = r.left + 8; y = r.bottom + 4; }
  // On the rail, also its place there (app.js): moving it, folders.
  const items = communityMenuItems(summary);
  if (fromRail) items.splice(items.length - 2, 0, ...railMenuItems(summary.id, folder));
  openMenu(x, y, tidyMenu(items));
}

function communityMenuItems(summary) {
  const id = summary.id;
  const copyId = { label: t('Copy community ID'), run: () => api('copy_text', id).then(() => toast(t('Community ID copied.')), toastError) };
  if (summary.status !== 'active' && summary.status !== 'pending') {
    // Removed, banned or revoked: only what still makes sense.
    return [copyId, { label: t('Remove from list'), danger: true, run: async () => {
      if (!(await confirmForget(summary ? summary.name : t('This community')))) return;
      await api('dismiss', id);
      if (st.cid === id) st.cid = null;
      await refreshCommunities();
      render();
    } }];
  }
  const can = summary.can || {};
  const q = modQueue(summary);
  const here = id === st.cid && st.view === 'community';
  const level = summary.notify_level || 'all';
  const setLevel = (v) => api('set_community_notify', id, v).then(refreshCommunities, toastError);
  const suppress = summary.suppress || [];
  const suppressItem = (kind, label) => ({ label, toggle: true, checked: suppress.includes(kind),
    run: () => api('set_community_suppress', id, kind, !suppress.includes(kind)).then(refreshCommunities, toastError) });
  return [
    { group: t('Notifications'), items: NOTIFY_LEVELS.map(([v, label]) => ({ label: t(label), checked: level === v, run: () => setLevel(v) })) },
    suppressItem('everyone', t('Suppress @everyone, @here and @channel')),
    suppressItem('roles', t('Suppress role mentions')),
    { label: summary.muted ? t('Unmute community') : t('Mute community'), run: () => api('toggle_mute', id, null).then(refreshCommunities, toastError) },
    { label: t('Mark all as read'), run: () => api('mark_community_read', id).then(() => loadState(id).then(() => { if (st.cid === id) render(); }), toastError) },
    ...(summary.hosted ? [{ separator: true }, ...hostingMenuItems(summary)] : []),
    { separator: true },
    can.create_invite ? { label: t('Invite people'), run: () => openCommunitySettings(id, 'invites') } : null,
    isCommunityAdmin(summary) ? { label: t('Community Admin'), admin: true, badge: q.total, run: () => openCommunitySettings(id) } : null,
    can.manage_messages ? { label: t('Reports'), badge: q.reports, run: () => openCommunitySettings(id, 'reports') } : null,
    can.approve_members ? { label: t('Approval queue'), badge: q.pending, run: () => openCommunitySettings(id, 'pending') } : null,
    can.view_audit_log || auditPublic(id) ? { label: t('Audit log'), run: () => openCommunitySettings(id, 'audit') } : null,
    here && featureOn(summary, 'search') ? { label: t('Search messages'), run: () => searchDialog() } : null,
    { label: t('Your profile here'), run: () => communityProfileDialog(summary) },
    copyId,
    summary.connection === 'offline' || summary.connection === 'error' || summary.connection === 'shut_down' ? { label: t('Reconnect'), run: () => api('retry', id) } : null,
    { separator: true },
    { label: t('Leave community'), danger: true, run: () => leaveDialog(summary) },
  ];
}

function auditPublic(cid = st.cid) { const s = st.states[cid]; return !!(s && s.settings && s.settings.audit_visibility === 'everyone'); }

function channelMenu(e, c) {
  const summary = st.communities.find((x) => x.id === st.cid);
  openMenu(e.clientX, e.clientY, [
    c.kind === 'voice' ? { label: t('Open chat'), run: () => voiceChat.open(c.id) } : null,
    { label: c.muted ? t('Unmute channel') : t('Mute channel'), run: () => api('toggle_mute', st.cid, c.id).then(() => loadState(st.cid)) },
    c.unread ? { label: t('Mark as read'), run: () => markRead(st.cid, c.id) } : null,
    summary && summary.can.manage_channels ? { label: t('Edit channel'), run: () => openCommunitySettings(st.cid, 'channels', c.id) } : null,
    c.kind === 'text' && c.can.manage_messages ? { label: t('Select messages to delete'), run: () => { selectChannel(c.id).then(() => startSelecting(null)); } } : null,
    c.can.manage_messages ? { label: t('Delete recent messages…'), danger: true, run: () => deleteRecentDialog(c) } : null,
    summary && summary.can.manage_channels ? { separator: true } : null,
    summary && summary.can.manage_channels ? { label: c.kind === 'voice' ? t('Delete voice channel…') : t('Delete channel…'), danger: true, run: () => deleteChannelDialog(c) } : null,
  ].filter(Boolean).filter((it, i, all) => !it.separator || (i > 0 && i < all.length - 1)));
}

// A channel, with its messages and files, gone from the server (Manage
// channels): from its right-click menu, as in Community Admin → Channels.
async function deleteChannelDialog(c) {
  const cid = st.cid;
  const voice = c.kind === 'voice';
  if (!(await confirmModal(voice ? t('Delete voice channel?') : t('Delete channel?'),
    voice ? t('{name}, its chat and its files are deleted from the server. Anyone in the call is disconnected.', { name: c.name })
      : t('#{name} and all its messages and files are deleted from the server.', { name: c.name }), t('Delete')))) return;
  try {
    await api('admin', cid, 'delete_channel', { id: c.id });
    await loadState(cid);
    if (st.cid === cid && st.channel === c.id) {
      const next = (st.states[cid] ? st.states[cid].channels : []).find((x) => x.kind === 'text');
      if (next) await selectChannel(next.id);
      else { st.channel = null; render(); }
    }
    toast(voice ? t('Voice channel deleted.') : t('Channel deleted.'));
  } catch (e) { toastError(e); }
}

function createChannelDialog(kind, categoryId = 0) {
  promptModal(kind === 'voice' ? t('Create voice channel') : t('Create text channel'), t('Channel name'), '', { ok: t('Create') }).then(async (name) => {
    if (!name) return;
    try { const r = await api('admin', st.cid, 'create_channel', { name, kind, category_id: categoryId }); await loadState(st.cid); if (kind === 'text') await selectChannel(r.id).then(focusComposer); }
    catch (e) { toastError(e); }
  });
}

// Removing a community that's gone (shut down, removed, banned) from the
// list forgets its key: asks first (the menu and Settings → Communities).
function confirmForget(name) {
  return confirmModal(t('Remove {name} from your list?', { name }), t('{name} and its key are removed from this device. To come back you\'d need a new invite. This can\'t be undone.', { name }), t('Remove'));
}

async function leaveDialog(summary) {
  const del = checkbox(t('Also delete my key for this community'), true, t("Without the key you can't come back as the same member."));
  modal({ title: t('Leave {community}?', { community: summary.name }), body: h('div', { class: 'stack' },
    h('p', { text: t('You stop receiving messages and this device deletes its copy of the community.') }), del),
  actions: [{ label: t('Cancel') }, { label: t('Leave'), kind: 'danger', run: async () => {
    await api('leave', summary.id, del.input.checked);
    st.cid = null;
    await refreshCommunities();
    render();
  } }] });
}

function communityProfileDialog(summary) {
  const s = st.states[summary.id];
  const me = (s && s.self) || {};
  const can = summary.can;
  const canRename = can.change_nickname;
  const name = h('input', { type: 'text', value: summary.display_name, 'aria-label': t('Display name'), disabled: !canRename });
  const nick = h('input', { type: 'text', value: me.nickname || '', maxlength: 32, 'aria-label': t('Nickname'), disabled: !canRename });
  const pronouns = h('input', { type: 'text', value: me.pronouns || '', maxlength: 40, 'aria-label': t('Pronouns') });
  const about = h('textarea', { rows: 3, value: me.about || '', maxlength: 190, 'aria-label': t('About me') });
  const cardsOn = featureOn(summary, 'contact_cards');
  const card = checkbox(t('Publish a contact card here'), summary.publish_card, t('Lets members of this community send you a direct message, which arrives as a request. On unless you turn it off.'));
  modal({ title: t('Your profile in {community}', { community: summary.name }), body: h('div', { class: 'stack' },
    field(t('Display name'), name, canRename ? t('Each community has its own.') : t("This community doesn't let you change your name.")),
    field(t('Nickname'), nick, canRename ? t('Shown instead of your display name here. Moderators can change it too. Leave empty for none.') : t('This community doesn\'t let you change your nickname.')),
    field(t('Pronouns'), pronouns, t('Up to 40 characters.')),
    field(t('About me'), about, t('Up to 190 characters. Members see it on your profile card.')),
    h('div', { class: 'row' }, h('button', { class: 'btn', text: t('Change avatar…'), onclick: async () => {
      const p = await pickFile('image'); if (!p) return;
      try { await api('set_avatar', summary.id, p); toast(t('Avatar updated.')); } catch (e) { toastError(e); }
    } }), h('button', { class: 'btn link', text: t('Remove avatar'), onclick: () => api('set_avatar', summary.id, null).then(() => toast(t('Avatar removed.')), toastError) })),
    cardsOn ? card : null),
  actions: [{ label: t('Cancel') }, { label: t('Save'), kind: 'primary', run: async () => {
    if (canRename && name.value !== summary.display_name) await api('set_display_name', summary.id, name.value);
    if (canRename && nick.value.trim() !== (me.nickname || '')) await api('set_nickname', summary.id, me.key, nick.value.trim());
    if (pronouns.value.trim() !== (me.pronouns || '') || about.value.trim() !== (me.about || '')) await api('update_profile', summary.id, about.value, pronouns.value);
    if (cardsOn && card.input.checked !== summary.publish_card) await api('set_contact_card', summary.id, card.input.checked);
    await refreshCommunities();
    await loadState(summary.id);
    loadMembers(summary.id);
  } }] });
}

// ---------------------------------------------------------------- main area

// The main area is rebuilt on every state change; a field being typed in
// (the composer) gets its focus and cursor back afterwards if it's still
// the same channel or DM, so updates never take the keyboard away.
function renderMain() {
  // A page (Settings, Community Admin) stays until something leaves it,
  // except the call's stage, which is asked for (voiceUI.showStage) by
  // joining a voice channel or from the voice bar.
  if (pageOpen()) {
    if (!st.stage) { if (!$('#main').contains(PAGE.cur.el)) clear($('#main')).appendChild(PAGE.cur.el); return; }
    leavePage();
  }
  const was = document.activeElement;
  const view = () => `${st.view}|${st.cid}|${st.channel}|${typeof dmUI === 'undefined' ? '' : dmUI.current}`;
  const keep = was && was.id && $('#main').contains(was) && (was.tagName === 'TEXTAREA' || was.tagName === 'INPUT')
    ? { id: was.id, view: view(), start: was.selectionStart, end: was.selectionEnd } : null;
  keepingFocus($('#main'), renderMainView);
  if (!keep || keep.view !== view()) return;
  const now = document.getElementById(keep.id);
  if (!now || now === was || !$('#main').contains(now)) return;
  now.focus();
  if (keep.start != null) {
    try { now.setSelectionRange(keep.start, keep.end); } catch (e) { /* inputs without a selection (e.g. checkboxes) */ }
  }
}

function renderMainView() {
  const main = clear($('#main'));
  if (st.view === 'dms') return renderDmMain(main);
  if (st.view === 'add') return renderAddPage(main);
  const summary = st.communities.find((c) => c.id === st.cid);
  if (!summary) {
    main.appendChild(h('div', { class: 'empty' }, h('h2', { text: t('No community selected') }),
      h('p', { text: t('Join one with an invite, find a public one, or host your own.') }),
      h('button', { class: 'btn primary', text: t('Add a community'), onclick: () => openAddCommunity() })));
    return;
  }
  const s = cstate();
  if (st.stage && voiceUI.state.active && voiceUI.state.community === st.cid) return voiceUI.renderStage();
  const c = chanById(st.channel);
  main.appendChild(h('div', { class: 'main-head' },
    h('span', { class: 'muted' }, icon('hash')), h('span', { class: 'title', text: c ? c.name : summary.name }),
    c && c.topic ? h('span', { class: 'channel-topic muted small', title: c.topic, text: c.topic }) : null,
    h('div', { class: 'spacer' }),
    c ? h('button', { class: 'icon-btn' + (c.muted ? ' on' : ''), title: c.muted ? t('Unmute channel') : t('Mute channel'), icon: c.muted ? 'bell-off' : 'bell',
      onclick: () => api('toggle_mute', st.cid, c.id).then(() => loadState(st.cid)) }) : null,
    c ? h('button', { class: 'icon-btn', title: t('Pinned messages'), 'aria-label': t('Pinned messages'), icon: 'pin', onclick: () => pinsDialog(c) }) : null,
    featureOn(summary, 'search') ? h('button', { class: 'icon-btn', title: t('Search'), icon: 'search', onclick: searchDialog }) : null,
    h('button', { class: 'icon-btn' + (st.showMembers ? ' on' : ''), title: t('Members'), icon: 'users', onclick: toggleMembers })));
  for (const b of banners(summary, s)) main.appendChild(b);
  if (!c) {
    const why = ['removed', 'banned', 'device_revoked'].includes(summary.status) ? summary.error
      : summary.connection === 'pending' ? t('Your request to join is waiting for an admin.') : summary.connection === 'online' ? t('Pick a channel.')
        : serverStatusLine(summary) || t('Connecting…');
    main.appendChild(h('div', { class: 'empty' }, h('h2', { text: summary.name }), h('p', { text: why })));
    return;
  }
  if (selection.on && selection.channel !== c.id) { selection.on = false; selection.ids.clear(); }
  if (selection.on) main.appendChild(selectionBar(c));
  selection.channel = c.id;
  const list = h('div', { class: 'messages', id: 'messages', role: 'log', 'aria-live': 'polite' });
  list.addEventListener('scroll', () => { if (list.scrollTop < 60) loadOlder('main'); });
  main.appendChild(list);
  main.appendChild(jumpToLatest(list));
  main.appendChild(h('div', { class: 'typing small muted', id: 'typing', 'aria-live': 'polite' }));
  renderMessages();
  renderTyping();
  main.appendChild(composer(c, summary));
}

function banners(summary, s) {
  const out = [];
  if (['removed', 'banned', 'device_revoked', 'device_limit'].includes(summary.status)) {
    out.push(h('div', { class: 'banner bad' }, h('span', { class: 'spacer', text: summary.error }),
      summary.status === 'device_limit' ? h('button', { class: 'btn small', text: t('Try again'), onclick: () => api('retry', summary.id) }) : null,
      h('button', { class: 'btn small', text: t('Dismiss'), onclick: async () => { await api('dismiss', summary.id); st.cid = null; await refreshCommunities(); render(); } })));
  } else if (summary.connection === 'offline' || summary.connection === 'error') {
    const why = serverStatusLine(summary);
    out.push(h('div', { class: 'banner warn' }, h('span', { class: 'spacer', text: why ? t('{status}. Showing what this device has cached.', { status: why }) : summary.error || t('Offline: showing what this device has cached.') }),
      h('button', { class: 'btn small', text: t('Reconnect'), onclick: () => api('retry', summary.id) })));
  } else if (summary.connection === 'pending') {
    out.push(h('div', { class: 'banner info', text: t('Pending approval: an admin has to let you in. This updates by itself.') }));
  }
  const hosted = hostingBanner(summary);
  if (hosted) out.push(hosted);
  if (s && summary.is_owner && s.single_owner && !dismissed('single_owner', summary.id)) {
    out.push(h('div', { class: 'banner warn single-owner' }, h('span', { class: 'spacer', text: t(SINGLE_OWNER_WARNING) }),
      h('button', { class: 'btn small', text: t('Add an owner'), onclick: () => openCommunitySettings(summary.id, 'ownership') }),
      dismissButton(() => dismiss('single_owner', summary.id))));
  }
  if (s && s.self && s.self.owner_invited && !summary.is_owner) {
    out.push(h('div', { class: 'banner info' }, h('span', { class: 'spacer', text: t('You\'ve been invited to become an owner of this community.') }),
      h('button', { class: 'btn small', text: t('Review'), onclick: () => openCommunitySettings(summary.id, 'ownership') })));
  }
  if (st.status && st.status.backup_current === false && !dismissed('backup', st.status.keys_revision)) {
    out.push(h('div', { class: 'banner warn' }, h('span', { class: 'spacer', text: t('Your backup is out of date.') }),
      h('button', { class: 'btn small', text: t('Export backup'), onclick: exportBackupFlow }),
      dismissButton(() => dismiss('backup', st.status.keys_revision))));
  }
  return out;
}

// Banners someone dismissed (ui_state "dismissed": banner -> what it was
// about): a community's single-owner warning for good, the backup reminder
// until the keys change again.
let dismissedBanners = null;
function dismissed(banner, about) {
  if (dismissedBanners === null) {
    dismissedBanners = {};
    api('ui_state_get', 'dismissed').then((v) => { dismissedBanners = v || {}; renderMain(); }).catch(() => {});
    return true; // not known yet: don't flash it
  }
  const seen = dismissedBanners[banner];
  return Array.isArray(seen) ? seen.includes(about) : seen === about;
}
function dismiss(banner, about) {
  const d = { ...(dismissedBanners || {}) };
  d[banner] = banner === 'backup' ? about : [...new Set([...(Array.isArray(d[banner]) ? d[banner] : []), about])].slice(-500);
  dismissedBanners = d;
  api('ui_state_set', 'dismissed', d).catch(() => {});
  renderMain();
}
function dismissButton(run) {
  return h('button', { class: 'icon-btn', title: t('Dismiss'), 'aria-label': t('Dismiss'), icon: 'x', onclick: run });
}

function myKey() { const s = cstate(); return s && s.self ? s.self.key : ''; }

// Redraws the message lists on screen (only `only`'s when given).
function renderMessages(keepScroll = false, only = null) {
  for (const [name, p] of Object.entries(PANES)) {
    if (only && only !== name) continue;
    const list = document.getElementById(p.list);
    if (list) keepingFocus(list, () => renderMessagesNow(list, p, keepScroll));
  }
}

function renderMessagesNow(list, pane, keepScroll) {
  const k = key(st.cid, pane.channel());
  const msgs = st.msgs[k] || [];
  // Drawn again only when something it shows changed (the messages, the New
  // marker, people's names): redrawing the same list makes it flicker.
  const sig = JSON.stringify([k, chat.newSince[k], myKey(), msgs, (st.members[st.cid] || []).map((x) => [x.key, x.nickname, x.display_name])]);
  if (list.dataset.drawn === sig && !keepScroll) return;
  list.dataset.drawn = sig;
  const atBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 80;
  const prevHeight = list.scrollHeight;
  clear(list);
  if (!msgs.length) list.appendChild(h('div', { class: 'empty' }, h('p', { text: t('No messages yet. Say hello!') })));
  let prev = null;
  const byId = {};
  for (const m of msgs) byId[m.id] = m;
  const since = chat.newSince[k];
  const me = myKey();
  let marked = false;
  for (const m of msgs) {
    if (!prev || fmtDate(prev.created_at) !== fmtDate(m.created_at)) list.appendChild(h('div', { class: 'day', text: fmtDate(m.created_at) }));
    if (!marked && since !== undefined && m.id > since && m.author !== me) {
      list.appendChild(h('div', { class: 'new-marker', role: 'separator', text: t('New') }));
      marked = true;
    }
    const first = !prev || !!prev.system || prev.author !== m.author || m.created_at - prev.created_at > 5 * 60000 || m.reply_to || fmtDate(prev.created_at) !== fmtDate(m.created_at);
    list.appendChild(m.system ? systemEl(m) : messageEl(m, first, byId));
    prev = m;
  }
  if (keepScroll) list.scrollTop = list.scrollHeight - prevHeight;
  else if (atBottom || !chat[pane.scrolled]) { list.scrollTop = list.scrollHeight; chat[pane.scrolled] = true; }
}

function systemEl(m) {
  const what = m.system.kind === 'joined' ? t('joined the community') : m.system.kind === 'left' ? t('left the community') : t('did something');
  return h('div', { class: 'msg system', 'data-id': m.id },
    h('span', { class: 'small muted' }, m.system.kind === 'left' ? icon('log-out', 'inline') : icon('log-in', 'inline'),
      h('b', { class: 'author', text: m.author_name || t('Someone'), tabindex: m.author ? '0' : null,
        oncontextmenu: m.author ? (e) => userMenu(e, { cid: st.cid, key: m.author, name: m.author_name }) : null }), ` ${what} · ${fmtTime(m.created_at)}`));
}

function messageEl(m, first, byId) {
  const mine = m.author === myKey();
  const c = chanById(m.channel_id);
  const f = features(c);
  const can = c ? c.can : {};
  const el = h('div', { class: 'msg' + (first ? ' first' : '') + (m.deleted ? ' deleted' : '') + (m.mentions_me ? ' mentioned' : '') + (m.pinned ? ' pinned' : ''), 'data-id': m.id, tabindex: '0' });
  if (selection.on && !m.deleted && !m.system) el.appendChild(selectBox(m));
  // The author's name and avatar, and whoever a reply quotes, open the
  // person's menu on a right-click (or the Menu key on the focused name).
  const authorMenu = (e) => userMenu(e, { cid: st.cid, key: m.author, name: m.author_name }, { channel: m.channel_id });
  const av = avatarEl(m.author_name, m.author);
  memberAvatar(m.author, av);
  av.addEventListener('contextmenu', authorMenu);
  el.appendChild(av);
  const body = h('div', null);
  if (m.reply_to) {
    const r = byId[m.reply_to];
    const snippet = r ? (r.deleted ? t('deleted message') : plainText(r.text, mdCtx()) || (r.files && r.files.length ? r.files[0].name : '')) : t('a message');
    body.appendChild(h('button', { class: 'reply-ref', icon: 'corner-down-right', title: t('Jump to the message'), onclick: () => jumpToMessage(m.channel_id, m.reply_to),
      oncontextmenu: r ? (e) => userMenu(e, { cid: st.cid, key: r.author, name: r.author_name }, { channel: m.channel_id }) : null },
    r ? `${r.author_name}: ${snippet.slice(0, 80)}` : snippet));
  }
  body.appendChild(h('div', { class: 'mhead' }, h('span', { class: 'author', tabindex: '0', text: m.author_name || t('Unknown'), oncontextmenu: authorMenu }),
    h('time', { text: fmtTime(m.created_at), title: fmtDateTime(m.created_at) }),
    m.pinned ? h('span', { class: 'pill', text: t('pinned') }) : null));
  if (m.deleted) body.appendChild(h('div', { class: 'mtext', text: t('Message deleted') }));
  else {
    if (m.text) body.appendChild(renderText(m.text, mdCtx()));
    if (m.edited_at) body.appendChild(h('span', { class: 'edited', text: t(' (edited)'), title: fmtDateTime(m.edited_at) }));
    for (const file of m.files || []) body.appendChild(fileEl(file));
    if (m.preview) body.appendChild(previewEl(m.preview));
    const rx = reactionsEl(m, c);
    if (rx) body.appendChild(rx);
  }
  el.appendChild(body);
  if (!m.deleted) {
    const canReact = f.reactions && can.add_reactions;
    el.appendChild(h('div', { class: 'actions' },
      can.send_messages ? h('button', { class: 'icon-btn', title: t('Reply'), 'aria-label': t('Reply'), icon: 'reply', onclick: () => { st.replyTo = m; renderComposerReply(); const p = PANES[paneOf(m.channel_id) || 'main']; const i = document.getElementById(p.input); if (i) i.focus(); } }) : null,
      canReact ? h('button', { class: 'icon-btn', title: t('Add reaction'), 'aria-label': t('Add reaction'), icon: 'smile-plus', onclick: (e) => reactionMenu(e, m) }) : null,
      mine && f.edit_own ? h('button', { class: 'icon-btn', title: t('Edit'), 'aria-label': t('Edit'), icon: 'pencil', onclick: () => editMessage(m) }) : null,
      can.manage_messages ? h('button', { class: 'icon-btn', title: m.pinned ? t('Unpin') : t('Pin'), 'aria-label': m.pinned ? t('Unpin') : t('Pin'), icon: 'pin', onclick: () => pinMessage(m) }) : null,
      (mine && f.delete_own) || can.manage_messages ? h('button', { class: 'icon-btn', title: t('Delete'), 'aria-label': t('Delete'), icon: 'trash-2', onclick: () => deleteMessage(m) }) : null,
      can.manage_messages && !selection.on ? h('button', { class: 'icon-btn', title: t('Select'), 'aria-label': t('Select'), icon: 'square-check', onclick: () => startSelecting(m) }) : null,
      !mine ? h('button', { class: 'icon-btn', title: t('Report'), 'aria-label': t('Report'), icon: 'flag', onclick: () => reportDialog(st.cid, m) }) : null,
      h('button', { class: 'icon-btn', title: t('Copy text'), 'aria-label': t('Copy text'), icon: 'copy', onclick: () => api('copy_text', decodeMentions(m.text || '')) })));
  }
  return el;
}

// A reaction's label: Unicode emoji as is; custom emoji (":name:id") by name
// (docs/protocol.md §4.3).
function emojiLabel(emoji) {
  const custom = /^:([A-Za-z0-9_]{2,32}):(\d+)$/.exec(emoji);
  if (!custom) return h('span', { text: emoji });
  const e = customEmojiFor(st.cid).find((x) => x.id === Number(custom[2]) && x.name === custom[1]);
  return e ? emojiImg(st.cid, e) : h('span', { class: 'custom-emoji', 'data-emoji': emoji, text: `:${custom[1]}:` });
}

function reactionsEl(m, c) {
  const rx = m.reactions || [];
  if (!rx.length) return null;
  const me = myKey();
  const f = features(c);
  const can = c ? c.can : {};
  return h('div', { class: 'reactions' }, rx.map((r) => {
    const mine = r.members.includes(me);
    const allowed = f.reactions && (mine || can.add_reactions);
    return h('button', { class: 'reaction' + (mine ? ' mine' : ''), disabled: !allowed,
      title: tn('{n} person', '{n} people', r.members.length),
      'aria-label': `${r.emoji} ${r.members.length}${mine ? t(', you reacted') : ''}`,
      onclick: () => react(m, r.emoji, mine),
      // Who reacted, each with their own menu.
      oncontextmenu: (e) => whoReactedMenu(e, r.emoji, r.members.map((k) => ({ cid: st.cid, key: k, name: mdCtx().memberName(k) || t('Unknown') })), { channel: m.channel_id }) },
    emojiLabel(r.emoji), ` ${r.members.length}`);
  }));
}

function reactionMenu(e, m) {
  const c = chanById(m.channel_id);
  // This community's custom emoji, when the toggle is on and the member may
  // use them here (the server checks both again).
  const custom = c && c.can.use_custom_emoji ? customEmojiFor(st.cid) : [];
  openMenu(e.clientX, e.clientY, [
    ...QUICK_EMOJI.map((emoji) => ({ label: emoji, run: () => react(m, emoji, false) })),
    ...custom.map((x) => ({ label: `:${x.name}:`, run: () => react(m, `:${x.name}:${x.id}`, false) })),
    { label: t('Other…'), run: async () => {
      const emoji = await promptModal(t('Add reaction'), t('Emoji'), '', { ok: t('React') });
      if (emoji) react(m, emoji.trim(), false);
    } },
  ]);
}

async function react(m, emoji, remove) {
  // The reply is the message with its new reactions (null when this client
  // doesn't have the message: updates never carry message text).
  try { const u = await api('react', st.cid, m.id, emoji, remove); if (u) upsertMessage(st.cid, u); renderMessages(); } catch (e) { toastError(e); }
}

async function pinMessage(m) {
  try { const u = await api('pin', st.cid, m.id, !!m.pinned); if (u) upsertMessage(st.cid, u); renderMessages(); toast(m.pinned ? t('Unpinned.') : t('Pinned.')); }
  catch (e) { toastError(e); }
}

async function pinsDialog(c) {
  const list = h('div', { class: 'stack' }, h('div', { class: 'muted', text: t('Loading…') }));
  const ref = modal({ title: t('Pinned in #{channel}', { channel: c.name }), body: list });
  try {
    const pins = await api('pins', st.cid, c.id);
    clear(list);
    if (!pins.length) list.appendChild(emptyNote(t('Nothing is pinned here yet.')));
    for (const m of pins) {
      list.appendChild(h('button', { class: 'pin-row', onclick: () => { ref.close(); jumpToMessage(m.channel_id, m.id); } },
        h('div', { class: 'small muted', text: `${m.author_name} · ${fmtDateTime(m.created_at)}` }),
        h('div', { text: (plainText(m.text, mdCtx()) || (m.files && m.files.length ? m.files.map((f) => f.name).join(', ') : '')).slice(0, 200) })));
    }
  } catch (e) { clear(list).appendChild(h('div', { class: 'error', text: e.message })); }
}

function fileEl(f) {
  const wrap = h('div');
  if (f.expired) { wrap.appendChild(h('div', { class: 'file expired' }, h('span', { class: 'file-ic' }, icon('file-text')), h('span', { class: 'muted', text: t('{file} (expired)', { file: f.name }) }))); return wrap; }
  const isImage = String(f.mime).startsWith('image/') && f.size <= 8 * 1024 * 1024;
  wrap.appendChild(h('div', { class: 'file', 'data-file': f.name }, h('span', { class: 'file-ic' }, isImage ? icon('image') : icon('file-text')),
    h('div', null, h('div', { class: 'fname', text: f.name }), h('div', { class: 'small muted', text: fmtBytes(f.size) })),
    h('button', { class: 'btn small', text: t('Save…'), 'aria-label': t('Save {file}', { file: f.name }), onclick: async () => {
      const p = await pickSave('any', f.name); if (!p) return;
      try { await api('save_file', st.cid, f, p); toast(t('Saved {file}', { file: f.name })); } catch (e) { toastError(e); }
    } }),
    isImage ? h('button', { class: 'btn small', text: t('Show'), onclick: (e) => showPreview(f, wrap, e.currentTarget) }) : null));
  if (isImage && chat.previews[f.file_id]) wrap.appendChild(h('div', { class: 'file-preview' }, h('img', { src: chat.previews[f.file_id], alt: f.name })));
  return wrap;
}

async function showPreview(f, wrap, btn) {
  btn.disabled = true;
  try { const uri = await api('file_preview', st.cid, f); rememberPreview(f.file_id, uri); wrap.appendChild(h('div', { class: 'file-preview' }, h('img', { src: uri, alt: f.name }))); btn.remove(); }
  catch (e) { toastError(e); btn.disabled = false; }
}

// Previews are data URIs of up to a few MB: the newest few stay for the
// session. A failed fetch isn't tried again for a minute (''), and one in
// flight isn't asked for twice (null).
const PREVIEWS_KEPT = 50;
function rememberPreview(id, uri) {
  delete chat.previews[id];
  chat.previews[id] = uri;
  const ids = Object.keys(chat.previews);
  for (const old of ids.slice(0, Math.max(0, ids.length - PREVIEWS_KEPT))) delete chat.previews[old];
}

// A link preview the community server fetched; its image comes from the
// server too (never from the linked site).
function previewEl(p) {
  const card = h('div', { class: 'link-preview' },
    p.site_name ? h('div', { class: 'small muted', text: p.site_name }) : null,
    p.title ? h('div', { class: 'lp-title', text: p.title }) : null,
    p.description ? h('div', { class: 'small', text: p.description }) : null,
    h('div', { class: 'small' }, h('span', { class: 'link', role: 'button', tabindex: '0', text: p.url, onclick: () => linkDialog(p.url) })));
  const img = p.image;
  if (img && !img.expired) {
    const holder = h('div', { class: 'lp-image' });
    card.appendChild(holder);
    const show = (uri) => { if (uri) holder.appendChild(h('img', { src: uri, alt: p.title ? t('Preview image for {title}', { title: p.title }) : t('Preview image') })); };
    const have = chat.previews[img.file_id];
    if (have) show(have);
    else if (have === undefined) {
      chat.previews[img.file_id] = null;
      api('file_preview', st.cid, img).then((uri) => { rememberPreview(img.file_id, uri); show(uri); }).catch(() => {
        chat.previews[img.file_id] = '';
        setTimeout(() => { if (chat.previews[img.file_id] === '') delete chat.previews[img.file_id]; }, 60000);
      });
    }
  }
  return card;
}

const avatarCache = {};
function memberAvatar(memberKey, el, cid = st.cid) {
  const mem = (st.members[cid] || []).find((x) => x.key === memberKey);
  if (!mem || !mem.has_avatar) return;
  const k = `${cid}:${memberKey}:${mem.avatar_version}`;
  const set = (uri) => { if (uri) { clear(el).appendChild(h('img', { src: uri, alt: '' })); } };
  if (avatarCache[k] !== undefined) return set(avatarCache[k]);
  avatarCache[k] = '';
  api('avatar', cid, memberKey, mem.avatar_version).then((uri) => { avatarCache[k] = uri; set(uri); }).catch(() => {});
}

async function editMessage(m) {
  const before = decodeMentions(m.text);
  const text = await promptModal(t('Edit message'), t('Message'), before, { multiline: true });
  if (text === null || text === before) return;
  try { upsertMessage(st.cid, await api('edit', st.cid, m.id, text)); renderMessages(); } catch (e) { toastError(e); }
}

async function deleteMessage(m) {
  if (!(await confirmModal(t('Delete message?'), t('This deletes it for everyone.'), t('Delete')))) return;
  try { await api('delete_message', st.cid, m.channel_id, m.id); } catch (e) { toastError(e); }
}

const MSGS_KEPT_OFF_SCREEN = 500; // a channel not on screen keeps its newest messages only

function upsertMessage(cid, m, { onScreen = true } = {}) {
  const k = key(cid, m.channel_id);
  const arr = st.msgs[k] || (st.msgs[k] = []);
  const i = arr.findIndex((x) => x.id === m.id);
  if (i >= 0) arr[i] = m;
  else {
    let j = arr.length;
    while (j > 0 && arr[j - 1].id > m.id) j--;
    arr.splice(j, 0, m);
  }
  if (!onScreen && arr.length > 2 * MSGS_KEPT_OFF_SCREEN) arr.splice(0, arr.length - MSGS_KEPT_OFF_SCREEN);
}

// A community that is gone takes its messages and typing state with it.
function forgetCommunityMessages(cid) {
  for (const k of Object.keys(st.msgs)) if (k.startsWith(cid + ':')) delete st.msgs[k];
  for (const k of Object.keys(chat.typing)) if (k.startsWith(cid + ':')) delete chat.typing[k];
}

// ---------------------------------------------------------------- jump to message, unread

async function jumpToMessage(channelId, id) {
  leavePage(false);
  const voice = (chanById(channelId) || {}).kind === 'voice';
  if (voice) await voiceChat.open(channelId);
  else if (st.channel !== channelId) await selectChannel(channelId);
  const list = PANES[voice ? 'side' : 'main'].list;
  const find = () => $(`#${list} [data-id="${CSS.escape(String(id))}"]`);
  if (!find()) {
    try {
      const res = await api('history_around', st.cid, channelId, id);
      for (const m of res.messages) upsertMessage(st.cid, m);
      chat.more = chat.more || {};
      chat.more[key(st.cid, channelId)] = true;
      renderMessages(true, voice ? 'side' : 'main');
    } catch (e) { toastError(e); return; }
  }
  const el = find();
  if (!el) { toast(t('That message is no longer available.')); return; }
  el.scrollIntoView({ block: 'center' });
  el.classList.add('flash');
  setTimeout(() => el.classList.remove('flash'), 1600);
}

async function jumpToFirstMention(channelId) {
  leavePage(false);
  const c = chanById(channelId);
  const since = c ? c.last_read || 0 : 0;
  if (c && c.kind === 'voice') await voiceChat.open(channelId);
  else if (st.channel !== channelId) await selectChannel(channelId);
  const k = key(st.cid, channelId);
  const m = (st.msgs[k] || []).find((x) => x.id > since && x.mentions_me);
  if (m) jumpToMessage(channelId, m.id);
}

function markRead(cid, ch) {
  return api('mark_read', cid, ch, 0).catch(() => {});
}

// Mark the open channel read soon after new messages arrive while it's on screen.
function markReadSoon() {
  clearTimeout(chat.readTimer);
  chat.readTimer = setTimeout(() => {
    if (st.view !== 'community' || !st.cid || !document.hasFocus()) return;
    if (st.channel && document.getElementById(PANES.main.list)) markRead(st.cid, st.channel);
    if (st.voiceChat && document.getElementById(PANES.side.list)) markRead(st.cid, st.voiceChat);
  }, 400);
}

on('unread', (u) => {
  const s = st.states[u.community];
  const c = s && s.channels.find((x) => x.id === u.channel);
  if (c) { c.unread = u.unread; c.mentions = u.mentions; c.last_read = u.last_read; }
  const summary = st.communities.find((x) => x.id === u.community);
  if (summary) { summary.unread = u.community_unread; summary.mentions = u.community_mentions; }
  renderRail();
  if (u.community === st.cid && st.view === 'community') renderSidebar();
});

// ---------------------------------------------------------------- typing

function typingNames(cid, ch) {
  const now = Date.now();
  const typing = chat.typing[key(cid, ch)] || {};
  return Object.values(typing).filter((x) => x.until > now).map((x) => x.name);
}

function renderTyping() {
  for (const p of Object.values(PANES)) {
    const el = document.getElementById(p.typing);
    if (el) el.textContent = typingText(typingNames(st.cid, p.channel()));
  }
}

on('typing', (ev) => {
  const k = key(ev.community, ev.channel);
  const map = chat.typing[k] || (chat.typing[k] = {});
  map[ev.member] = { name: ev.name, until: Date.now() + TYPING_SHOWN_MS };
  renderTyping();
  setTimeout(renderTyping, TYPING_SHOWN_MS + 50);
});

// ---------------------------------------------------------------- composer

// Once a channel is open, typing goes straight into it.
function focusComposer(pane = 'main') {
  const input = document.getElementById(PANES[pane].input);
  if (input) input.focus();
}

function composer(c, summary, pane = 'main') {
  const p = PANES[pane];
  const box = h('div', { class: 'composer' });
  const reply = h('div', { id: p.reply });
  box.appendChild(reply);
  if (chat.uploading) box.appendChild(h('div', { class: 'upload-progress', id: p.progress, text: chat.uploading }));
  const locked = summary.connection === 'online' ? composerLock(cstate()) : null;
  if (locked) { box.appendChild(locked); return box; }
  if (!c.can.send_messages || summary.connection !== 'online') {
    box.appendChild(h('div', { class: 'composer-box' }, h('div', { class: 'muted',
      text: summary.connection !== 'online' ? t("You're offline from this community.") : t("You can't send messages in this channel.") })));
    return box;
  }
  const f = features(c);
  const label = c.kind === 'voice' ? t('Message {channel}', { channel: c.name }) : t('Message #{channel}', { channel: c.name });
  const input = h('textarea', { id: p.input, rows: 1, placeholder: label, 'aria-label': label, value: chat.drafts[key(st.cid, c.id)] || '' });
  const autosize = () => { input.style.height = 'auto'; input.style.height = Math.min(input.scrollHeight, window.innerHeight * 0.4) + 'px'; };
  // Before the Enter-to-send handler: while the list is open, Enter picks.
  box.appendChild(mentionPicker(input, st.cid));
  input.addEventListener('input', () => {
    autosize();
    saveDraft(st.cid, c.id, input.value);
    if (f.typing && input.value && Date.now() - chat.typingSent > 3000) { chat.typingSent = Date.now(); api('typing', st.cid, c.id).catch(() => {}); }
  });
  input.addEventListener('keydown', async (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      const text = input.value;
      if (!text.trim()) return;
      input.disabled = true;
      const cid = st.cid; // the rail stays clickable while the answer is on its way
      try {
        const replyTo = st.replyTo && st.replyTo.channel_id === c.id ? st.replyTo.id : 0;
        const m = await api('send', cid, c.id, text, replyTo, []);
        upsertMessage(cid, m);
        input.value = ''; saveDraft(cid, c.id, ''); if (replyTo) st.replyTo = null; renderComposerReply();
        chat.typingSent = 0;
        chat[p.scrolled] = false; renderMessages(false, pane);
      } catch (err) { toastError(err); }
      finally { input.disabled = false; input.focus(); autosize(); }
    } else if (e.key === 'Escape' && st.replyTo) { st.replyTo = null; renderComposerReply(); }
  });
  if (c.slowmode && !c.can.manage_messages && !c.can.manage_channels) {
    box.appendChild(h('div', { class: 'slowmode small muted', text: t('Slowmode is on: one message every {interval}.', { interval: fmtSeconds(c.slowmode) }) }));
  }
  box.appendChild(h('div', { class: 'composer-box' },
    f.attachments && c.can.attach_files ? h('button', { class: 'icon-btn', title: t('Attach files (up to 10)'), 'aria-label': t('Attach files'), icon: 'paperclip', onclick: () => channelAttachDialog(c, pane) }) : null,
    input, emojiPicker(c, input)));
  setTimeout(() => { autosize(); renderComposerReply(); }, 0);
  return box;
}

// Typing @ in a message box lists the members (and roles) whose name starts
// with what follows: arrows and Enter (or Tab), or a click, put in "@Name ",
// which Python turns into a mention when sending (messaging.encode_mentions).
function mentionPicker(input, cid) {
  const pop = h('div', { class: 'mention-pop hidden', role: 'listbox', 'aria-label': t('Mention someone') });
  let items = [];
  let sel = 0;
  let start = -1;
  const close = () => { pop.classList.add('hidden'); items = []; input.removeAttribute('aria-activedescendant'); };
  const pick = (it) => {
    const before = input.value.slice(0, start);
    const after = input.value.slice(input.selectionStart);
    input.value = `${before}@${it.name} ${after}`;
    const at = before.length + it.name.length + 2;
    input.setSelectionRange(at, at);
    close();
    input.dispatchEvent(new Event('input'));
    input.focus();
  };
  const draw = () => {
    clear(pop);
    items.forEach((it, i) => pop.appendChild(h('div', { class: 'mention-opt' + (i === sel ? ' sel' : ''), role: 'option', id: `mention-opt-${i}`,
      'aria-selected': String(i === sel), onmousedown: (e) => { e.preventDefault(); pick(it); } },
    it.role ? h('span', { class: 'muted', text: '@' }) : avatarEl(it.name, it.key, 'xs'),
    h('span', { text: it.name }), it.other ? h('span', { class: 'small muted', text: it.other }) : null)));
    input.setAttribute('aria-activedescendant', `mention-opt-${sel}`);
  };
  const update = () => {
    const upto = input.value.slice(0, input.selectionStart);
    const m = /(^|[\s(])@([^\s@<>]{0,32})$/.exec(upto);
    if (!m) { close(); return; }
    start = upto.length - m[2].length - 1;
    const q = m[2].toLowerCase();
    const members = st.members[cid];
    if (!members) { loadMembers(cid).then(() => { if (document.activeElement === input) update(); }).catch(() => {}); }
    const s = st.states[cid];
    const seen = new Set();
    const found = [];
    for (const mm of members || []) {
      const name = mm.nickname || mm.display_name;
      if (!name || seen.has(name.toLowerCase())) continue;
      const other = mm.nickname && mm.display_name !== mm.nickname ? mm.display_name : '';
      if (name.toLowerCase().startsWith(q) || (other && other.toLowerCase().startsWith(q))) { seen.add(name.toLowerCase()); found.push({ name, key: mm.key, other }); }
    }
    for (const r of (s && s.roles) || []) {
      if (r.name && r.name !== '@everyone' && r.name.toLowerCase().startsWith(q)) found.push({ name: r.name, role: true, other: t('role') });
    }
    for (const word of ['everyone', 'here', 'channel']) if (word.startsWith(q)) found.push({ name: word, role: true });
    items = found.slice(0, 8);
    sel = 0;
    if (!items.length) { close(); return; }
    pop.classList.remove('hidden');
    draw();
  };
  input.addEventListener('input', update);
  input.addEventListener('click', update);
  input.addEventListener('blur', () => setTimeout(close, 150));
  input.addEventListener('keydown', (e) => {
    if (!items.length) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      sel = (sel + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length;
      draw();
    } else if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault();
      e.stopImmediatePropagation();
      pick(items[sel]);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopImmediatePropagation();
      close();
    }
  });
  return pop;
}

function fmtSeconds(n) {
  if (n % 3600 === 0) return tn('hour', '{n} hours', n / 3600);
  if (n % 60 === 0) return tn('minute', '{n} minutes', n / 60);
  return tn('second', '{n} seconds', n);
}

function renderComposerReply() {
  for (const p of Object.values(PANES)) {
    const r = document.getElementById(p.reply);
    if (r) composerReplyInto(r, p);
  }
}

function composerReplyInto(r, pane) {
  clear(r);
  if (st.replyTo && st.replyTo.channel_id === pane.channel()) r.appendChild(h('div', { class: 'replying' }, h('span', { class: 'spacer', text: t('Replying to {name}', { name: st.replyTo.author_name }) }),
    h('button', { class: 'btn link small', text: t('Cancel'), onclick: () => { st.replyTo = null; renderComposerReply(); } })));
}

function saveDraft(cid, ch, text) {
  chat.drafts[key(cid, ch)] = text;
  clearTimeout(chat.draftTimer);
  chat.draftTimer = setTimeout(() => api('ui_state_set', 'drafts', chat.drafts).catch(() => {}), 600);
}

// Up to 10 files in one message, with the composer's text as the caption.
function channelAttachDialog(c, pane = 'main') {
  const p = PANES[pane];
  const s = cstate();
  const imagesOnly = features(c).images_only;
  const maxMb = s && s.settings ? Math.round(s.settings.max_file_size / 1048576) : 25;
  const chosen = [];
  const list = h('div', { class: 'stack', 'aria-live': 'polite' });
  const draw = () => {
    clear(list);
    if (!chosen.length) list.appendChild(h('div', { class: 'small muted', text: t('No files chosen yet.') }));
    chosen.forEach((p, i) => list.appendChild(h('div', { class: 'row' }, h('span', { class: 'spacer mono', text: p }),
      h('button', { class: 'btn link small', text: t('Remove'), 'aria-label': t('Remove {file}', { file: p }), onclick: () => { chosen.splice(i, 1); draw(); } }))));
  };
  const add = (p) => {
    p = p.trim();
    if (!p || chosen.includes(p)) return;
    if (chosen.length >= 10) { toast(t('At most 10 files per message.'), { error: true }); return; }
    chosen.push(p); draw();
  };
  const pathIn = h('input', { type: 'text', 'aria-label': t('File path'), placeholder: t('/path/to/file') });
  pathIn.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); add(pathIn.value); pathIn.value = ''; } });
  draw();
  modal({
    title: t('Attach files'),
    body: h('div', { class: 'stack' }, list,
      h('div', { class: 'row' }, h('button', { class: 'btn small', text: t('Browse…'), onclick: async () => { for (const p of await pickFiles()) add(p); } })),
      field(t('Or type a path'), h('div', { class: 'row' }, pathIn, h('button', { class: 'btn small', text: t('Add'), onclick: () => { add(pathIn.value); pathIn.value = ''; } })),
        imagesOnly ? t("Up to 10 files, {size} MB each, images only. The community's server stores them (its admins can read them).", { size: maxMb })
          : t("Up to 10 files, {size} MB each. The community's server stores them (its admins can read them).", { size: maxMb }))),
    actions: [
      { label: t('Cancel') },
      { label: t('Send'), kind: 'primary', run: async () => {
        if (!chosen.length) { toast(t('Choose at least one file.'), { error: true }); return false; }
        const input = document.getElementById(p.input);
        const caption = input ? input.value : '';
        chat.uploading = t('Uploading…');
        const prog = document.getElementById(p.progress);
        if (prog) prog.textContent = chat.uploading;
        const replyTo = st.replyTo && st.replyTo.channel_id === c.id ? st.replyTo.id : 0;
        const cid = st.cid;
        try {
          const files = await confirmOpenAll(chosen);
          if (!files.length) return false;
          const m = await api('send', cid, c.id, caption, replyTo, files);
          upsertMessage(cid, m);
          if (input) { input.value = ''; saveDraft(cid, c.id, ''); }
          if (replyTo) st.replyTo = null;
        } finally { chat.uploading = null; }
        chat[p.scrolled] = false;
        if (pane === 'side') voiceChat.render(true); else renderMain();
        return true;
      } },
    ],
  });
}

on('upload', (u) => {
  chat.uploading = t('Uploading {file}: {percent}%', { file: u.name, percent: Math.round((100 * u.sent) / u.size) });
  for (const p of Object.values(PANES)) {
    const el = document.getElementById(p.progress);
    if (el) el.textContent = chat.uploading;
  }
});

async function loadHistory(cid, ch) {
  const k = key(cid, ch);
  try {
    const cached = await api('cached_messages', cid, ch);
    if (!st.msgs[k] || !st.msgs[k].length) { st.msgs[k] = cached; if (st.cid === cid && paneOf(ch)) renderMessages(false, paneOf(ch)); }
  } catch (e) { /* not fatal */ }
  try {
    const before = new Set((st.msgs[k] || []).map((m) => m.id));
    const res = await api('history', cid, ch, 0);
    if (!res.offline) {
      // The server's newest page is the truth for its range: what it no
      // longer has (past retention) leaves the view too. Messages that
      // arrived during the call, and older pages still on the server, stay.
      const ids = new Set(res.messages.map((m) => m.id));
      const oldest = res.messages.length ? Math.min(...ids) : Infinity;
      st.msgs[k] = (st.msgs[k] || []).filter((m) => !before.has(m.id) || ids.has(m.id) || (res.more && m.id < oldest));
    }
    for (const m of res.messages) upsertMessage(cid, m);
    chat.more = chat.more || {};
    chat.more[k] = res.more;
    if (st.cid === cid && paneOf(ch)) renderMessages(false, paneOf(ch));
  } catch (e) { toastError(e); }
}

async function loadOlder(pane) {
  const ch = PANES[pane].channel();
  const k = key(st.cid, ch);
  if (!chat.more || !chat.more[k] || chat.loadingOlder) return;
  const arr = st.msgs[k] || [];
  if (!arr.length) return;
  chat.loadingOlder = true;
  try {
    const res = await api('history', st.cid, ch, arr[0].id);
    for (const m of res.messages) upsertMessage(st.cid, m);
    chat.more[k] = res.more;
    renderMessages(true, pane);
  } catch (e) { toastError(e); }
  chat.loadingOlder = false;
}

function searchDialog() {
  const q = h('input', { type: 'search', placeholder: t('Search this community'), 'aria-label': t('Search this community') });
  const results = h('div', { class: 'stack' });
  const run = async () => {
    clear(results).appendChild(h('div', { class: 'muted', text: t('Searching…') }));
    try {
      const hits = await api('search_messages', st.cid, q.value, 0);
      clear(results);
      if (!hits.length) results.appendChild(emptyNote(t('Nothing found.')));
      for (const m of hits) {
        const c = chanById(m.channel_id);
        results.appendChild(h('div', { class: 'result', role: 'button', tabindex: '0', onclick: () => { modalRef.close(); jumpToMessage(m.channel_id, m.id); } },
          h('div', { class: 'small muted' }, c && c.kind === 'voice' ? icon('volume-2', 'inline') : icon('hash', 'inline'), `${c ? c.name : '?'} · ${m.author_name} · ${fmtDateTime(m.created_at)}`),
          m.text ? renderText(m.text, mdCtx()) : h('div', { class: 'mtext', text: (m.files || []).map((f) => f.name).join(', ') })));
      }
    } catch (e) { clear(results).appendChild(h('div', { class: 'error', text: e.message })); }
  };
  q.addEventListener('keydown', (e) => { if (e.key === 'Enter') run(); });
  const modalRef = modal({ title: t('Search'), body: h('div', { class: 'stack' }, q, h('div', { class: 'hint', text: t("Only channels you can read are searched. Results come from the community's server.") }), results) });
}

// ---------------------------------------------------------------- members

function toggleMembers() {
  st.showMembers = !st.showMembers;
  api('ui_state_set', 'show_members', st.showMembers).catch(() => {});
  renderMembers();
  renderMain();
  if (st.showMembers && st.cid) loadMembers(st.cid);
}

async function loadMembers(cid) {
  try { st.members[cid] = await api('members', cid); } catch (e) { /* offline */ }
  if (cid === st.cid) { renderMembers(); renderMessages(); }
}

function shownName(m) { return m.nickname || m.display_name; }

function renderMembers() { return keepingFocus($('#members'), renderMembersNow); }

function renderMembersNow() {
  const panel = $('#members');
  if (voiceChat.shown()) { voiceChat.render(); return; }
  voiceChat.leavePanel(panel);
  panel.classList.toggle('hidden', !st.showMembers || st.view !== 'community' || !st.cid);
  if (panel.classList.contains('hidden')) return;
  clear(panel);
  const s = cstate();
  const list = (st.members[st.cid] || []).slice().sort((a, b) => shownName(a).toLowerCase().localeCompare(shownName(b).toLowerCase()));
  const roles = s ? Object.fromEntries(s.roles.map((r) => [r.id, r])) : {};
  // Grouped like Discord: online members under their highest "display
  // separately" role (highest roles first), other online members under
  // Online, and everyone offline under Offline.
  const groups = new Map();
  const offline = { label: t('Offline'), role: null, members: [] };
  for (const m of list) {
    const held = m.role_ids.map((id) => roles[id]).filter(Boolean).sort((a, b) => b.position - a.position);
    if (!m.online) { offline.members.push({ m, top: held[0] }); continue; }
    const hoisted = held.find((r) => r.hoist);
    const gk = hoisted ? hoisted.id : 0;
    if (!groups.has(gk)) groups.set(gk, { label: hoisted ? hoisted.name : t('Online'), role: hoisted || null, members: [] });
    groups.get(gk).members.push({ m, top: held[0] });
  }
  const ordered = [...groups.values()].sort((a, b) => (b.role ? b.role.position : -1) - (a.role ? a.role.position : -1));
  if (offline.members.length) ordered.push(offline);
  for (const g of ordered) {
    panel.appendChild(h('div', { class: 'side-group member-group', 'data-role': g.role ? String(g.role.id) : '', text: `${g.label} · ${g.members.length}` }));
    for (const { m, top } of g.members) {
      const av = avatarEl(shownName(m), m.key, 'sm');
      memberAvatar(m.key, av);
      // Their status (status.js): the dot, the custom text under the name,
      // and both in words for screen readers.
      const status = memberStatus(st.cid, m);
      const desc = statusDesc(status);
      panel.appendChild(h('button', { class: 'member' + (m.online ? ' online' : ' offline'), 'aria-label': shownName(m), title: statusSentence(status),
        'aria-describedby': desc.id, 'data-member': m.key, 'data-status': status.state, 'aria-haspopup': 'menu', onclick: (e) => memberMenu(e, m), oncontextmenu: (e) => memberMenu(e, m) },
      withStatus(av, status),
      h('span', { class: 'mname-wrap' }, h('span', { class: 'mname', text: shownName(m), style: roleColorStyle(top && top.color) }), statusTextEl(status)),
      desc,
      m.pronouns ? h('span', { class: 'small muted pronouns', text: m.pronouns }) : null,
      m.owner ? h('span', { class: 'pill', text: t('owner') }) : null,
      timedOut(m) ? h('span', { class: 'pill warn', title: t('Timed out until {time}', { time: fmtUntil(m.timeout_until) }), text: t('timed out') }) : null));
    }
  }
}

// Someone who just joined comes online before the page has them: the
// member list is loaded again (once for a burst of joins).
const membersReload = {};
on('presence', (p) => {
  const list = st.members[p.community];
  const known = list && list.find((m) => m.key === p.member);
  if (known) known.online = p.online;
  else if (list && p.online && !membersReload[p.community]) {
    membersReload[p.community] = setTimeout(() => { delete membersReload[p.community]; loadMembers(p.community); }, 500);
  }
  if (p.community === st.cid) renderMembers();
});

// A member-list row: a click or a right-click opens the person's menu (people.js).
function memberMenu(e, m) { return userMenu(e, { cid: st.cid, key: m.key, member: m }); }

function profileCard(m, cid = st.cid) {
  const s = st.states[cid];
  const roles = s ? s.roles.filter((r) => m.role_ids.includes(r.id)) : [];
  const av = avatarEl(shownName(m), m.key);
  memberAvatar(m.key, av, cid);
  modal({ title: shownName(m), body: h('div', { class: 'stack profile-card' },
    h('div', { class: 'row' }, av, h('div', null,
      h('div', { class: 'lp-title', text: shownName(m) }),
      m.nickname ? h('div', { class: 'small muted', text: t('Display name: {name}', { name: m.display_name }) }) : null,
      m.pronouns ? h('div', { class: 'small', text: m.pronouns }) : null,
      ...profileStatus(memberStatus(cid, m)))),
    m.about ? h('div', null, h('div', { class: 'group-label', text: t('About') }), h('div', { class: 'about', text: m.about })) : null,
    roles.length ? h('div', { class: 'row wrap' }, ...roles.map((r) => h('span', { class: 'pill', text: r.name, style: roleColorStyle(r.color) }))) : null,
    h('div', { class: 'small muted', text: t('Member since {date}', { date: fmtDateTime(m.joined_at) }) })),
  actions: [{ label: t('Close') }] });
}
