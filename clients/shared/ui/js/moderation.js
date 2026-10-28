// Moderation and custom emoji in the UI (docs/protocol.md §4.7 to §4.10): ban with
// history deletion, timeouts, reports, bulk delete, member screening,
// AutoMod, the filtered audit log and the Emoji section. The server checks
// every permission again; hiding is only for clarity.
'use strict';

const BAN_WINDOWS = [[0, N_("Don't delete any")], [3600, N_('Last hour')], [21600, N_('Last 6 hours')], [86400, N_('Last 24 hours')], [259200, N_('Last 3 days')], [604800, N_('Last 7 days')]];
// A ban's network block on the community's dedicated relay (seconds; 0 = until unbanned).
const NETWORK_BLOCKS = [[86400, N_('1 day')], [604800, N_('7 days')], [2592000, N_('30 days')], [0, N_('Until they are unbanned')]];
const TIMEOUTS = [[60, N_('60 seconds')], [300, N_('5 minutes')], [600, N_('10 minutes')], [3600, N_('1 hour')], [86400, N_('1 day')], [604800, N_('1 week')], [2419200, N_('28 days')]];
const SCREENING = [[0, N_('Off')], [10, N_('10 minutes')], [30, N_('30 minutes')], [60, N_('1 hour')], [360, N_('6 hours')], [1440, N_('1 day')], [4320, N_('3 days')], [10080, N_('7 days')]];
const AUDIT_ACTIONS = [['', N_('Any action')], ['kick', N_('Kick')], ['ban', N_('Ban')], ['unban', N_('Unban')], ['network_block', N_('Network address blocked')], ['network_unblock', N_('Network block lifted')], ['timeout', N_('Timeout')], ['bulk_delete', N_('Bulk delete')],
  ['assign_roles', N_('Roles assigned')], ['update_settings', N_('Settings')], ['create_join_key', N_('Invite created')], ['revoke_join_key', N_('Invite revoked')],
  ['raid_revoke', N_('Raid revoke')], ['approve', N_('Approved')], ['deny', N_('Denied')], ['create_channel', N_('Channel created')], ['edit_channel', N_('Channel edited')],
  ['delete_channel', N_('Channel deleted')], ['create_role', N_('Role created')], ['edit_role', N_('Role edited')], ['delete_role', N_('Role deleted')],
  ['save_automod_rule', N_('AutoMod rule saved')], ['delete_automod_rule', N_('AutoMod rule deleted')], ['automod', N_('AutoMod action')],
  ['resolve_report', N_('Report closed')], ['add_emoji', N_('Emoji added')], ['rename_emoji', N_('Emoji renamed')], ['delete_emoji', N_('Emoji deleted')],
  ['set_nickname', N_('Nickname changed')], ['set_channel_override', N_('Permission override')], ['create_category', N_('Category created')],
  ['edit_category', N_('Category edited')], ['delete_category', N_('Category deleted')], ['voice_mute', N_('Server mute')], ['voice_deafen', N_('Server deafen')],
  ['voice_move', N_('Moved in voice')], ['delete_message', N_('Message deleted')], ['pin', N_('Message pinned')], ['unpin', N_('Message unpinned')]];

// A table's [value, N_(label)] pairs with the labels translated.
function tOptions(list) { return list.map(([v, label]) => [v, t(label)]); }

function fmtUntil(ms) {
  const d = new Date(ms);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay ? fmtTime(ms) : d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

// ---------------------------------------------------------------- member actions (member menu, Members section)

function banDialog(cid, m, after) {
  const reason = h('input', { type: 'text', 'aria-label': t('Reason'), placeholder: t('Kept in the audit log') });
  const del = selectEl(tOptions(BAN_WINDOWS), '0');
  del.setAttribute('aria-label', t('Delete message history'));
  // Only on a dedicated relay (docs/protocol.md §2): it blocks the address
  // itself; neither the server nor we ever see it.
  const summary = st.communities.find((x) => x.id === cid);
  const dedicated = !!(summary && summary.dedicated_relay);
  const block = dedicated ? checkbox(t('Also block their network address'), false,
    t('The address they connect from is blocked; nobody here sees it. Everyone sharing that address is blocked too (same home or office network, mobile carrier, VPN or Tor), and a VPN or new address gets around it.')) : null;
  const blockFor = selectEl(tOptions(NETWORK_BLOCKS), '604800');
  blockFor.setAttribute('aria-label', t('Block the address for'));
  const blockField = field(t('Block the address for'), blockFor);
  if (block) {
    block.classList.add('ban-block-network');
    blockField.style.display = 'none';
    block.input.addEventListener('change', () => { blockField.style.display = block.input.checked ? '' : 'none'; });
  }
  modal({ title: t('Ban {name}', { name: m.display_name }), body: h('div', { class: 'stack' }, field(t('Reason'), reason),
    field(t('Delete message history'), del, t('Their messages from this period are deleted for everyone, and from search.')),
    block, block ? blockField : null),
  actions: [{ label: t('Cancel') }, { label: t('Ban'), kind: 'danger', run: async () => {
    const params = { member: m.key, reason: reason.value, delete_message_seconds: Number(del.value) };
    if (block && block.input.checked) Object.assign(params, { block_network: true, block_seconds: Number(blockFor.value) });
    const r = await api('admin', cid, 'ban', params);
    const done = Number(del.value) ? t('{name} was banned. {count} message(s) deleted.', { name: m.display_name, count: r.deleted || 0 })
      : t('{name} was banned.', { name: m.display_name });
    toast([done, networkBlockText(r.network_block)].filter(Boolean).join(' '));
    if (after) after();
  } }] });
}

// What happened to a ban's network block, for the moderator.
function networkBlockText(status) {
  if (status === 'blocked') return t('Their network address is blocked.');
  if (status === 'pending') return t('Their address will be blocked when they next try to connect (they aren’t connected right now, or the server didn’t answer).');
  if (status === 'skipped') return t('Their address is the community server’s own network, so it wasn’t blocked.');
  return '';
}

// The ban list's note about a ban's network block.
function banBlockNote(b) {
  if (b.network_block === 'blocked') {
    return b.network_block_until ? t('Network address blocked until {when}', { when: fmtDateTime(b.network_block_until) })
      : t('Network address blocked until unbanned');
  }
  if (b.network_block === 'pending') return t('Network address block waiting for them to connect');
  return '';
}

function timeoutDialog(cid, m, after) {
  const dur = selectEl(tOptions(TIMEOUTS), '600');
  dur.setAttribute('aria-label', t('Duration'));
  const reason = h('input', { type: 'text', 'aria-label': t('Reason'), placeholder: t('Kept in the audit log') });
  modal({ title: t('Time out {name}', { name: m.display_name }), body: h('div', { class: 'stack' },
    h('p', { class: 'small', text: t('They can still read, but can’t post, react, speak, start typing or change their nickname until it ends.') }),
    field(t('Duration'), dur), field(t('Reason'), reason)),
  actions: [{ label: t('Cancel') }, { label: t('Time out'), kind: 'danger', run: async () => {
    await api('admin', cid, 'timeout', { member: m.key, seconds: Number(dur.value), reason: reason.value });
    toast(t('{name} is timed out.', { name: m.display_name }));
    if (after) after();
  } }] });
}

async function liftTimeout(cid, m, after) {
  try { await api('admin', cid, 'timeout', { member: m.key, seconds: 0 }); toast(t("{name}'s timeout was removed.", { name: m.display_name })); if (after) after(); }
  catch (e) { toastError(e); }
}

function timedOut(m) { return !!(m && m.timeout_until && m.timeout_until > Date.now()); }

// Kick: a reason for the audit log, then out (an invite brings them back).
async function kickDialog(cid, m, after) {
  const reason = await promptModal(t('Kick {name}', { name: m.display_name }), t('Reason (kept in the audit log)'), '', { ok: t('Kick') });
  if (reason === null) return;
  try { await api('admin', cid, 'kick', { member: m.key, reason }); toast(t('{name} was kicked.', { name: m.display_name })); if (after) after(); }
  catch (e) { toastError(e); }
}

// Someone's nickname in a community (Manage Nicknames).
async function nicknameDialog(cid, m, after) {
  const nick = await promptModal(t('Nickname for {name}', { name: m.display_name }), t('Nickname'), m.nickname || '', { hint: t('Up to 32 characters. Leave empty to clear it.') });
  if (nick === null) return;
  try { await api('set_nickname', cid, m.key, nick.trim()); toast(t('Nickname saved.')); if (after) after(); }
  catch (e) { toastError(e); }
}

// ---------------------------------------------------------------- message actions

function reportDialog(cid, m) {
  const reason = h('textarea', { rows: 3, 'aria-label': t('Reason'), maxlength: 500, placeholder: t('What’s wrong with it? (optional)') });
  modal({ title: t('Report message'), body: h('div', { class: 'stack' },
    h('div', { class: 'card small' }, h('b', { text: m.author_name }), ' ', (m.text || '').slice(0, 200)),
    field(t('Reason'), reason, t('Moderators see your name, the message and this reason.'))),
  actions: [{ label: t('Cancel') }, { label: t('Report'), kind: 'danger', run: async () => {
    await api('report_message', cid, m.id, reason.value);
    toast(t('Reported. Thanks, the moderators will take a look.'));
  } }] });
}

// Selection mode for bulk delete: messages get checkboxes; a bar counts and deletes them.
const selection = { on: false, ids: new Set() };

function startSelecting(first) {
  selection.on = true;
  selection.ids = new Set(first ? [first.id] : []);
  renderMain();
}

function stopSelecting() { selection.on = false; selection.ids.clear(); renderMain(); }

function selectionBar(c) {
  return h('div', { class: 'select-bar' },
    h('span', { class: 'spacer', text: t('{count} selected (up to 100)', { count: selection.ids.size }) }),
    h('button', { class: 'btn small danger', text: t('Delete selected'), disabled: !selection.ids.size, onclick: async () => {
      if (!(await confirmModal(t('Delete messages?'), t('{count} message(s) are deleted for everyone.', { count: selection.ids.size }), t('Delete')))) return;
      try {
        const r = await api('admin', st.cid, 'bulk_delete', { channel_id: c.id, message_ids: [...selection.ids] });
        toast(t('Deleted {count} message(s).', { count: r.deleted }));
        stopSelecting();
      } catch (e) { toastError(e); }
    } }),
    h('button', { class: 'btn small', text: t('Cancel'), onclick: stopSelecting }));
}

function selectBox(m) {
  const box = h('input', { type: 'checkbox', class: 'select-box', 'aria-label': t('Select message {id}', { id: m.id }), checked: selection.ids.has(m.id) });
  box.addEventListener('change', () => {
    if (box.checked && selection.ids.size >= 100) { box.checked = false; toast(t('A bulk delete removes at most 100 messages.'), { error: true }); return; }
    if (box.checked) selection.ids.add(m.id); else selection.ids.delete(m.id);
    const bar = $('.select-bar .spacer');
    if (bar) bar.textContent = t('{count} selected (up to 100)', { count: selection.ids.size });
    const del = $('.select-bar .danger');
    if (del) del.disabled = !selection.ids.size;
  });
  return box;
}

function deleteRecentDialog(c) {
  const n = h('input', { type: 'number', min: 1, max: 100, value: 10, 'aria-label': t('How many') });
  modal({ title: t('Delete recent messages in #{channel}', { channel: c.name }), body: h('div', { class: 'stack' }, field(t('How many of the newest messages'), n, t('1 to 100. One audit log entry.'))),
    actions: [{ label: t('Cancel') }, { label: t('Delete'), kind: 'danger', run: async () => {
      const r = await api('admin', st.cid, 'bulk_delete', { channel_id: c.id, last: Number(n.value) });
      toast(t('Deleted {count} message(s).', { count: r.deleted }));
    } }] });
}

// The composer's locked state for a timeout or member screening.
function composerLock(s) {
  const me = s && s.self;
  if (!me) return null;
  const now = Date.now();
  let text = null; let until = 0;
  if (me.timeout_until > now) { until = me.timeout_until; text = t("You're timed out. You can post again at {time}.", { time: fmtUntil(until) }); }
  else if (me.can_post_at > now) { until = me.can_post_at; text = t('New members can post from {time}.', { time: fmtUntil(until) }); }
  if (!text) return null;
  clearTimeout(chat.lockTimer);
  chat.lockTimer = setTimeout(() => { loadState(st.cid).then(renderMain); }, Math.min(until - now + 1000, 2 ** 31 - 1));
  return h('div', { class: 'composer-box composer-locked' }, h('div', { class: 'muted', text }));
}

// Jump to a reported message, in whichever community it is.
async function jumpToReported(cid, channelId, messageId) {
  if (st.cid !== cid) await selectCommunity(cid, channelId);
  await jumpToMessage(channelId, messageId);
}

// ---------------------------------------------------------------- custom emoji in messages

const emojiCache = {};

function customEmojiFor(cid) {
  const s = st.states[cid];
  return s && s.settings && s.settings.features.custom_emoji ? s.emoji || [] : [];
}

// Appends text to el, turning ":name:id" references to the community's
// emoji into images (fetched as data: URIs over the session).
function appendWithEmoji(el, text, cid = st.cid) {
  const list = customEmojiFor(cid);
  if (!list.length) { el.appendChild(document.createTextNode(text)); return; }
  const re = /:([a-z0-9_]{2,32}):([0-9]{1,18})/g;
  let last = 0; let m;
  while ((m = re.exec(text))) {
    const e = list.find((x) => x.id === Number(m[2]) && x.name === m[1]);
    if (!e) continue;
    if (m.index > last) el.appendChild(document.createTextNode(text.slice(last, m.index)));
    el.appendChild(emojiImg(cid, e));
    last = m.index + m[0].length;
  }
  if (last < text.length) el.appendChild(document.createTextNode(text.slice(last)));
}

function emojiImg(cid, e, cls = 'emoji') {
  const img = h('img', { class: cls, alt: `:${e.name}:`, title: `:${e.name}:` });
  const k = `${cid}:${e.id}:${e.version}`;
  if (emojiCache[k]) img.src = emojiCache[k];
  else api('emoji_image', cid, e.id).then((uri) => { if (uri) { emojiCache[k] = uri; img.src = uri; } }).catch(() => {});
  return img;
}

function emojiPicker(c, input) {
  const list = customEmojiFor(st.cid);
  if (!list.length || !c.can.use_custom_emoji) return null;
  return h('button', { class: 'icon-btn', title: t('Custom emoji'), icon: 'smile-plus', onclick: () => {
    const grid = h('div', { class: 'emoji-grid' });
    const ref = modal({ title: t('Custom emoji'), body: grid });
    for (const e of list) {
      grid.appendChild(h('button', { class: 'icon-btn emoji-pick', title: `:${e.name}:`, 'aria-label': `:${e.name}:`, onclick: () => {
        input.value += (input.value && !input.value.endsWith(' ') ? ' ' : '') + `:${e.name}:${e.id} `;
        input.dispatchEvent(new Event('input'));
        ref.close();
        input.focus();
      } }, emojiImg(st.cid, e)));
    }
  } });
}

// New reports and AutoMod alerts show as the moderator badge (the server's
// queue counts on the community's summary: rail icon, community menu,
// Community Admin, the Moderation section and Reports), not as a toast; the
// desktop notification follows Settings → Notifications (moderation.py).

// ---------------------------------------------------------------- community settings sections

async function adminReports(sec) {
  const cid = adminUI.cid;
  const closed = checkbox(t('Show resolved and dismissed'), !!adminUI.showClosed);
  closed.input.addEventListener('change', () => { adminUI.showClosed = closed.input.checked; adminReports(clear(sec)); });
  sec.appendChild(sectionIntro(t('Reports from members and AutoMod alerts. Members with Manage Messages see this queue.')));
  sec.appendChild(toolbar(closed));
  const list = await api('admin_query', cid, 'reports', { include_closed: !!adminUI.showClosed });
  if (!list.length) { sec.appendChild(emptyNote(t('Nothing to review.'))); return; }
  const chans = Object.fromEntries(adminUI.s.channels.map((c) => [c.id, c.name]));
  const rows = [];
  for (const r of list) {
    const who = r.kind === 'automod' ? t('AutoMod rule “{rule}” ({action})', { rule: r.rule_name, action: r.automod_action }) : t('Reported by {name}', { name: r.reporter_name });
    rows.push(h('div', { class: 'setting-row report-row', 'data-id': r.id },
      h('div', null,
        h('b', { text: t('{author} in #{channel}', { author: r.author_name, channel: chans[r.channel_id] || '?' }) }),
        r.status !== 'open' ? h('span', { class: 'pill', text: reportStatus(r.status) }) : null,
        h('div', { class: 'desc', text: `${who} · ${fmtDateTime(r.created_at)}` }),
        r.reason ? h('div', { class: 'desc report-reason', text: `“${r.reason}”` }) : null,
        r.resolved_by_name ? h('div', { class: 'desc', text: t('{status} by {name}', { status: reportStatus(r.status), name: r.resolved_by_name }) }) : null),
      h('div', { class: 'row wrap' },
        r.message_id ? h('button', { class: 'btn small', text: t('Jump to message'), onclick: () => { leavePage(false); jumpToReported(cid, r.channel_id, r.message_id); } }) : null,
        r.status === 'open' ? h('button', { class: 'btn small primary', text: t('Resolve'), onclick: () => closeReport(r, false, sec) }) : null,
        r.status === 'open' ? h('button', { class: 'btn small', text: t('Dismiss'), onclick: () => closeReport(r, true, sec) }) : null)));
  }
  sec.appendChild(formSection(null, rows));
}

const REPORT_STATUS = { open: N_('open'), resolved: N_('resolved'), dismissed: N_('dismissed') };
function reportStatus(status) { return REPORT_STATUS[status] ? t(REPORT_STATUS[status]) : status; }

async function closeReport(r, dismiss, sec) {
  try { await api('admin', adminUI.cid, 'resolve_report', { id: r.id, dismiss }); toast(dismiss ? t('Dismissed.') : t('Resolved.')); }
  catch (e) { toastError(e); }
  adminReports(clear(sec));
}

function adminScreening(sec) {
  const s = adminUI.s.settings;
  const sel = selectEl(tOptions(SCREENING), String(s.screening_minutes));
  if (![...sel.options].some((o) => o.value === String(s.screening_minutes))) sel.appendChild(h('option', { value: s.screening_minutes, text: t('{n} minutes', { n: s.screening_minutes }) }));
  sel.value = String(s.screening_minutes);
  sel.setAttribute('aria-label', t('Minimum membership age'));
  sec.appendChild(formSection(null,
    field(t('Minimum membership age'), sel, t('New members wait this long after joining before they can post, react or start direct messages. Members with a role skip it.')),
    saveButton(() => ({ screening_minutes: Number(sel.value) }))));
}

async function adminAutoMod(sec) {
  const rules = await api('admin_query', adminUI.cid, 'automod_rules');
  sec.appendChild(sectionIntro(t('The server checks every new or edited message against these rules. It looks at the text in memory only and never stores it; alerts point moderators to the message.')));
  sec.appendChild(toolbar(h('button', { class: 'btn primary', text: t('New rule…'), onclick: () => ruleDialog(null, () => adminAutoMod(clear(sec))) })));
  if (!rules.length) { sec.appendChild(emptyNote(t('No rules yet.'))); return; }
  const triggers = { keyword: t('Keywords'), mention_spam: t('Mention spam'), links: t('Links') };
  const rows = [];
  for (const r of rules) {
    const acts = [r.block && t('block'), r.alert && t('alert'), r.timeout_seconds && t('time out')].filter(Boolean).join(', ');
    rows.push(h('div', { class: 'setting-row rule-row' },
      h('div', null, h('b', { text: r.name }), h('span', { class: 'pill ' + (r.enabled ? 'ok' : ''), text: r.enabled ? t('on') : t('off') }),
        h('div', { class: 'desc', text: `${triggers[r.trigger]} → ${acts}` })),
      h('div', { class: 'row' },
        h('button', { class: 'btn small', text: t('Edit…'), onclick: () => ruleDialog(r, () => adminAutoMod(clear(sec))) }),
        h('button', { class: 'btn small danger', text: t('Delete'), onclick: async () => {
          if (await confirmModal(t('Delete rule?'), t('“{name}” stops applying.', { name: r.name }), t('Delete'))) {
            try { await api('admin', adminUI.cid, 'delete_automod_rule', { id: r.id }); } catch (e) { toastError(e); }
            adminAutoMod(clear(sec));
          }
        } }))));
  }
  sec.appendChild(formSection(null, rows));
}

function ruleDialog(r, after) {
  r = r || { name: '', enabled: true, trigger: 'keyword', keywords: [], patterns: [], mention_limit: 5, allowed_domains: [], block: true, alert: false, timeout_seconds: 0, exempt_role_ids: [], exempt_channel_ids: [] };
  const name = h('input', { type: 'text', value: r.name, 'aria-label': t('Rule name') });
  const trig = selectEl([['keyword', t('Keywords and patterns')], ['mention_spam', t('Mention spam')], ['links', t('Links')]], r.trigger);
  trig.setAttribute('aria-label', t('Looks for'));
  const kws = h('textarea', { rows: 3, value: r.keywords.join('\n'), 'aria-label': t('Keywords'), placeholder: t('One per line or comma-separated. word* matches the start of a word, *word the end.') });
  const pats = h('textarea', { rows: 2, value: r.patterns.join('\n'), 'aria-label': t('Regular expressions'), placeholder: t('One regular expression per line. Upper and lower case match alike.') });
  const limit = h('input', { type: 'number', min: 1, max: 50, value: r.mention_limit || 5, 'aria-label': t('Most mentions allowed') });
  const domains = h('textarea', { rows: 2, value: r.allowed_domains.join('\n'), 'aria-label': t('Allowed domains'), placeholder: t('example.org (subdomains included). Empty blocks every link.') });
  const block = checkbox(t('Block the message'), r.block, t('The sender sees why.'));
  const alert = checkbox(t('Alert moderators'), r.alert, t('An entry in the report queue and a notice to members with Manage Messages.'));
  const tsel = selectEl([[0, t("Don't time out")], ...tOptions(TIMEOUTS)], String(r.timeout_seconds || 0));
  tsel.setAttribute('aria-label', t('Time out the sender'));
  const enabled = checkbox(t('Rule is on'), r.enabled);
  const roleBoxes = adminUI.s.roles.filter((x) => !x.builtin).map((x) => { const c = checkbox(x.name, r.exempt_role_ids.includes(x.id)); c.roleId = x.id; return c; });
  // Voice channels' chat is checked by AutoMod too.
  const chanBoxes = adminUI.s.channels.map((x) => { const c = checkbox(x.name, r.exempt_channel_ids.includes(x.id)); c.querySelector('div').prepend(x.kind === 'voice' ? icon('volume-2', 'inline') : icon('hash', 'inline')); c.chanId = x.id; return c; });
  const parts = { keyword: [field(t('Keywords'), kws), field(t('Regular expressions'), pats, t('Up to 10, 260 characters each.'))],
    mention_spam: [field(t('Most distinct @mentions in one message'), limit)], links: [field(t('Allowed domains'), domains)] };
  const trigBox = h('div', { class: 'stack' });
  const paint = () => { clear(trigBox); append(trigBox, parts[trig.value]); };
  trig.addEventListener('change', paint);
  paint();
  modal({ title: r.id ? t('Edit {name}', { name: r.name }) : t('New AutoMod rule'), body: h('div', { class: 'stack' }, field(t('Name'), name), field(t('Looks for'), trig), trigBox,
    h('div', { class: 'group-label', text: t('Actions') }), block, alert, field(t('Time out the sender'), tsel),
    roleBoxes.length ? h('div', null, h('div', { class: 'group-label', text: t('Exempt roles') }), h('div', { class: 'row wrap' }, ...roleBoxes)) : null,
    h('div', null, h('div', { class: 'group-label', text: t('Exempt channels') }), h('div', { class: 'row wrap' }, ...chanBoxes)), enabled),
  actions: [{ label: t('Cancel') }, { label: t('Save rule'), kind: 'primary', run: async () => {
    await api('admin', adminUI.cid, 'save_automod_rule', {
      id: r.id || 0, name: name.value, trigger: trig.value, enabled: enabled.input.checked,
      keywords: kws.value, patterns: pats.value, mention_limit: Number(limit.value), allowed_domains: domains.value,
      block: block.input.checked, alert: alert.input.checked, timeout_seconds: Number(tsel.value),
      exempt_role_ids: roleBoxes.filter((b) => b.input.checked).map((b) => b.roleId),
      exempt_channel_ids: chanBoxes.filter((b) => b.input.checked).map((b) => b.chanId),
    });
    toast(t('Rule saved.'));
    if (after) after();
  } }] });
}

// Community Admin → Insights: the community's activity per day, counted by
// its server (counts only: nothing says who). Days with nothing recorded
// are shown as zero.
// (Community Admin calls a section with arguments of its own: the period
// is insightsPage's.)
function adminInsights(sec) { return insightsPage(sec, 30); }

async function insightsPage(sec, days) {
  sec.appendChild(h('p', { class: 'small muted', text: t('Counts kept by this community\'s server, per day (UTC): nothing here says who. Active members are those who connected that day.') }));
  const range = selectEl([['7', t('Last 7 days')], ['30', t('Last 30 days')], ['90', t('Last 90 days')]], String(days));
  range.addEventListener('change', () => insightsPage(clear(sec), Number(range.value)));
  sec.appendChild(field(t('Period'), range));
  let r;
  try { r = await api('admin_query', adminUI.cid, 'insights', { days }); } catch (e) { sec.appendChild(h('div', { class: 'error', text: e.message })); return; }
  const byDay = new Map(r.days.map((d) => [d.day, d]));
  const series = [];
  for (let i = days - 1; i >= 0; i--) {
    const day = new Date(Date.now() - i * 86400000).toISOString().slice(0, 10);
    series.push(byDay.get(day) || { day, members: 0, joined: 0, left: 0, messages: 0, active: 0, posters: 0, voice_minutes: 0, peak_online: 0 });
  }
  const sum = (k) => series.reduce((n, d) => n + d[k], 0);
  const avg = (k) => Math.round(sum(k) / Math.max(1, series.length));
  const tile = (label, value) => h('div', { class: 'insight-tile' }, h('div', { class: 'insight-value', text: String(value) }), h('div', { class: 'small muted', text: label }));
  sec.appendChild(h('div', { class: 'insight-tiles' },
    tile(t('Members now'), r.members), tile(t('Online now'), r.online),
    tile(t('Joined'), sum('joined')), tile(t('Left'), sum('left')),
    tile(t('Messages'), sum('messages')), tile(t('Active members a day (average)'), avg('active')),
    tile(t('Voice minutes'), sum('voice_minutes'))));
  const chart = (title, keys) => {
    const peak = Math.max(1, ...series.map((d) => Math.max(...keys.map(([k]) => d[k]))));
    const bars = h('div', { class: 'insight-chart', 'aria-hidden': 'true' }, ...series.map((d) => h('div', { class: 'insight-day', title: d.day + ': ' + keys.map(([k, label]) => `${label} ${d[k]}`).join(', ') },
      ...keys.map(([k], i) => h('div', { class: 'insight-bar insight-bar-' + i, style: { height: (100 * d[k] / peak) + '%' } })))));
    // The same numbers as a table, for screen readers and copying.
    const table = h('table', { class: 'sr-only' }, h('tr', {}, h('th', { text: t('Day') }), ...keys.map(([, label]) => h('th', { text: label }))),
      ...series.map((d) => h('tr', {}, h('td', { text: d.day }), ...keys.map(([k]) => h('td', { text: String(d[k]) })))));
    const legend = keys.length > 1 ? h('div', { class: 'insight-legend small' }, ...keys.map(([, label], i) => h('span', {}, h('i', { class: 'insight-bar-' + i }), label))) : null;
    return formSection(title, [bars, legend, table]);
  };
  sec.append(
    chart(t('Messages a day'), [['messages', t('Messages')]]),
    chart(t('Active members a day'), [['active', t('Connected')], ['posters', t('Posted')]]),
    chart(t('Joined and left'), [['joined', t('Joined')], ['left', t('Left')]]),
    chart(t('Voice minutes a day'), [['voice_minutes', t('Minutes')]]));
  if (r.channels.length) {
    sec.appendChild(formSection(t('Busiest channels (last 30 days)'), [h('table', { class: 'insight-channels' },
      h('tr', {}, h('th', { text: t('Channel') }), h('th', { text: t('Messages') })),
      ...r.channels.slice(0, 15).map((c) => h('tr', {}, h('td', { text: c.name ? '#' + c.name : t('(deleted channel)') }), h('td', { text: String(c.messages) }))))]));
  }
}

async function adminAuditLog(sec) {
  if (adminUI.s.can.manage_community) {
    const vis = selectEl([['admins', t('Admins (members with View audit log)')], ['everyone', t('Every member')]], adminUI.s.settings.audit_visibility);
    sec.appendChild(formSection(null, field(t('Who can see the audit log'), vis), saveButton(() => ({ audit_visibility: vis.value }))));
  }
  let members = [];
  try { members = await api('members', adminUI.cid); } catch (e) { /* offline: filters by key still work */ }
  const people = [['', t('Anyone')], ...members.map((m) => [m.key, m.display_name])];
  const f = adminUI.auditFilter || (adminUI.auditFilter = { action: '', actor: '', target: '', since: '', until: '' });
  const action = selectEl(tOptions(AUDIT_ACTIONS), f.action); action.setAttribute('aria-label', t('Action'));
  const actor = selectEl(people, f.actor); actor.setAttribute('aria-label', t('Done by'));
  const target = selectEl(people, f.target); target.setAttribute('aria-label', t('Done to'));
  const since = h('input', { type: 'text', value: f.since, placeholder: t('YYYY-MM-DD'), 'aria-label': t('From') });
  const until = h('input', { type: 'text', value: f.until, placeholder: t('YYYY-MM-DD'), 'aria-label': t('To') });
  const results = h('div', { class: 'stack audit-results' });
  const table = h('table', { class: 'list audit' });
  const more = h('button', { class: 'btn small', text: t('Load more') });
  let next = 0;
  const load = async (reset) => {
    if (reset) { next = 0; clear(table).appendChild(h('tr', null, ...[t('When'), t('Who'), t('What'), t('Signed')].map((x) => h('th', { text: x })))); }
    const err = $('.error', results); if (err) err.remove();
    let page;
    try { page = await api('admin_query', adminUI.cid, 'audit_page', { ...f, before_id: next }); }
    catch (e) { results.prepend(h('div', { class: 'error', text: e.message })); return; }
    for (const e of page.entries) {
      table.appendChild(h('tr', { class: 'audit-row' }, h('td', { class: 'small', text: fmtDateTime(e.at) }), h('td', { text: e.actor_name }), h('td', { text: e.summary }),
        h('td', null, e.system ? h('span', { class: 'pill', text: t('server') }) : h('span', { class: 'pill ' + (e.signature_ok ? 'ok' : 'bad'), text: e.signature_ok ? t('valid') : t('INVALID') }))));
    }
    next = page.next_before_id;
    more.hidden = !page.more;
    const empty = $('.audit-empty', results);
    if (empty) empty.remove();
    if (table.rows.length === 1) results.appendChild(h('p', { class: 'empty-note audit-empty', text: t('Nothing matches.') }));
  };
  more.addEventListener('click', () => load(false));
  sec.appendChild(h('h3', { text: t('Actions') }));
  sec.appendChild(h('div', { class: 'row wrap audit-filters' }, field(t('Action'), action), field(t('Done by'), actor), field(t('Done to'), target), field(t('From'), since), field(t('To'), until),
    h('button', { class: 'btn primary', text: t('Apply filters'), onclick: () => {
      Object.assign(f, { action: action.value, actor: actor.value, target: target.value, since: since.value.trim(), until: until.value.trim() });
      load(true);
    } }),
    h('button', { class: 'btn', text: t('Clear'), onclick: () => { adminUI.auditFilter = null; adminAuditLog(clear(sec)); } })));
  results.appendChild(table);
  results.appendChild(more);
  sec.appendChild(results);
  await load(true);
}

async function adminEmoji(sec) {
  const s = adminUI.s.settings;
  const can = adminUI.s.can;
  // The toggle itself is under Features, with the other feature toggles.
  if (!s.features.custom_emoji) {
    sec.appendChild(emptyNote(can.manage_community ? t('Custom emoji are turned off. Turn them on under Features.') : t('Custom emoji are turned off.')));
    return;
  }
  if (can.manage_community) {
    const max = h('input', { type: 'number', min: 1, max: 250, value: s.max_custom_emoji, 'aria-label': t('Most custom emoji') });
    sec.appendChild(formSection(null, field(t('Most custom emoji'), max, t('1 to 250.')),
      saveButton(() => ({ max_custom_emoji: Number(max.value) }))));
  }
  const list = adminUI.s.emoji || [];
  sec.appendChild(h('h3', { text: t('Emoji ({count} of {max})', { count: list.length, max: s.max_custom_emoji }) }));
  if (can.manage_emoji) {
    const name = h('input', { type: 'text', placeholder: t('party_parrot'), 'aria-label': t('Emoji name') });
    const path = h('input', { type: 'text', placeholder: t('/path/to/image.png'), 'aria-label': t('Image path') });
    const err = h('div', { class: 'error' });
    const upload = h('button', { class: 'btn small primary', text: t('Upload'), onclick: async () => {
      err.textContent = '';
      upload.disabled = true;
      try {
        const p = await pickFile('image', path.value);
        if (!p) { upload.disabled = false; return; }
        await api('emoji_upload', adminUI.cid, name.value, p);
        await reloadAdmin();
        toast(t('Added :{name}:.', { name: name.value.trim() }));
        adminEmoji(clear(sec));
      } catch (e) { err.textContent = e.message; }
      upload.disabled = false;
    } });
    sec.appendChild(h('div', { class: 'card stack emoji-upload' },
      h('div', { class: 'small muted', text: t('PNG, GIF or WebP (animated is fine), up to 256 KB and 128×128 pixels. Names use a to z, 0 to 9 and _.') }),
      h('div', { class: 'row wrap' }, field(t('Name'), name), field(t('Image'), h('div', { class: 'row' }, path,
        h('button', { class: 'btn small', text: t('Browse…'), onclick: async () => { const p = await pickFile('image'); if (p) path.value = p; } }))), upload), err));
  }
  const rows = [];
  for (const e of list) {
    rows.push(h('div', { class: 'setting-row emoji-row', 'data-name': e.name },
      h('div', { class: 'row' }, emojiImg(adminUI.cid, e, 'emoji-big'), h('div', null, h('b', { text: `:${e.name}:` }),
        h('div', { class: 'desc', text: e.animated ? t('{size} · animated', { size: fmtBytes(e.size) }) : fmtBytes(e.size) }))),
      can.manage_emoji ? h('div', { class: 'row' },
        h('button', { class: 'btn small', text: t('Rename…'), onclick: async () => {
          const n = await promptModal(t('Rename :{name}:', { name: e.name }), t('New name'), e.name, { ok: t('Rename') });
          if (!n) return;
          try { await api('admin', adminUI.cid, 'rename_emoji', { id: e.id, name: n }); await reloadAdmin(); } catch (x) { toastError(x); }
          adminEmoji(clear(sec));
        } }),
        h('button', { class: 'btn small danger', text: t('Delete'), onclick: async () => {
          if (!(await confirmModal(t('Delete :{name}:?', { name: e.name }), t('Messages that use it show its name instead.'), t('Delete')))) return;
          try { await api('admin', adminUI.cid, 'delete_emoji', { id: e.id }); await reloadAdmin(); } catch (x) { toastError(x); }
          adminEmoji(clear(sec));
        } })) : null));
  }
  sec.appendChild(rows.length ? formSection(null, rows) : emptyNote(t('No custom emoji yet.')));
}
