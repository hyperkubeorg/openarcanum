// A person's menu, the same wherever a person is shown: the member list,
// a message's author (name and avatar), a mention, a reply's quote, who
// reacted, the DM list, a DM's author, a group DM's people, Contacts and
// Community Admin's Members. A right-click opens it, or the Menu key /
// Shift+F10 on the focused name (dom.js). The entries are what applies to
// that person and to what you may do in that community; every action is the
// one the rest of the app uses (the server checks permissions again).
//
// userMenu(e, who, opts) from an event handler; userMenuItems(who, opts) for
// a menu that adds its own entries (a call's participant menu).
//   who:  { name, cid + key (a community member), member (their member-list
//           entry, if at hand), ident (their DM identity key), person (a
//           participant from dm_info), contact (an entry from dm_contacts) }
//   opts: { channel (the community channel the name is shown in: Mention
//           goes to its message box), admin: { after } (a Community Admin
//           row: redraw it after an action), dm (the dm_info of the
//           conversation the person is shown in), invited (they're invited
//           to it, not in it yet), after (redraw after Block or Add as contact) }
'use strict';

function userMenu(e, who, opts = {}) {
  e.preventDefault();
  e.stopPropagation();
  return openUserMenu(e.clientX, e.clientY, who, opts);
}

async function openUserMenu(x, y, who, opts = {}) {
  let items;
  try { items = await userMenuItems(who, opts); } catch (err) { toastError(err); return; }
  openMenu(x, y, items, { label: personName(who) });
}

// The name a person's menu carries (its accessible name).
function personName(who) {
  const m = who.member || (who.cid && who.key ? findMember(who.cid, who.key) : null);
  return who.name || (m ? shownName(m) : '') || (who.person && who.person.name) || (who.contact && who.contact.name) || '';
}

// Who reacted with one emoji (a right-click on the reaction): pick someone
// for their menu.
function whoReactedMenu(e, emoji, people, opts = {}) {
  e.preventDefault();
  e.stopPropagation();
  const x = e.clientX;
  const y = e.clientY;
  const shown = String(emoji).replace(/^:([A-Za-z0-9_]{2,32}):\d+$/, ':$1:');
  openMenu(x, y, [{ group: t('Reacted with {emoji}', { emoji: shown }), items: people.map((p) => ({ label: p.name, run: () => openUserMenu(x, y, p, opts) })) }]);
}

function userMenuItems(who, opts = {}) {
  return who.cid ? memberMenuItems(who, opts) : dmPersonMenuItems(who, opts);
}

// ---------------------------------------------------------------- a community member

function findMember(cid, k) { return (st.members[cid] || []).find((x) => x.key === k) || null; }

// The member list isn't loaded until it's first shown: load it quietly.
async function memberOf(cid, k) {
  let m = findMember(cid, k);
  if (m || !st.communities.some((c) => c.id === cid && c.status === 'active')) return m;
  try { st.members[cid] = await api('members', cid); } catch (e) { return null; }
  return findMember(cid, k);
}

async function memberMenuItems(who, opts) {
  const cid = who.cid;
  const summary = st.communities.find((c) => c.id === cid);
  const s = st.states[cid];
  const admin = opts.admin;
  const self = admin && adminUI.s ? adminUI.s.self : s && s.self;
  const me = !!self && who.key === self.key;
  const m = who.member || await memberOf(cid, who.key);
  // Permissions as Community Admin's Members section checks them.
  const can = (admin && adminUI.s ? adminUI.s.can : summary && summary.can) || {};
  const here = !!(m && summary && summary.status === 'active');
  // Without the member and an active community the menu has little in it:
  // say why in the engine's log (the stderr log in CI) when that happens.
  if (!here || !self) {
    const why = !m ? 'no-member' : !summary ? 'no-community' : summary.status !== 'active' ? `status-${summary.status}` : 'no-state';
    console.warn(`person-menu ${String(who.key).slice(0, 8)}: ${why}`);
  }
  const contacts = me ? [] : await api('dm_contacts').catch(() => []);
  const contact = contacts.find((c) => c.community === cid && c.member === who.key);
  const cards = !!summary && featureOn(summary, 'contact_cards');
  const after = admin ? admin.after : () => loadMembers(cid);
  const others = here && !me;
  const moderate = others && !m.owner;
  const mention = !admin && others ? () => mentionInput(cid, opts.channel) : null;
  return tidyMenu([
    // Their status and custom text (status.js), above what you can do.
    here ? statusNote(memberStatus(cid, m)) : null,
    here ? { label: t('View profile'), run: () => profileCard(m, cid) } : null,
    others ? messageEntry(contact && contact.id, cards ? () => dmUI.startWith(cid, m) : null) : null,
    mention && mention() ? { label: t('Mention'), run: () => insertMention(mention, shownName(m)) } : null,
    // Also for a contact: adding again fetches their card afresh (a changed
    // identity key shows up in your conversations).
    others && cards ? { label: t('Add as contact'), run: () => api('dm_add_contact', cid, m.key).then(() => toast(t('Added to contacts.')), toastError) } : null,
    { label: t('Copy user ID'), run: () => copyUserId(who.key) },
    contact ? blockEntry(contact.id, contact.blocked, opts.after) : null,
    { separator: true },
    me && here ? { label: t('Edit your profile here'), run: () => communityProfileDialog(summary) } : null,
    others && can.manage_nicknames ? { label: t('Change nickname…'), run: () => nicknameDialog(cid, m, admin ? after : () => { loadMembers(cid); loadState(cid); }) } : null,
    here && can.manage_roles ? { label: t('Roles…'), run: () => rolesDialog(cid, m, after) } : null,
    moderate && can.timeout_members ? (timedOut(m)
      ? { label: t('Remove timeout'), run: () => liftTimeout(cid, m, after) }
      : { label: t('Time out…'), danger: true, run: () => timeoutDialog(cid, m, after) }) : null,
    moderate && can.kick ? { label: t('Kick…'), danger: true, run: () => kickDialog(cid, m, after) } : null,
    moderate && can.ban ? { label: t('Ban…'), danger: true, run: () => banDialog(cid, m, after) } : null,
  ]);
}

// The message box a Mention goes to: the one showing `channel` (the main
// view or a voice channel's chat), else the main one; none when you can't
// post there.
function mentionInput(cid, channel) {
  if (st.view !== 'community' || cid !== st.cid) return null;
  const pane = channel !== undefined ? paneOf(channel) : 'main';
  return pane ? document.getElementById(PANES[pane].input) : null;
}

// "@name " at the cursor, as if typed (the draft is saved, typed mentions
// become tokens when sent).
function insertMention(getInput, name) {
  closeDialogs();
  const input = getInput();
  if (!input) return;
  const at = input.selectionStart ?? input.value.length;
  const end = input.selectionEnd ?? at;
  const before = input.value.slice(0, at);
  input.setRangeText(`${before && !/\s$/.test(before) ? ' ' : ''}@${name} `, at, end, 'end');
  input.dispatchEvent(new Event('input', { bubbles: true }));
  input.focus();
}

// ---------------------------------------------------------------- a DM participant or contact

async function dmPersonMenuItems(who, opts) {
  const p = who.person || {};
  const c = who.contact || {};
  const ident = who.ident || p.id || c.id;
  const name = who.name || p.name || c.name;
  const me = !!p.me;
  const info = opts.dm;
  const community = p.community || c.community;
  const member = p.member || c.member;
  const blocked = p.blocked !== undefined ? p.blocked : !!c.blocked;
  const m = community && member && !me ? await memberOf(community, member) : null;
  const inGroup = info && !info.direct && !opts.invited;
  const open = info && st.view === 'dms' && dmUI.current === info.id;
  const after = opts.after || (() => dmUI.reload());
  const owner = info && info.owner && !info.direct && !me;
  return tidyMenu([
    ident || me ? statusNote(personStatus(ident, me)) : null,
    m ? { label: t('View profile'), run: () => profileCard(m, community) } : null,
    !me ? messageEntry(ident, community && member ? () => dmUI.startWith(community, { key: member }) : null) : null,
    inGroup && open && !me ? { label: t('Mention'), run: () => insertMention(() => $('#dm-input'), name) } : null,
    !me && !p.contact && !who.contact && community && member
      ? { label: t('Add as contact'), run: () => api('dm_add_contact', community, member).then(() => { toast(t('Added to contacts.')); after(); }, toastError) } : null,
    !me && ident ? { label: t('Rename…'), run: () => renameContactDialog({ id: ident, name, nickname: c.nickname || p.nickname || '', card_name: c.card_name || p.name || name }) } : null,
    !me ? { label: t('Safety number'), run: () => safetyDialog(ident, name, info ? info.id : '') } : null,
    { label: t('Copy user ID'), run: () => copyUserId(ident) },
    !me ? blockEntry(ident, blocked, after) : null,
    { separator: true },
    owner && !opts.invited ? { label: t('Make owner'), run: async () => {
      if (await confirmModal(t('Make owner'), t("Make {name} the owner of this group? You'll no longer be able to change its settings.", { name }), t('Make owner'), false)) dmCall('dm_transfer', info.id, ident);
    } } : null,
    owner ? { label: opts.invited ? t('Cancel invitation') : t('Remove from group'), danger: true, run: async () => {
      if (await confirmModal(t('Remove'), t("Remove {name} from this group? They won't receive anything new.", { name }), t('Remove'))) dmCall('dm_remove_member', info.id, ident);
    } } : null,
    // On the conversation's row: the whole conversation.
    info && info.direct && opts.row ? { label: t('Delete conversation'), danger: true, run: () => leaveConversation(info) } : null,
  ]);
}

// ---------------------------------------------------------------- shared entries

// Message: their 1:1 conversation if there is one (unless it's the one on
// screen), else a new one through `start`, the usual way (their contact
// card and DM settings decide whether it's a request).
function messageEntry(ident, start) {
  const conv = ident ? dmUI.list.find((c) => c.direct && c.status !== 'removed' && (c.peers || []).includes(ident)) : null;
  if (conv) return conv.id === dmUI.current && st.view === 'dms' ? null : { label: t('Message'), run: () => { closeDialogs(); return dmUI.open(conv.id); } };
  return start ? { label: t('Message'), run: () => { closeDialogs(); return start(); } } : null;
}

function blockEntry(ident, blocked, after) {
  return { label: blocked ? t('Unblock') : t('Block'), run: () => api('dm_block', ident, !blocked).then(() => {
    dmUI.refresh();
    if (after) return after();
    return dmUI.reload();
  }, toastError) };
}

function copyUserId(id) {
  return api('copy_text', id).then(() => toast(t('User ID copied.')), toastError);
}

// No separator first, last or twice in a row.
function tidyMenu(items) {
  const out = [];
  for (const it of items) {
    if (!it) continue;
    if (it.separator && (!out.length || out[out.length - 1].separator)) continue;
    out.push(it);
  }
  while (out.length && out[out.length - 1].separator) out.pop();
  return out;
}
