// Direct messages and group DMs. DMs never touch community servers: they go
// through relays and maintainer mailboxes, encrypted end to end (vodozemac).
'use strict';

const QUICK_EMOJI = ['👍', '❤️', '😂', '🎉', '😮', '😢'];

const dmUI = {
  list: [],
  current: null,
  info: null,
  history: [],
  replyTo: null,
  // The read position when the open conversation was opened.
  newSince: null,
  typingSent: 0,
  // The composer survives re-renders (new messages arrive while typing).
  drafts: {},
  composerFocused: false,
  unreadTotal() { return this.list.reduce((a, c) => a + (c.unread || 0), 0); },

  async refresh() {
    try { this.list = await api('dm_list'); } catch (e) { this.list = []; }
    if (this.current && !this.list.find((c) => c.id === this.current)) { this.current = null; this.info = null; }
    renderRail();
    if (st.view === 'dms') {
      renderSidebar();
      if (!dmViewMatches()) renderMain();
    }
  },

  // Loads `id` into info/history, unless another conversation was opened
  // while its answers were on the way (then those are for that one).
  async load(id) {
    try {
      const info = await api('dm_info', id);
      if (this.current !== id) return;
      const history = await api('dm_history', id);
      if (this.current !== id) return;
      this.info = info;
      this.history = history;
    } catch (e) { if (this.current !== id) return; this.info = null; this.history = []; toastError(e); }
  },

  async open(id) {
    if (this.current !== id) { this.replyTo = null; this.info = null; this.history = []; }
    this.current = id;
    leavePage();
    st.view = 'dms';
    await this.load(id);
    if (this.current !== id) return;
    // The "New" marker stays where it was when the conversation was opened,
    // though opening it marks everything read (as in channels).
    this.newSince = this.info ? this.info.read_at : null;
    await this.refresh();
    if (this.info && this.info.status === 'active') api('dm_mark_read', id).catch(() => {});
  },

  async reload() {
    const id = this.current;
    if (!id) return;
    await this.load(id);
    if (st.view !== 'dms' || this.current !== id) return;
    if (dmViewMatches()) fillDmView();
    else renderMain();
  },

  // The contacts page: the main panel when no conversation is open.
  home() {
    this.current = null; this.info = null; this.history = []; this.replyTo = null;
    leavePage();
    st.view = 'dms';
    render();
  },

  async startWith(cid, member) {
    try { const r = await api('dm_open', cid, member.key); await this.open(r.id); }
    catch (e) { toastError(e); }
  },

  async startWithContact(id) {
    try { const r = await api('dm_open_contact', id); await this.open(r.id); }
    catch (e) { toastError(e); }
  },
};

function dmCall(...args) {
  return api(...args).then(() => dmUI.reload(), toastError);
}

// ---------------------------------------------------------------- sidebar

function renderDmSidebar(side) {
  side.appendChild(h('div', { class: 'side-head' }, h('span', { class: 'name', text: t('Direct messages') }),
    h('button', { class: 'icon-btn', title: t('New group DM'), 'aria-label': t('New group DM'), icon: 'plus', onclick: (e) => { e.stopPropagation(); newGroupDialog(); } }),
    h('button', { class: 'icon-btn', title: t('Check for new messages'), 'aria-label': t('Check for new messages'), icon: 'refresh-cw', onclick: (e) => { e.stopPropagation(); api('dm_poll').then(() => dmUI.refresh(), toastError); } })));
  const body = h('div', { class: 'side-body dm-list' });
  body.appendChild(h('button', { class: 'chan dm-home' + (dmUI.current ? '' : ' sel'), onclick: () => dmUI.home() },
    icon('users'), h('span', { class: 'name', text: t('Contacts') })));
  const waiting = dmUI.list.filter((c) => c.status === 'request' || c.status === 'invited');
  const rest = dmUI.list.filter((c) => c.status !== 'request' && c.status !== 'invited');
  if (!dmUI.list.length) body.appendChild(h('div', { class: 'small muted',
    text: t('No conversations yet. Message someone from Contacts, or right-click someone in a community and choose "Message".') }));
  if (waiting.length) body.appendChild(h('div', { class: 'cat', text: t('Requests and invitations') }));
  for (const c of waiting) body.appendChild(dmListItem(c));
  if (waiting.length && rest.length) body.appendChild(h('div', { class: 'cat', text: t('Conversations') }));
  for (const c of rest) body.appendChild(dmListItem(c));
  side.appendChild(body);
  side.appendChild(userPanel(null));
}

function dmListItem(c) {
  // A 1:1 conversation shows the person's status (status.js).
  const peer = c.direct && (c.peers || [])[0];
  const status = peer && c.status === 'active' ? personStatus(peer) : null;
  const desc = status ? statusDesc(status) : null;
  const av = c.icon ? h('img', { class: 'group-icon sm', src: c.icon, alt: '' }) : avatarEl(c.name, c.id, 'sm');
  return h('button', { class: 'chan' + (c.id === dmUI.current ? ' sel' : '') + (c.unread ? ' unread' : ''), 'data-dm': c.id,
    'aria-label': c.unread ? t('{name}, {n} unread', { name: c.name, n: c.unread }) : c.name, onclick: () => dmUI.open(c.id),
    'aria-describedby': desc ? desc.id : null, 'data-status': status ? status.state : null, title: status ? statusSentence(status) : null,
    // A 1:1 conversation's row is the person: their menu (people.js).
    oncontextmenu: c.direct ? (e) => dmRowMenu(e, c) : null },
  status ? withStatus(av, status) : av,
  status ? h('span', { class: 'cname-wrap' }, h('span', { class: 'cname', text: c.name }), statusTextEl(status)) : h('span', { class: 'cname', text: c.name }),
  desc,
  c.status === 'request' ? h('span', { class: 'pill warn', text: t('request') }) : null,
  c.status === 'invited' ? h('span', { class: 'pill warn', text: t('invited') }) : null,
  c.status === 'removed' ? h('span', { class: 'pill', text: t('removed') }) : null,
  c.blocked ? h('span', { class: 'pill', text: t('blocked') }) : null,
  c.mentions ? h('span', { class: 'pill mention', text: '@' }) : null,
  c.unread ? h('span', { class: 'pill count', text: String(c.unread) }) : null);
}

async function dmRowMenu(e, c) {
  e.preventDefault();
  const x = e.clientX;
  const y = e.clientY;
  let info;
  try { info = await api('dm_info', c.id); } catch (err) { toastError(err); return; }
  const people = [...info.members, ...info.invited];
  const p = people.find((m) => !m.me);
  if (p) openUserMenu(x, y, { person: p }, { dm: info, invited: info.invited.includes(p), after: () => dmUI.refresh() });
}

// The participant a DM message or reaction names, by identity key.
function dmPerson(info, ident) {
  return [...info.members, ...info.invited].find((p) => p.id === ident) || null;
}

// ---------------------------------------------------------------- conversation

// The conversation pane is built once per conversation (and status); new
// data then updates the header, banners and messages in place, so the
// composer (what's being typed, focus, caret) is never rebuilt mid-sentence.
function dmViewMatches() {
  const v = $('#dm-view');
  return !!(v && dmUI.info && v.dataset.gid === dmUI.current && v.dataset.status === dmUI.info.status);
}

function renderDmMain(main) {
  const c = dmUI.list.find((x) => x.id === dmUI.current);
  const info = dmUI.info;
  if (!c || !info) {
    renderDmHome(main);
    return;
  }
  const view = h('div', { id: 'dm-view', class: 'dm-view', 'data-gid': c.id, 'data-status': info.status },
    h('div', { id: 'dm-head' }), h('div', { id: 'dm-banners' }),
    h('div', { class: 'messages', id: 'dm-messages', role: 'log', 'aria-live': 'polite' }),
    h('div', { class: 'typing small muted', id: 'dm-typing', 'aria-live': 'polite' }));
  if (info.status === 'active') view.appendChild(dmComposer(c, info));
  main.appendChild(view);
  fillDmView(true);
}

function fillDmView(first = false) {
  const c = dmUI.list.find((x) => x.id === dmUI.current) || { id: dmUI.current, name: dmUI.info.name || t('Conversation') };
  const info = dmUI.info;
  const others = info.members.filter((m) => !m.me);
  clear($('#dm-head')).appendChild(h('div', { class: 'main-head' },
    h('span', { class: 'title', text: c.name }),
    info.direct ? null : h('span', { class: 'small muted', text: t('{n} members', { n: info.members.length }) }),
    h('div', { class: 'spacer' }),
    h('span', { class: 'small muted', text: t('End-to-end encrypted') }),
    h('button', { class: 'icon-btn', title: t('Conversation details'), 'aria-label': t('Conversation details'), icon: 'info', onclick: () => dmInfoDialog() })));
  dmBanners(clear($('#dm-banners')), c, info, others);
  // The owner can turn attachments on and off while the composer is open.
  const attach = $('#dm-attach');
  if (attach) attach.classList.toggle('hidden', !info.allow_attachments);
  const list = $('#dm-messages');
  const atBottom = first || list.scrollHeight - list.scrollTop - list.clientHeight < 40;
  clear(list);
  const byId = Object.fromEntries(dmUI.history.map((m) => [m.id, m]));
  let prev = null;
  let markedNew = false;
  for (const m of dmUI.history) {
    if (!prev || fmtDate(prev.at) !== fmtDate(m.at)) list.appendChild(h('div', { class: 'day', text: fmtDate(m.at) }));
    if (!markedNew && !m.mine && !m.system && dmUI.newSince && (m.recv_at || m.at) > dmUI.newSince) {
      list.appendChild(h('div', { class: 'new-marker', text: t('New') }));
      markedNew = true;
    }
    if (m.system) list.appendChild(h('div', { class: 'msg system', 'data-id': m.id }, h('span', { class: 'small muted', text: t('{who} {action} · {time}', { who: m.mine ? t('You') : m.name, action: m.text, time: fmtTime(m.at) }) })));
    else {
      const first = !prev || prev.system || prev.sender !== m.sender || m.at - prev.at > 300000;
      list.appendChild(dmMessageEl(m, first, byId, info));
    }
    prev = m;
  }
  if (!dmUI.history.length) list.appendChild(h('div', { class: 'empty' }, h('p', { text: t('No messages yet.') })));
  if (atBottom) setTimeout(() => { list.scrollTop = list.scrollHeight; }, 0);
  $('#dm-typing').textContent = typingText(info.typing);
}

function typingText(names) {
  if (!names || !names.length) return '';
  if (names.length === 1) return t('{name} is typing…', { name: names[0] });
  if (names.length <= 3) return t('{names} are typing…', { names: names.join(', ') });
  return t('Several people are typing…');
}

function dmBanners(main, c, info, others) {
  if (info.status === 'request') {
    main.appendChild(h('div', { class: 'banner info' }, h('span', { class: 'spacer', text: t("{name} isn't in your contacts and wants to message you.", { name: c.name }) }),
      h('button', { class: 'btn small primary', text: t('Accept'), onclick: () => api('dm_accept', c.id).then(() => dmUI.open(c.id), toastError) }),
      h('button', { class: 'btn small', text: t('Decline'), onclick: () => api('dm_decline', c.id).then(() => { dmUI.current = null; dmUI.refresh(); }, toastError) })));
  } else if (info.status === 'invited') {
    main.appendChild(h('div', { class: 'banner info' }, h('span', { class: 'spacer', text: t('You were invited to "{name}". You\'ll only see messages sent after you join.', { name: c.name }) }),
      h('button', { class: 'btn small primary', text: t('Join'), onclick: () => api('dm_accept', c.id).then(() => dmUI.open(c.id), toastError) }),
      h('button', { class: 'btn small', text: t('Decline'), onclick: () => api('dm_decline', c.id).then(() => { dmUI.current = null; dmUI.refresh(); }, toastError) })));
  } else if (info.status === 'removed') {
    main.appendChild(h('div', { class: 'banner' }, h('span', { class: 'spacer', text: t('You were removed from this conversation. You can still read what you received before.') }),
      h('button', { class: 'btn small', text: t('Delete conversation'), onclick: () => api('dm_leave', c.id).then(() => { dmUI.current = null; dmUI.refresh(); }, toastError) })));
  }
  for (const m of others.filter((x) => x.key_changed)) {
    main.appendChild(h('div', { class: 'banner warn' }, h('span', { class: 'spacer', text: t("{name}'s key changed. This happens when they reinstall or reset their profile, or when someone else is pretending to be them. Check their safety number before trusting new messages.", { name: m.name }) }),
      h('button', { class: 'btn small', text: t('Safety number'), onclick: () => safetyDialog(m.id, m.name, info.id) })));
  }
  if (info.direct && others.some((m) => m.blocked)) {
    main.appendChild(h('div', { class: 'banner' }, h('span', { class: 'spacer', text: t("You blocked this person. You won't receive their messages.") }),
      h('button', { class: 'btn small', text: t('Unblock'), onclick: () => dmCall('dm_block', others[0].id, false) })));
  }
}

function dmMessageEl(m, first, byId, info) {
  const el = h('div', { class: 'msg' + (first ? ' first' : '') + (m.deleted ? ' deleted' : '') + (m.mentions_me ? ' mentioned' : '') + (m.pinned ? ' pinned' : ''), 'data-id': m.id, tabindex: '0' });
  // Whoever sent it (name and avatar) or is quoted has a menu (people.js).
  const menuFor = (ident, name) => (e) => {
    const p = dmPerson(info, ident);
    userMenu(e, p ? { person: p } : { ident, name }, { dm: info, invited: !!p && info.invited.includes(p) });
  };
  const authorMenu = menuFor(m.sender, m.name);
  const av = avatarEl(m.mine ? t('You') : m.name, m.sender);
  av.addEventListener('contextmenu', authorMenu);
  el.appendChild(av);
  const body = h('div', null);
  if (m.reply_to) {
    const r = byId[m.reply_to];
    body.appendChild(h('button', { class: 'reply-ref', icon: 'corner-down-right', onclick: () => jumpTo(m.reply_to), oncontextmenu: r ? menuFor(r.sender, r.name) : null },
      r ? t('{name}: {text}', { name: r.mine ? t('You') : r.name, text: (r.deleted ? t('deleted message') : r.text || '').slice(0, 80) }) : t('a message')));
  }
  body.appendChild(h('div', { class: 'mhead' }, h('span', { class: 'author', tabindex: '0', text: m.mine ? t('You') : m.name, oncontextmenu: authorMenu }),
    h('time', { text: fmtTime(m.at), title: fmtDateTime(m.at) }),
    m.pinned ? h('span', { class: 'pill', text: t('pinned') }) : null));
  if (m.deleted) body.appendChild(h('div', { class: 'mtext', text: t('Message deleted') }));
  else {
    // DMs mention by plain @name (dm.py marks who); formatting as in channels.
    if (m.text) body.appendChild(renderText(m.text, { plainMentions: true, onLink: linkDialog }));
    if (m.edited_at) body.appendChild(h('span', { class: 'edited', text: t(' (edited)'), title: fmtDateTime(m.edited_at) }));
    for (const f of m.files || []) body.appendChild(dmFileEl(m, f));
    const rx = Object.entries(m.reactions || {});
    if (rx.length) {
      body.appendChild(h('div', { class: 'reactions' }, rx.map(([emoji, who]) => {
        const mine = who.includes(info.me);
        return h('button', { class: 'reaction' + (mine ? ' mine' : ''), title: tn('{n} person', '{n} people', who.length),
          'aria-label': mine ? t('{emoji} {n}, you reacted', { emoji, n: who.length }) : `${emoji} ${who.length}`,
          onclick: () => dmCall('dm_react', dmUI.current, m.id, emoji, mine),
          oncontextmenu: (e) => whoReactedMenu(e, emoji, who.map((ident) => {
            const p = dmPerson(info, ident);
            return p ? { person: p, name: p.me ? t('{name} (you)', { name: p.name }) : p.name } : { ident, name: t('Someone') };
          }), { dm: info }) }, `${emoji} ${who.length}`);
      })));
    }
  }
  el.appendChild(body);
  if (!m.deleted && info.status === 'active') {
    el.appendChild(h('div', { class: 'actions' },
      h('button', { class: 'icon-btn', title: t('Reply'), 'aria-label': t('Reply'), icon: 'reply', onclick: () => { dmUI.replyTo = m; renderDmReply(); const i = $('#dm-input'); if (i) i.focus(); } }),
      h('button', { class: 'icon-btn', title: t('Add reaction'), 'aria-label': t('Add reaction'), icon: 'smile-plus', onclick: (e) => reactMenu(e, m) }),
      m.mine ? h('button', { class: 'icon-btn', title: t('Edit'), 'aria-label': t('Edit'), icon: 'pencil', onclick: () => dmEdit(m) }) : null,
      h('button', { class: 'icon-btn', title: m.pinned ? t('Unpin') : t('Pin'), 'aria-label': m.pinned ? t('Unpin') : t('Pin'), icon: 'pin', onclick: () => dmCall('dm_pin', dmUI.current, m.id, !!m.pinned) }),
      m.mine ? h('button', { class: 'icon-btn', title: t('Delete'), 'aria-label': t('Delete'), icon: 'trash-2', onclick: () => dmDelete(m) }) : null,
      h('button', { class: 'icon-btn', title: t('Copy text'), 'aria-label': t('Copy text'), icon: 'copy', onclick: () => api('copy_text', m.text || '') })));
  }
  return el;
}

function jumpTo(id) {
  const el = $(`#dm-messages [data-id="${CSS.escape(id)}"]`);
  if (!el) { toast(t('That message is no longer in this conversation.')); return; }
  el.scrollIntoView({ block: 'center' });
  el.classList.add('flash');
  setTimeout(() => el.classList.remove('flash'), 1600);
}

function reactMenu(e, m) {
  openMenu(e.clientX, e.clientY, [
    ...QUICK_EMOJI.map((emoji) => ({ label: emoji, run: () => dmCall('dm_react', dmUI.current, m.id, emoji, false) })),
    { label: t('Other…'), run: async () => {
      const emoji = await promptModal(t('Add reaction'), t('Emoji'), '', { ok: t('React') });
      if (emoji) dmCall('dm_react', dmUI.current, m.id, emoji, false);
    } },
  ]);
}

async function dmEdit(m) {
  const text = await promptModal(t('Edit message'), t('Message'), m.text, { multiline: true });
  if (text !== null && text !== m.text) dmCall('dm_edit', dmUI.current, m.id, text);
}

async function dmDelete(m) {
  if (await confirmModal(t('Delete message'), t('Delete this message for everyone in the conversation?'), t('Delete'))) dmCall('dm_delete', dmUI.current, m.id);
}

function dmComposer(c, info) {
  const box = h('div', { class: 'composer' });
  box.appendChild(h('div', { id: 'dm-reply' }));
  const input = h('textarea', { id: 'dm-input', rows: 1, placeholder: t('Message {name}', { name: c.name }), 'aria-label': t('Message {name}', { name: c.name }), value: dmUI.drafts[c.id] || '' });
  input.addEventListener('focus', () => { dmUI.composerFocused = true; });
  input.addEventListener('blur', () => { if (input.isConnected) dmUI.composerFocused = false; });
  const autosize = () => { input.style.height = 'auto'; input.style.height = Math.min(input.scrollHeight, window.innerHeight * 0.4) + 'px'; };
  input.addEventListener('input', () => {
    dmUI.drafts[c.id] = input.value;
    autosize();
    if (Date.now() - dmUI.typingSent > 3000 && input.value) { dmUI.typingSent = Date.now(); api('dm_typing', c.id, true).catch(() => {}); }
  });
  input.addEventListener('keydown', async (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      if (!input.value.trim()) return;
      input.disabled = true;
      try {
        await api('dm_send', c.id, input.value, dmUI.replyTo ? dmUI.replyTo.id : '');
        input.value = '';
        delete dmUI.drafts[c.id];
        dmUI.replyTo = null;
        dmUI.typingSent = 0;
        await dmUI.reload();
      } catch (err) { toastError(err); }
      finally { const i = $('#dm-input'); if (i) { i.disabled = false; i.focus(); } }
    } else if (e.key === 'Escape' && dmUI.replyTo) { dmUI.replyTo = null; renderDmReply(); }
  });
  box.appendChild(h('div', { class: 'composer-box' },
    h('button', { id: 'dm-attach', class: 'icon-btn' + (info.allow_attachments ? '' : ' hidden'), title: t('Attach files (up to 10, 25 MB each)'), 'aria-label': t('Attach files'), icon: 'paperclip', onclick: () => attachDialog(c) }),
    input));
  setTimeout(() => {
    autosize();
    renderDmReply();
    if (dmUI.composerFocused && input.isConnected) {
      input.focus();
      input.setSelectionRange(input.value.length, input.value.length);
    }
  }, 0);
  return box;
}

function renderDmReply() {
  const r = $('#dm-reply');
  if (!r) return;
  clear(r);
  const m = dmUI.replyTo;
  if (m) r.appendChild(h('div', { class: 'replying' }, h('span', { class: 'spacer', text: m.mine ? t('Replying to yourself') : t('Replying to {name}', { name: m.name }) }),
    h('button', { class: 'btn link small', text: t('Cancel'), onclick: () => { dmUI.replyTo = null; renderDmReply(); } })));
}

// ---------------------------------------------------------------- attachments

function dmFileEl(m, f) {
  const isImage = String(f.mime).startsWith('image/');
  const status = f.state === 'expired' ? t('expired') : f.state === 'waiting' ? t('not downloaded yet') : '';
  return h('div', { class: 'file' + (f.state === 'expired' ? ' expired' : ''), 'data-file': f.name },
    f.thumb ? h('img', { class: 'thumb', src: f.thumb, alt: t('Preview of {name}', { name: f.name }) }) : h('span', { class: 'file-ic' }, isImage ? icon('image') : icon('file-text')),
    h('div', null, h('div', { class: 'fname', text: f.name }),
      h('div', { class: 'small muted', text: `${fmtBytes(f.size)} · ${f.mime}${status ? ' · ' + status : ''}` })),
    f.state === 'expired' ? null : h('button', { class: 'btn small', text: t('Save…'), 'aria-label': t('Save {name}', { name: f.name }), onclick: () => saveDmFile(m, f) }));
}

async function saveDmFile(m, f) {
  let dir = '';
  try { dir = await api('downloads_dir'); } catch (e) { dir = ''; }
  const path = h('input', { type: 'text', value: dir ? `${dir}/${f.name}` : f.name, 'aria-label': t('Save to') });
  modal({
    title: t('Save {name}', { name: f.name }),
    body: h('div', { class: 'stack' }, field(t('Save to'), path, t('A full path, or a folder.')),
      h('button', { class: 'btn small', text: t('Browse…'), onclick: async () => { const p = await pickSave('any', f.name); if (p) path.value = p; } })),
    actions: [
      { label: t('Cancel') },
      { label: t('Save'), kind: 'primary', run: async () => {
        const target = await pickSave('any', f.name, path.value);
        if (!target) return false;
        await api('dm_save_file', dmUI.current, m.id, f.hash, target);
        toast(t('Saved {name}.', { name: f.name }));
      } },
    ],
  });
}

function attachDialog(c) {
  const chosen = [];
  const list = h('div', { class: 'stack', 'aria-live': 'polite' });
  const draw = () => {
    clear(list);
    if (!chosen.length) list.appendChild(h('div', { class: 'small muted', text: t('No files chosen yet.') }));
    chosen.forEach((p, i) => list.appendChild(h('div', { class: 'row' }, h('span', { class: 'spacer mono', text: p }),
      h('button', { class: 'btn link small', text: t('Remove'), 'aria-label': t('Remove {name}', { name: p }), onclick: () => { chosen.splice(i, 1); draw(); } }))));
  };
  const add = (p) => { p = p.trim(); if (p && !chosen.includes(p)) chosen.push(p); draw(); };
  const pathIn = h('input', { type: 'text', 'aria-label': t('File path'), placeholder: t('/path/to/file') });
  pathIn.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); add(pathIn.value); pathIn.value = ''; } });
  draw();
  modal({
    title: t('Attach files'),
    body: h('div', { class: 'stack' }, list,
      h('div', { class: 'row' }, h('button', { class: 'btn small', text: t('Browse…'), onclick: async () => { for (const p of await pickFiles()) add(p); } })),
      field(t('Or type a path'), h('div', { class: 'row' }, pathIn, h('button', { class: 'btn small', text: t('Add'), onclick: () => { add(pathIn.value); pathIn.value = ''; } })),
        t('Up to 10 files, 25 MB each. They are encrypted on this device before they leave it.'))),
    actions: [
      { label: t('Cancel') },
      { label: t('Send'), kind: 'primary', run: async () => {
        if (!chosen.length) { toast(t('Choose at least one file.'), { error: true }); return false; }
        const input = $('#dm-input');
        const caption = input ? input.value : '';
        const files = await confirmOpenAll(chosen);
        if (!files.length) return false;
        await api('dm_send', c.id, caption, dmUI.replyTo ? dmUI.replyTo.id : '', files);
        if (input) input.value = '';
        delete dmUI.drafts[c.id];
        dmUI.replyTo = null;
        await dmUI.reload();
        return true;
      } },
    ],
  });
}

// ---------------------------------------------------------------- details, members, owner controls

function dmInfoDialog() {
  const info = dmUI.info;
  if (!info) return;
  const body = h('div', { class: 'dm-info' });
  if (!info.direct) {
    body.appendChild(h('div', { class: 'row' },
      info.icon ? h('img', { class: 'group-icon', src: info.icon, alt: t('Group icon') }) : null,
      h('b', { class: 'spacer', text: info.name || t('Unnamed group') }),
      info.owner ? h('button', { class: 'btn small', text: t('Set icon…'), onclick: () => { m.close(); groupIconDialog(info); } }) : null,
      info.owner && info.icon ? h('button', { class: 'btn small', text: t('Remove icon'), onclick: async () => { await dmCall('dm_set_icon', info.id, ''); m.close(); dmInfoDialog(); } }) : null,
      info.owner ? h('button', { class: 'btn small', text: t('Rename'), onclick: async () => {
        const name = await promptModal(t('Rename group'), t('Name'), info.name);
        if (name !== null) { await dmCall('dm_rename', info.id, name); m.close(); dmInfoDialog(); }
      } }) : null));
  }
  body.appendChild(h('h3', { text: info.direct ? t('People') : t('Members ({n} of {max})', { n: info.members.length, max: info.max_members }) }));
  for (const p of [...info.members, ...info.invited]) {
    const invited = info.invited.includes(p);
    // Members' statuses (status.js), in words for screen readers too.
    const status = invited ? null : personStatus(p.id, p.me);
    body.appendChild(h('div', { class: 'member-row', oncontextmenu: (e) => memberOptions(e, p, invited) },
      status ? withStatus(avatarEl(p.name, p.id, 'sm'), status) : avatarEl(p.name, p.id, 'sm'),
      h('span', { class: 'spacer' }, h('span', { text: p.me ? t('{name} (you)', { name: p.name }) : p.name }),
        status ? statusTextEl(status) : null, status ? h('span', { class: 'sr-only', text: statusSentence(status) }) : null),
      p.owner && !info.direct ? h('span', { class: 'pill', text: t('owner') }) : null,
      invited ? h('span', { class: 'pill warn', text: t('invited') }) : null,
      p.verified ? h('span', { class: 'pill ok', text: t('verified') }) : null,
      p.key_changed ? h('span', { class: 'pill warn', text: t('key changed') }) : null,
      p.blocked ? h('span', { class: 'pill', text: t('blocked') }) : null,
      p.me ? null : h('button', { class: 'icon-btn', title: t('Options for {name}', { name: p.name }), 'aria-label': t('Options for {name}', { name: p.name }), icon: 'ellipsis', onclick: (e) => memberOptions(e, p, invited) })));
  }
  if (info.can_add) body.appendChild(h('button', { class: 'btn small', text: t('Add people'), onclick: () => { m.close(); addPeopleDialog(info); } }));
  if (info.owner && !info.direct) {
    body.appendChild(h('h3', { text: t('Settings') }));
    const canAdd = checkbox(t('Members can add people'), info.members_can_add, t('Otherwise only you can invite.'));
    canAdd.input.addEventListener('change', () => dmCall('dm_set_options', info.id, { members_can_add: canAdd.input.checked }));
    const files = checkbox(t('Allow attachments'), info.allow_attachments, t('When off, nobody can send files here.'));
    files.input.addEventListener('change', () => dmCall('dm_set_options', info.id, { allow_attachments: files.input.checked }));
    body.appendChild(canAdd);
    body.appendChild(files);
  }
  if (info.pins.length) {
    body.appendChild(h('h3', { text: t('Pinned messages') }));
    for (const p of info.pins) {
      body.appendChild(h('button', { class: 'pin-row', onclick: () => { m.close(); jumpTo(p.id); } },
        h('b', { text: p.mine ? t('You') : p.name }), ': ', (p.text || '').slice(0, 120)));
    }
  }
  const m = modal({
    title: info.direct ? t('Conversation') : t('Group details'),
    body,
    actions: [
      { label: info.direct ? t('Delete conversation') : t('Leave group'), kind: 'danger', run: async () => {
        if (!(await confirmModal(info.direct ? t('Delete conversation') : t('Leave group'),
          info.direct ? t('Delete this conversation and its history from your devices?') : t('Leave this group? You will stop receiving its messages, and its history is deleted from your devices.'),
          info.direct ? t('Delete') : t('Leave')))) return false;
        await api('dm_leave', info.id);
        dmUI.current = null;
        dmUI.refresh();
        return true;
      } },
      { label: t('Close'), kind: 'primary' },
    ],
  });
}

function groupIconDialog(info) {
  const path = h('input', { type: 'text', 'aria-label': t('Image path'), placeholder: t('/path/to/image.png') });
  modal({
    title: t('Group icon'),
    body: h('div', { class: 'stack' }, field(t('Image'), path, t('It is shrunk on this device and shared inside the encrypted group.')),
      h('button', { class: 'btn small', text: t('Browse…'), onclick: async () => { const p = await pickFile('image'); if (p) path.value = p; } })),
    actions: [
      { label: t('Cancel') },
      { label: t('Set icon'), kind: 'primary', run: async () => {
        const p = await pickFile('image', path.value); if (!p) return false;
        await api('dm_set_icon', info.id, p); await dmUI.reload();
      } },
    ],
  });
}

// A person in the conversation's details (their row, or its Options
// button): their menu (people.js), with the group owner's entries.
function memberOptions(e, p, invited) {
  return userMenu(e, { person: p }, { dm: dmUI.info, invited });
}

// People to start a group with or add to one: from my contacts (no
// community needed: contact codes), or from a community's members.
async function pickMembers(title, max, okLabel, withName) {
  const comms = st.communities.filter((c) => c.status === 'active');
  return new Promise((resolve) => {
    let done = false;
    const sel = selectEl([['', t('My contacts')], ...comms.map((c) => [c.id, c.name])], '');
    const name = h('input', { type: 'text', placeholder: t('Optional') });
    const list = h('div', { class: 'pick-list', role: 'group', 'aria-label': t('People') });
    const load = async () => {
      clear(list);
      if (!sel.value) {
        let contacts = [];
        try { contacts = await api('dm_contacts'); } catch (e) { toastError(e); }
        for (const c of contacts.filter((x) => !x.blocked)) {
          const cb = checkbox(c.name, false);
          cb.input.value = c.id;
          list.appendChild(cb);
        }
        if (!list.children.length) list.appendChild(h('div', { class: 'small muted', text: t('No contacts yet. Add someone with their contact code first.') }));
        return;
      }
      list.appendChild(h('div', { class: 'small muted', text: t('Loading members…') }));
      let members = [];
      try { members = await api('members', sel.value); } catch (e) { toastError(e); }
      // Who I am there (not loaded yet when the app opened in this view).
      if (!st.states[sel.value]) await loadState(sel.value);
      const mine = (st.states[sel.value] && st.states[sel.value].self) ? st.states[sel.value].self.key : '';
      clear(list);
      for (const mm of members.filter((x) => x.key !== mine)) {
        const cb = checkbox(mm.display_name, false);
        cb.input.value = mm.key;
        list.appendChild(cb);
      }
      if (!list.children.length) list.appendChild(h('div', { class: 'small muted', text: t('Nobody else here yet.') }));
    };
    sel.addEventListener('change', load);
    load();
    modal({
      title,
      body: h('div', { class: 'stack' }, field(t('People from'), sel), withName ? field(t('Group name'), name) : null,
        h('div', { class: 'hint', text: t('Up to {max} people. From a community, they need a contact card there. Their privacy settings decide whether they can be invited.', { max }) }), list),
      actions: [
        { label: t('Cancel'), run: () => { done = true; resolve(null); } },
        { label: okLabel, kind: 'primary', run: () => {
          const keys = $$('input[type=checkbox]', list).filter((x) => x.checked).map((x) => x.value);
          if (!keys.length) { toast(t('Pick at least one person.'), { error: true }); return false; }
          if (keys.length > max) { toast(t('You can pick at most {max} people.', { max }), { error: true }); return false; }
          done = true;
          resolve({ cid: sel.value, keys, name: name.value });
          return true;
        } },
      ],
      onclose: () => { if (!done) resolve(null); },
    });
  });
}

async function newGroupDialog() {
  const pick = await pickMembers(t('New group DM'), 9, t('Create'), true);
  if (!pick) return;
  try {
    const r = await api('dm_create_group', pick.cid, pick.keys, pick.name);
    toast(t('Group created. People join once they accept the invitation.'));
    await dmUI.open(r.id);
  } catch (e) { toastError(e); }
}

async function addPeopleDialog(info) {
  const room = info.max_members - info.members.length - info.invited.length;
  const pick = await pickMembers(t('Add people'), room, t('Invite'), false);
  if (!pick) return;
  try { await api('dm_invite', info.id, pick.cid, pick.keys); toast(t('Invitations sent.')); dmUI.reload(); }
  catch (e) { toastError(e); }
}

// ---------------------------------------------------------------- contacts, verification, blocking

async function safetyDialog(id, name, gid = '') {
  let s;
  try { s = await api('dm_safety', id, gid); } catch (e) { toastError(e); return; }
  modal({
    title: t('Safety number with {name}', { name }),
    body: h('div', null,
      h('p', { text: t('Compare this number with {name} in person or on a call you trust. If it matches on both screens, nobody is in the middle.', { name }) }),
      h('div', { class: 'safety-number mono', 'aria-label': t('Safety number'), text: s.number }),
      s.key_changed ? h('p', { class: 'warn', text: t('Their key changed since you last checked. Accept the new key first, then compare.') }) : null,
      h('p', { class: 'small muted', text: s.verified ? t('You marked this contact as verified.') : t('Not verified yet.') })),
    actions: [
      s.key_changed ? { label: t('Accept new key'), run: () => api('dm_accept_key_change', id).then(() => { dmUI.reload(); }) } : null,
      s.key_changed ? null : { label: s.verified ? t('Mark as not verified') : t('Mark as verified'), kind: 'primary', run: () => api('dm_set_verified', id, !s.verified).then(() => dmUI.reload()) },
      { label: t('Close') },
    ].filter(Boolean),
  });
}

// A contact code lets someone message you without sharing a community: the
// report form on the website asks for one, so the maintainers can reply.
function myContactCodeDialog() {
  const out = h('textarea', { rows: 4, readonly: true, class: 'contact-code' });
  const err = h('div', { class: 'error', role: 'alert' });
  const make = async () => {
    err.textContent = '';
    try { out.value = await api('dm_contact_code'); } catch (e) { err.textContent = e.message; }
  };
  const shown = (st.status && st.status.display_name) || '';
  const body = h('div', {},
    h('p', { text: t('Give this code to someone so they can send you a direct message, without sharing a community. It works for about a week; make a new one when you need it again.') }),
    h('p', { class: 'small muted', text: shown ? t('They see you as “{name}” (Settings → Identity → Your name).', { name: shown }) : t('It carries no name. Set one under Settings → Identity → Your name.') }),
    h('div', { class: 'row' }, h('button', { class: 'btn', text: t('Make a new code'), onclick: make })),
    field(t('Your contact code'), out), err);
  make();
  modal({ title: t('Your contact code'), body, actions: [
    { label: t('Copy'), kind: 'primary', run: async () => { if (!out.value) return false; await api('copy_text', out.value); toast(t('Contact code copied.')); } },
    { label: t('Close') },
  ] });
}

function addContactCodeDialog() {
  const code = h('textarea', { rows: 4, placeholder: 'openarcanum-contact:…' }); // i18n: not shown (the code's literal prefix)
  const body = h('div', {},
    h('p', { text: t('Paste the contact code someone gave you. They are added to your contacts, and you can message them.') }),
    field(t('Contact code'), code));
  modal({ title: t('Add a contact code'), body, actions: [
    { label: t('Add and message'), kind: 'primary', run: async () => {
      const r = await api('dm_add_contact_code', code.value);
      toast(t('Added to your contacts.'));
      dmUI.startWithContact(r.id);
    } },
    { label: t('Cancel') },
  ] });
}

// Contacts, the main panel of Direct messages: everyone I can message
// without sharing a community, and how to add more (contact codes).
async function renderDmHome(main) {
  const page = h('div', { class: 'dm-home-page' });
  main.appendChild(page);
  page.appendChild(h('div', { class: 'main-head' }, h('span', { class: 'title', text: t('Contacts') }), h('div', { class: 'spacer' }),
    h('span', { class: 'small muted', text: t('End-to-end encrypted') })));
  const body = h('div', { class: 'dm-home-body' });
  page.appendChild(body);
  body.appendChild(h('div', { class: 'row dm-home-actions' },
    h('button', { class: 'btn primary', text: t('Add a contact code'), onclick: () => addContactCodeDialog() }),
    h('button', { class: 'btn', text: t('Your contact code'), onclick: () => myContactCodeDialog() }),
    h('button', { class: 'btn', text: t('New group DM'), onclick: () => newGroupDialog() })));
  body.appendChild(h('p', { class: 'small muted', text: t("To message someone, add their contact code; to be messaged, give them yours. You don't need to share a community. Direct messages are end-to-end encrypted: only the people in the conversation can read them.") }));
  let contacts = [];
  try { contacts = await api('dm_contacts'); } catch (e) { toastError(e); return; }
  if (!page.isConnected) return;
  const list = h('div', { class: 'dm-info' });
  if (!contacts.length) list.appendChild(emptyNote(t('No contacts yet. Add someone with their contact code, or add people from a community\'s member list.')));
  const again = () => { if (st.view === 'dms' && !dmUI.current) render(); };
  for (const c of contacts) {
    list.appendChild(h('div', { class: 'member-row', oncontextmenu: (e) => userMenu(e, { contact: c }, { after: again }) },
      withStatus(avatarEl(c.name, c.id, 'sm'), personStatus(c.id)),
      h('span', { class: 'spacer' }, h('span', { text: c.name }), statusTextEl(personStatus(c.id)),
        h('span', { class: 'sr-only', text: statusSentence(personStatus(c.id)) })),
      c.verified ? h('span', { class: 'pill ok', text: t('verified') }) : null,
      c.key_changed ? h('span', { class: 'pill warn', text: t('key changed') }) : null,
      c.blocked ? h('span', { class: 'pill', text: t('blocked') }) : null,
      c.blocked ? null : h('button', { class: 'btn small primary', text: t('Message'), onclick: () => dmUI.startWithContact(c.id) }),
      h('button', { class: 'btn small', text: t('Safety number'), onclick: () => safetyDialog(c.id, c.name) }),
      h('button', { class: 'btn small', text: c.blocked ? t('Unblock') : t('Block'), onclick: () => api('dm_block', c.id, !c.blocked).then(again, toastError) }),
      h('button', { class: 'btn small danger', text: t('Remove'), onclick: () => api('dm_remove_contact', c.id).then(again, toastError) })));
  }
  body.appendChild(list);
}

// ---------------------------------------------------------------- events

on('dm', () => { dmUI.refresh(); if (dmUI.current) dmUI.reload(); });
on('dm_contacts', () => { if (dmUI.current) dmUI.reload(); else if (st.view === 'dms') render(); });
on('dm_message', async ({ conv }) => {
  if (st.view === 'dms' && dmUI.current === conv) {
    await dmUI.reload();
    api('dm_mark_read', conv).catch(() => {});
  }
  dmUI.refresh();
});
on('dm_update', async ({ conv }) => { if (st.view === 'dms' && dmUI.current === conv) await dmUI.reload(); });
on('dm_typing', ({ id, typing }) => {
  if (dmUI.current !== id) return;
  const el = $('#dm-typing');
  if (el) el.textContent = typingText(typing);
});
