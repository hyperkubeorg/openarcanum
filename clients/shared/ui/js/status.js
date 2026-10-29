// Statuses (openarcanum/statuses.py; docs/user-guide.md "Your status"): the
// status picker from the user panel, the dot and custom text wherever a
// person is shown, and Settings → Privacy & DMs → Status (who sees it).
// Python decrypts everything; the page gets only what it shows, never a key.
'use strict';

const statusUI = {
  view: null,
};

const STATUS_STATES = ['online', 'idle', 'dnd', 'invisible'];
const STATUS_EMOJI = ['💬', '🎧', '📅', '🏠', '🚗', '🍽️', '🤒', '🌴', '🎮', '💤'];
const STATUS_TEXT_MAX = 128;

function statusLabel(state) {
  return { online: t('Online'), idle: t('Idle'), dnd: t('Do not disturb'), invisible: t('Invisible'), offline: t('Offline') }[state] || t('Offline');
}

// What a received status means now: past its expiry it's plain online with
// no text; a contact's that wasn't refreshed in time is offline (contacts
// have no server to say they're connected).
function liveStatus(r, online = true) {
  const plain = (state) => ({ state, text: '', emoji: '' });
  if (!online) return plain('offline');
  if (!r) return plain('online');
  const now = Date.now();
  if (r.source === 'contacts' && r.fresh_until && now > r.fresh_until) return plain('offline');
  if (r.state === 'offline') return plain('offline');
  // When they were last active: only from a status they shared (not from
  // just being connected), and never for offline or invisible.
  const activity = r.shared && r.at ? { idleSince: r.idle_since || 0 } : null;
  if (r.expires_at && now > r.expires_at) return { ...plain('online'), activity };
  return { state: r.state || 'online', text: r.text || '', emoji: r.emoji || '', activity };
}

function myStatus() {
  const m = statusUI.view && statusUI.view.mine;
  if (!m) return { state: 'online', text: '', emoji: '' };
  const expired = m.expires_at && Date.now() > m.expires_at;
  return { state: m.effective || m.state, text: expired ? '' : m.text, emoji: expired ? '' : m.emoji };
}

// A community member (an entry of the member list, with its presence).
function memberStatus(cid, m) {
  const s = st.states[cid];
  if (s && s.self && s.self.key === m.key) return myStatus();
  const r = statusUI.view && (statusUI.view.communities[cid] || {})[m.key];
  return liveStatus(r, !!m.online);
}

// A person known by DM identity (a contact or someone in a conversation).
function personStatus(ident, me = false) {
  if (me) return myStatus();
  const r = statusUI.view && statusUI.view.people[ident];
  return r ? liveStatus(r, true) : { state: 'offline', text: '', emoji: '' };
}

function statusLine(s) { return [s.emoji, s.text].filter(Boolean).join(' '); }

// The text alternative of a dot: "Do not disturb: In a meeting".
// "Idle - Last active 12 minutes ago", "Online - Active now"; just the state
// for offline, and when the person shared no activity.
function statusActivity(s) {
  const label = statusLabel(s.state);
  const a = s.activity;
  if (!a || s.state === 'offline' || s.state === 'invisible') return label;
  if (a.idleSince) return t('{status} - Last active {ago}', { status: label, ago: agoText(a.idleSince) });
  if (s.state === 'idle') return label; // set by hand, not from inactivity
  return t('{status} - Active now', { status: label });
}

function statusSentence(s) {
  const line = statusLine(s);
  const status = statusActivity(s);
  return line ? t('{status}: {text}', { status, text: line }) : status;
}

// The dot on an avatar (colour and shape only; the row it's in carries the
// words through statusDesc).
function statusDot(s) {
  return h('span', { class: 'presence ' + s.state, 'aria-hidden': 'true' });
}

// An avatar with its status dot.
function withStatus(av, s) { return h('span', { class: 'av-wrap' }, av, statusDot(s)); }

// The status in words for screen readers: a hidden element the row points
// at with aria-describedby (rows are named by the person's name).
let statusDescSeq = 0;
function statusDesc(s) {
  return h('span', { class: 'sr-only', id: `status-desc-${++statusDescSeq}`, text: statusSentence(s) });
}

// The custom text under a name (nothing when there is none).
function statusTextEl(s) {
  const line = statusLine(s);
  return line ? h('span', { class: 'status-text', text: line, title: line }) : null;
}

// A profile card's status: the dot with its name, and the custom text.
function profileStatus(s) {
  const line = statusLine(s);
  return [
    h('div', { class: 'small status-line' }, h('span', { class: 'presence inline ' + s.state, 'aria-hidden': 'true' }), h('span', { text: statusLabel(s.state) })),
    line ? h('div', { class: 'small status-custom', text: line }) : null,
  ];
}

// For a person's menu (people.js): their status as a note above the entries.
function statusNote(s) {
  if (!s || (s.state === 'offline' && !s.text)) return null;
  return { note: statusSentence(s), dot: s.state };
}

async function loadStatuses() {
  try { statusUI.view = await api('statuses'); } catch (e) { return; }
  statusesChanged();
}

let statusRepaint = null;
function statusesChanged() {
  // Redraw what shows statuses (once per burst).
  if (statusRepaint) return;
  statusRepaint = setTimeout(() => {
    statusRepaint = null;
    if (st.view === 'community') keepingFocus($('#members'), () => renderMembers());
    if (st.view === 'dms') keepingFocus($('#sidebar'), () => renderSidebar());
    paintContactStatuses();
    paintUserPanels();
  }, 50);
}

// The user panel's own status, in place (the sidebar isn't redrawn for it).
function paintUserPanels() {
  for (const panel of $$('.user-panel')) {
    const s = myStatus();
    const dot = $('.me-btn .presence', panel);
    if (dot) dot.className = 'presence ' + s.state;
    const line = $('.me-status', panel);
    if (line) line.textContent = statusLine(s) || panel.dataset.connection || statusLabel(s.state);
    const btn = $('.me-btn', panel);
    if (btn) btn.setAttribute('aria-label', t('Your status: {status}. Change it', { status: statusSentence(s) }));
  }
}

on('statuses', (v) => { statusUI.view = v; statusesChanged(); });
on('profile', (s) => { if (s && s.state === 'unlocked') loadStatuses(); });
// Expiries and contacts going stale happen without an event.
setInterval(() => { if (statusUI.view) statusesChanged(); }, 60000);

// ---------------------------------------------------------------- the picker

// Who sees what, in a sentence (the picker's "Who can see this").
function audienceSummary(a) {
  if (!a) return '';
  const levels = Object.values(a.communities || {});
  const nState = levels.filter((l) => l !== 'off').length;
  const nText = levels.filter((l) => l === 'full').length;
  const contacts = a.contacts === 'off' ? null : a.contacts_scope === 'chosen' ? tn('{n} chosen contact', '{n} chosen contacts', a.contacts_chosen) : t('your contacts');
  const stateWho = [contacts, nState ? tn('members of {n} community', 'members of {n} communities', nState) : null].filter(Boolean);
  const textWho = [a.contacts === 'full' ? contacts : null, nText ? tn('members of {n} community', 'members of {n} communities', nText) : null].filter(Boolean);
  const join = (list) => list.join(t(' and '));
  return [
    stateWho.length ? t('Your status: {who}.', { who: join(stateWho) }) : t('Nobody sees your status.'),
    textWho.length ? t('Custom text: {who}.', { who: join(textWho) }) : t('Nobody sees your custom text.'),
  ].join(' ');
}

function statusPicker() {
  const mine = (statusUI.view && statusUI.view.mine) || { state: 'online', text: '', emoji: '', clear_after: 'never' };
  let state = mine.state;
  let emoji = mine.emoji || '';
  const desc = {
    online: t('Shown to the people who can see your status.'),
    idle: t('Set automatically after a while without input; you can also pick it.'),
    dnd: t('No desktop notifications. Unread marks and mention counts still count.'),
    invisible: t("You appear offline to everyone. Your community servers still see that you're connected (they deliver your messages), but they don't tell anyone."),
  };
  const choices = h('div', { class: 'status-choices', role: 'radiogroup', 'aria-label': t('Status') });
  const paint = () => {
    for (const b of $$('button', choices)) {
      const on = b.dataset.state === state;
      b.setAttribute('aria-checked', String(on));
      b.tabIndex = on ? 0 : -1;
    }
  };
  for (const s of STATUS_STATES) {
    choices.appendChild(h('button', { type: 'button', class: 'status-choice', role: 'radio', 'data-state': s, 'aria-checked': String(s === state),
      'aria-describedby': `status-choice-desc-${s}`,
      onclick: () => { state = s; paint(); },
      onkeydown: (e) => {
        if (!['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight'].includes(e.key)) return;
        e.preventDefault();
        const i = STATUS_STATES.indexOf(state) + (e.key === 'ArrowDown' || e.key === 'ArrowRight' ? 1 : STATUS_STATES.length - 1);
        state = STATUS_STATES[i % STATUS_STATES.length];
        paint();
        $(`[data-state="${state}"]`, choices).focus();
      } },
    h('span', { class: 'presence ' + s, 'aria-hidden': 'true' }),
    h('span', { class: 'status-choice-text' }, h('b', { text: statusLabel(s) }), h('span', { class: 'desc', id: `status-choice-desc-${s}`, text: desc[s] }))));
  }
  paint();
  const text = h('input', { type: 'text', maxlength: String(STATUS_TEXT_MAX), value: mine.text || '', placeholder: t("What's happening?"), 'aria-label': t('Custom text') });
  const emojiRow = h('div', { class: 'status-emoji', role: 'radiogroup', 'aria-label': t('Emoji') });
  const paintEmoji = () => { for (const b of $$('button', emojiRow)) b.setAttribute('aria-checked', String(b.dataset.emoji === emoji)); };
  for (const e of ['', ...STATUS_EMOJI]) {
    emojiRow.appendChild(h('button', { type: 'button', class: 'emoji-choice', role: 'radio', 'data-emoji': e, 'aria-label': e || t('No emoji'), 'aria-checked': String(e === emoji),
      text: e || '-', onclick: () => { emoji = e; paintEmoji(); } }));
  }
  const clear = selectEl([['never', t("Don't clear")], ['30m', t('30 minutes')], ['1h', t('1 hour')], ['4h', t('4 hours')], ['today', t('Today')]], mine.clear_after || 'never');
  clear.setAttribute('aria-label', t('Clear after'));
  const err = h('div', { class: 'error' });
  const who = h('div', { class: 'small muted status-who', text: audienceSummary(statusUI.view && statusUI.view.audience) });
  const body = h('div', { class: 'stack status-picker' },
    choices,
    field(t('Custom text'), text, t('Up to {n} characters.', { n: STATUS_TEXT_MAX })),
    h('div', null, h('div', { class: 'group-label', text: t('Emoji') }), emojiRow),
    field(t('Clear after'), clear),
    h('div', { class: 'status-who-box' },
      h('div', { class: 'group-label', text: t('Who can see this') }), who,
      h('button', { class: 'btn link', type: 'button', text: t('Change who can see this'), onclick: () => { m.close(); openSettings('privacy'); } })),
    err);
  const save = async (cleared) => {
    err.textContent = '';
    try {
      statusUI.view = await api('set_status', state, cleared ? '' : text.value, cleared ? '' : emoji, cleared ? 'never' : clear.value);
      statusesChanged();
    } catch (e) { err.textContent = e.message; return false; }
  };
  const m = modal({ title: t('Set your status'), body, actions: [
    mine.text || mine.emoji ? { label: t('Clear custom text'), run: () => save(true) } : null,
    { label: t('Cancel') },
    { label: t('Save'), kind: 'primary', run: () => save(false) },
  ].filter(Boolean) });
}

// ---------------------------------------------------------------- Settings → Privacy & DMs → Status

const STATUS_LEVELS = () => [['off', t('Off')], ['state', t('Online status only')], ['full', t('Status and custom text')]];

async function statusPrivacySection() {
  const v = settingsUI.data.vault;
  const contacts = selectEl(STATUS_LEVELS(), v.status_contacts);
  const scope = selectEl([['all', t('All contacts')], ['chosen', t('Only the contacts I choose')]], v.status_contacts_scope);
  const chosenNote = h('span', { class: 'small muted', text: tn('{n} chosen', '{n} chosen', (v.status_contacts_chosen || []).length) });
  const choose = h('button', { class: 'btn small', type: 'button', text: t('Choose…'), onclick: () => chooseStatusContacts(chosenNote) });
  const idle = selectEl([[5, t('5 minutes')], [10, t('10 minutes')], [15, t('15 minutes')], [30, t('30 minutes')], [60, t('1 hour')], [0, t('Never')]], String(v.status_idle_minutes));
  if (![...idle.options].some((o) => o.value === String(v.status_idle_minutes))) idle.appendChild(h('option', { value: v.status_idle_minutes, text: t('{n} minutes', { n: v.status_idle_minutes }) }));
  idle.value = String(v.status_idle_minutes);
  const list = (await api('list_communities')).filter((c) => c.status === 'active');
  const levels = v.status_communities || {};
  const rows = list.map((c) => {
    const sel = selectEl(STATUS_LEVELS(), levels[c.id] || 'state');
    sel.setAttribute('data-community', c.id);
    return settingRow(c.name, t('Members of this community'), sel, async () => {
      const r = await api('set_status_community', c.id, sel.value);
      settingsUI.data.vault.status_communities = r.value;
    });
  });
  return h('div', { class: 'status-privacy' },
    formSection(t('Status'), [
      h('p', { class: 'small muted', text: t('Your status (online, idle, do not disturb) and custom text are end-to-end encrypted: only the people you share them with can read them, not any server. A community server still sees when you are connected and when your status changes, never what it says.') }),
      vaultSetting('status_contacts', t('Your contacts see'), t('Contacts are people you added yourself. It reaches them the same way as a direct message.'), contacts),
      vaultSetting('status_contacts_scope', t('Share with'), null, scope),
      h('div', { class: 'setting-row' }, h('div', null, h('div', { text: t('Chosen contacts') }),
        h('div', { class: 'desc', text: t('Used with "Only the contacts I choose".') })), h('div', { class: 'row' }, chosenNote, choose)),
      vaultSetting('status_idle_minutes', t('Idle after'), t('Your status shows Idle after this long without input on this device.'), idle, (c) => Number(c.value))]),
    formSection(t('Status in each community'), [
      h('p', { class: 'small muted', text: t('By default members of a community see only whether you are online, idle or busy; your custom text is for your contacts. Invisible hides you from members, not from the community server.') }),
      ...(rows.length ? rows : [emptyNote(t('No communities yet.'))])]));
}

async function chooseStatusContacts(note) {
  let contacts = [];
  try { contacts = await api('dm_contacts'); } catch (e) { toastError(e); return; }
  const chosen = new Set(settingsUI.data.vault.status_contacts_chosen || []);
  const boxes = contacts.map((c) => { const cb = checkbox(c.name, chosen.has(c.id)); cb.contactId = c.id; return cb; });
  const err = h('div', { class: 'error' });
  modal({ title: t('Who sees your status'), body: h('div', { class: 'stack' },
    h('p', { class: 'small muted', text: t('With "Only the contacts I choose", only these contacts see your status. Anyone you take off the list stops seeing it.') }),
    ...(boxes.length ? boxes : [emptyNote(t("No contacts yet. Add people from a community's member list."))]), err),
  actions: [{ label: t('Cancel') }, { label: t('Save'), kind: 'primary', run: async () => {
    const ids = boxes.filter((b) => b.input.checked).map((b) => b.contactId);
    try {
      const r = await api('set_setting', 'status_contacts_chosen', ids);
      settingsUI.data.vault.status_contacts_chosen = r.value;
      note.textContent = tn('{n} chosen', '{n} chosen', ids.length);
    } catch (e) { err.textContent = e.message; return false; }
  } }] });
}
