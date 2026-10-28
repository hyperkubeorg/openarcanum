// Community settings. Changes are signed admin actions; the
// server checks permissions and records each one in the audit log.
'use strict';

// Permissions travel as lists of names (64-bit masks don't fit JavaScript's
// bit operators); the catalog (name, label, group, channel) comes from
// Python's permissions.py.
const adminUI = { cid: null, s: null };

function permCatalog() { return adminUI.s.permission_catalog || []; }

// The toggles in the Features section, by the names permissions.py and the
// server use.
const FEATURE_TOGGLES = [
  [N_('Content'), [['attachments', N_('Attachments'), N_('Members with Attach Files can send up to 10 files per message (see Files for the size limit).')],
    ['link_previews', N_('Link previews'), N_('The community server fetches a preview of the first link in a message, so linked sites see the server\'s address, never a member\'s. Senders need Embed Links. Off by default.')],
    ['reactions', N_('Reactions'), N_('Members with Add Reactions can react with emoji.')],
    ['custom_emoji', N_('Custom emoji'), N_('Members with Use Custom Emoji can use the community\'s own emoji (Emoji section) in messages and reactions.')]]],
  [N_('Voice and video'), [['voice', N_('Voice channels'), N_('Off hides every voice channel and ends calls.')], ['video', N_('Video'), ''], ['screen_share', N_('Screen share'), '']]],
  [N_('Discovery and contact'), [['search', N_('Search'), N_('The search index is kept in the server\'s memory, never on disk.')],
    ['contact_cards', N_('Member contact cards'), N_('Members can share a contact card here so other members can start direct messages with them.')]]],
  [N_('Messaging'), [['edit_own', N_('Editing own messages'), N_('Members can edit what they posted.')],
    ['delete_own', N_('Deleting own messages'), N_('Members can delete what they posted. Manage Messages can always delete.')],
    ['typing', N_('Typing indicators'), N_('Show when someone is typing. Nothing is stored.')],
    ['history_for_new_members', N_('New members see history'), N_('Off: members see only messages posted after they joined.')],
    ['join_leave_messages', N_('Join and leave messages'), N_('Post a message when someone joins or leaves.')]]],
];

// My highest role position (owners outrank every role).
function myTop(s = adminUI.s) {
  if (s.is_owner) return Infinity;
  const pos = s.roles.filter((r) => s.self.role_ids.includes(r.id)).map((r) => r.position);
  return pos.length ? Math.max(...pos) : 0;
}

// The moderator queues' counts for the community open in Community Admin,
// from its newest summary.
function adminQueue() { return modQueue(st.communities.find((c) => c.id === adminUI.cid)); }

// A community's counts changed: Community Admin's badges follow, and an
// open queue shows what's new.
function adminBadgesChanged(summary) {
  const shell = adminUI.shell;
  if (!shell || !shell.open() || summary.id !== adminUI.cid) return;
  shell.refreshBadges();
  const before = adminUI.shownQueue || {};
  const now = modQueue(summary);
  adminUI.shownQueue = now;
  const section = shell.current();
  if ((section === 'reports' && now.reports !== before.reports) || (section === 'pending' && now.pending !== before.pending)) shell.show(section);
}

// Community Admin (the red entry in the community menu and the rail's menu).
// Without a section asked for, it opens where something waits for review
// (open reports, then join requests), else on the first section the member
// can use.
async function openCommunitySettings(cid, initial, focus) {
  adminUI.cid = cid;
  adminUI.focus = focus;
  adminUI.s = await api('community_state', cid);
  // Not synced yet (connecting or offline): there are no settings to show.
  if (!adminUI.s.settings) { toast(t("Community Admin opens once you're connected to this community.")); return; }
  const can = adminUI.s.can;
  const owner = adminUI.s.is_owner;
  const queue = adminQueue();
  adminUI.shownQueue = queue;
  if (!initial) initial = can.manage_messages && queue.reports ? 'reports' : can.approve_members && queue.pending ? 'pending' : undefined;
  const sections = [
    can.manage_community && { group: adminUI.s.name, id: 'overview', label: t('Overview'), render: adminOverview },
    can.manage_community && { group: adminUI.s.name, id: 'visibility', label: t('Visibility'), render: adminVisibility },
    (can.create_invite || can.manage_community) && { group: t('Joining'), id: 'invites', label: t('Invites & join policy'), render: adminInvites },
    can.approve_members && { group: t('Joining'), id: 'pending', label: t('Approval queue'), render: adminPending, badge: () => adminQueue().pending },
    can.manage_roles && { group: t('People'), id: 'roles', label: t('Roles'), render: adminRoles },
    (can.manage_roles || can.kick || can.ban || can.manage_nicknames || can.timeout_members) && { group: t('People'), id: 'members', label: t('Members'), render: adminMembers },
    can.ban && { group: t('People'), id: 'bans', label: t('Bans'), render: adminBans },
    can.manage_channels && { group: t('Content'), id: 'channels', label: t('Channels'), render: adminChannels },
    can.manage_community && { group: t('Content'), id: 'features', label: t('Features'), render: adminFeatures },
    can.manage_community && { group: t('Content'), id: 'retention', label: t('Retention'), render: adminRetention },
    can.manage_community && { group: t('Content'), id: 'files', label: t('Files'), render: adminFiles },
    (can.manage_emoji || can.manage_community) && { group: t('Content'), id: 'emoji', label: t('Emoji'), render: adminEmoji },
    can.manage_messages && { group: t('Moderation'), id: 'reports', label: t('Reports'), render: adminReports, badge: () => adminQueue().reports },
    can.manage_community && { group: t('Moderation'), id: 'automod', label: t('AutoMod'), render: adminAutoMod },
    can.manage_community && { group: t('Moderation'), id: 'screening', label: t('Member screening'), render: adminScreening },
    (can.view_audit_log || adminUI.s.settings.audit_visibility === 'everyone') && { group: t('Moderation'), id: 'audit', label: t('Audit log'), render: adminAuditLog },
    (can.manage_community || can.view_audit_log) && { group: adminUI.s.name, id: 'insights', label: t('Insights'), render: adminInsights },
    can.manage_community && { group: t('Network'), id: 'network', label: t('Relays'), render: adminNetwork },
    can.manage_community && { group: t('Network'), id: 'members-limits', label: t('Devices'), render: adminLimits },
    (owner || can.manage_community || adminUI.s.self.owner_invited) && { group: t('Ownership'), id: 'ownership', label: t('Ownership'), render: adminOwnership },
  ].filter(Boolean);
  if (!sections.length) { toast(t("You don't have permission to change this community's settings.")); return; }
  // The page is about the community it shows beside: from another
  // community's rail menu (or direct messages), that community opens first.
  if (st.view !== 'community' || st.cid !== cid) await selectCommunity(cid);
  const cls = 'settings community-admin'; // i18n: not shown (CSS classes)
  adminUI.shell = showPage({ title: t('Community Admin'), context: adminUI.s.name, icon: 'settings', cls, nav: sections, initial,
    onClose: async () => { adminUI.shell = null; await loadState(cid); render(); } });
  return adminUI.shell;
}

// Disconnected meanwhile: keep showing what was last synced.
async function reloadAdmin() { const s = await api('community_state', adminUI.cid); if (s.settings) adminUI.s = s; }

async function saveSettings(patch, msg = t('Saved.')) {
  await api('admin', adminUI.cid, 'update_settings', patch);
  await reloadAdmin();
  toast(msg);
}

// The footer of a settings form (formSection): its error on the left and
// Save changes on the right, under a divider.
function saveButton(collect, after) {
  const err = h('div', { class: 'error', role: 'alert' });
  const b = h('button', { class: 'btn primary', text: t('Save changes'), onclick: async () => {
    err.textContent = '';
    b.disabled = true;
    try { await saveSettings(collect()); if (after) after(); } catch (e) { err.textContent = e.message; }
    b.disabled = false;
  } });
  return formFoot(err, b);
}

function adminOverview(sec) {
  const s = adminUI.s.settings;
  const name = h('input', { type: 'text', value: s.name });
  const desc = h('textarea', { rows: 3, value: s.description });
  const lang = h('input', { type: 'text', value: s.language, placeholder: t('e.g. en') });
  let icon = s.icon;
  const iconPrev = h('div', { class: 'icon-preview' }, icon ? h('img', { src: 'data:image/png;base64,' + icon }) : initials(s.name));
  sec.appendChild(formSection(null, [field(t('Name'), name), field(t('Description'), desc),
    h('div', { class: 'row' }, iconPrev, h('button', { class: 'btn small', text: t('Change icon…'), onclick: async () => {
      const p = await pickFile('image'); if (!p) return;
      try { icon = await api('read_image', p); clear(iconPrev).appendChild(h('img', { src: 'data:image/png;base64,' + icon })); } catch (e) { toastError(e); }
    } }), h('button', { class: 'btn link small', text: t('Remove icon'), onclick: () => { icon = ''; clear(iconPrev).appendChild(document.createTextNode(initials(name.value))); } })),
    field(t('Language'), lang, t('A short language tag shown in the directory.'))],
  saveButton(() => ({ name: name.value, description: desc.value, language: lang.value, icon }))));
}

// Who can find the community and who can join it, together: visibility
// decides whether it is listed in the directory, the join policy (under
// Invites & join policy) whether finding it is enough to get in.
function reachSummary(visibility, policy) {
  if (visibility !== 'public') return t('Not listed: only people with an invite link can find it, whatever the join policy.');
  if (policy === 'open') return t('Anyone can find it in directory search and join straight away, with no invite.');
  if (policy === 'request') return t('Anyone can find it in directory search and ask to join; an admin approves each request (Approval queue).');
  return t('Listed in directory search, but joining needs an invite (or the public join key, if you made one).');
}

function adminVisibility(sec, show) {
  const s = adminUI.s.settings;
  // No maintainer node of this device serves the directory: listing isn't
  // offered (a community listed before can still go private).
  const listing = directoryAvailable();
  const visOptions = [['private', t('Private: reachable only through invite links')]];
  if (listing || s.visibility === 'public') visOptions.push(['public', t('Public: listed and searchable in the directory')]);
  const vis = selectEl(visOptions, s.visibility);
  const tags = h('input', { type: 'text', value: s.tags.join(', ') });
  const now = h('p', { class: 'reach-now', text: reachSummary(s.visibility, s.join_policy) });
  const preview = h('p', { class: 'small muted reach-preview' });
  vis.addEventListener('change', () => {
    preview.textContent = vis.value === s.visibility ? '' : t('After saving: {effect}', { effect: reachSummary(vis.value, s.join_policy) });
  });
  const canPolicy = adminUI.s.can.manage_community;
  const fullyPublic = s.visibility === 'public' && s.join_policy === 'open';
  const rerender = () => adminVisibility(clear(sec), show);
  sec.appendChild(formSection(t('Who can find and join'), [
    now,
    listing ? h('ul', { class: 'small muted reach-table' },
      h('li', { text: t('Public + Open: anyone can find it and join. No invite or key to share.') }),
      h('li', { text: t('Public + Request to join: anyone can find it and ask; admins approve.') }),
      h('li', { text: t('Public + Invite only: listed, but joining needs an invite.') }),
      h('li', { text: t('Private: invite links only.') })) : noDirectoryNote(),
  ], formFoot(
    show ? h('button', { class: 'btn', type: 'button', text: t('Change join policy…'), onclick: () => show('invites') }) : null,
    canPolicy && listing && !fullyPublic ? h('button', { class: 'btn primary', type: 'button', text: t('Let anyone find and join'), onclick: async () => {
      if (!await confirmModal(t('Let anyone find and join?'), t('The community is listed in the directory with its name, description, tags and member count, and anyone who finds it can join without an invite. You can change this back at any time.'), t('Make it public'), false)) return;
      try { await saveSettings({ visibility: 'public', join_policy: 'open' }); rerender(); } catch (e) { toastError(e); }
    } }) : null)));
  sec.appendChild(formSection(t('Visibility'), [
    field(t('Visibility'), vis, t('Public listings show the name, description, tags, roughly how many members it has and the join policy. Private communities aren\'t listed: the directory only knows how to reach them, not what they are.')),
    preview,
    field(t('Tags'), tags, t('Up to 10, comma-separated. Letters, digits and dashes.'))],
  saveButton(() => ({ visibility: vis.value, tags: tags.value }), rerender)));
}

async function adminInvites(sec, show) {
  const s = adminUI.s.settings;
  if (adminUI.s.can.manage_community) {
    const policy = selectEl(joinPolicyOptions(), s.join_policy);
    const maxm = h('input', { type: 'number', min: 0, value: s.max_members });
    const effect = h('p', { class: 'small muted reach-now', text: reachSummary(s.visibility, s.join_policy) });
    policy.addEventListener('change', () => { effect.textContent = reachSummary(s.visibility, policy.value); });
    sec.appendChild(formSection(t('Join policy'), [
      h('div', { class: 'row' }, field(t('Join policy'), policy), field(t('Maximum members'), maxm, t('0 = no limit'))),
      h('div', { class: 'row wrap' }, effect, show ? h('button', { class: 'btn link small', type: 'button', text: t('Visibility…'), onclick: () => show('visibility') }) : null)],
    saveButton(() => ({ join_policy: policy.value, max_members: Number(maxm.value) }), () => adminInvites(clear(sec), show))));
  }
  const can = adminUI.s.can;
  if (!can.create_invite && !can.manage_community) return;
  sec.appendChild(h('h3', { text: t('Invites') }));
  if (can.create_invite) sec.appendChild(toolbar(h('button', { class: 'btn primary', text: t('Create invite…'), onclick: () => createInviteDialog(() => adminInvites(clear(sec), show)) })));
  const keys = await api('admin_query', adminUI.cid, 'join_keys');
  const roles = Object.fromEntries(adminUI.s.roles.map((r) => [r.id, r.name]));
  const me = adminUI.s.self.key;
  const tbl = h('table', { class: 'list invites' }, h('tr', null, ...[t('Label'), t('Created by'), t('Uses'), t('Expires'), t('Grants'), ''].map((x) => h('th', { text: x }))));
  for (const k of keys) {
    const expired = k.expires_at && k.expires_at < Date.now();
    const mayRevoke = can.manage_community || (can.create_invite && k.created_by === me);
    tbl.appendChild(h('tr', { 'data-invite': String(k.id) },
      h('td', null, h('div', { text: k.label || t('(no label)') }), h('div', { class: 'row wrap' },
        k.revoked ? h('span', { class: 'pill bad', text: t('revoked') }) : expired ? h('span', { class: 'pill warn', text: t('expired') }) : h('span', { class: 'pill ok', text: t('active') }),
        k.request_only ? h('span', { class: 'pill', text: t('needs approval') }) : null, k.public ? h('span', { class: 'pill', text: t('public') }) : null)),
      h('td', { text: k.created_by_name }),
      h('td', { text: `${k.uses}${k.max_uses ? ' / ' + k.max_uses : ''}` }),
      h('td', { text: k.expires_at ? fmtDateTime(k.expires_at) : t('never') }),
      h('td', { text: k.role_ids.map((id) => roles[id] || '?').join(', ') || '-' }),
      h('td', null, h('div', { class: 'row' },
        k.invite && !k.revoked ? copyButton(k.invite, t('Copy invite')) : null,
        !k.revoked && mayRevoke ? h('button', { class: 'btn small danger', text: t('Revoke'), 'aria-label': k.label ? t('Revoke {label}', { label: k.label }) : t('Revoke invite'), onclick: async () => {
          try { await api('admin', adminUI.cid, 'revoke_invite', { id: k.id }); } catch (e) { toastError(e); }
          adminInvites(clear(sec), show);
        } }) : null,
        can.kick && can.manage_community ? h('button', { class: 'btn small', text: t('Raid revoke…'), onclick: () => raidDialog(k, () => adminInvites(clear(sec), show)) }) : null))));
  }
  sec.appendChild(keys.length ? tbl : emptyNote(t('No invites yet.')));
  sec.appendChild(h('p', { class: 'small muted', text: can.manage_community
    ? t("A key's secret is shown only once, when it's created (except for public keys). Revoking a key stops it working at once.")
    : t('You see the invites you created. Revoking stops one working immediately.') }));
}

function createInviteDialog(after) {
  const label = h('input', { type: 'text', placeholder: t('e.g. for the newsletter') });
  const exp = selectEl([[1 / 24, t('1 hour')], [1, t('1 day')], [7, t('7 days')], [30, t('30 days')], [0, t('Never')]], '7');
  const uses = h('input', { type: 'number', min: 0, value: 0 });
  const req = checkbox(t('Needs approval'), false, t('People using this key land in the approval queue.'));
  const pub = checkbox(t('Public join key'), false, t('Shown in the directory listing (public communities), so anyone who finds the community can join with it even when the join policy is Invite only. Not needed with the Open policy. Replaces any previous public key.'));
  const top = myTop();
  const roleBoxes = adminUI.s.can.manage_roles
    ? adminUI.s.roles.filter((r) => !r.builtin && r.position < top).map((r) => { const c = checkbox(r.name, false); c.roleId = r.id; return c; }) : [];
  const out = h('div');
  modal({ title: t('Create invite'), body: h('div', { class: 'stack' }, field(t('Label'), label), h('div', { class: 'row' }, field(t('Expires after'), exp), field(t('Max uses'), uses, t('0 = unlimited'))),
    roleBoxes.length ? h('div', null, h('div', { class: 'group-label', text: t('Roles granted on joining') }), h('div', { class: 'row wrap' }, ...roleBoxes)) : null,
    req, adminUI.s.can.manage_community ? pub : null, out),
  actions: [{ label: t('Close') }, { label: t('Create'), kind: 'primary', run: async () => {
    const r = await api('admin', adminUI.cid, 'create_invite', {
      label: label.value, expires_days: Number(exp.value), max_uses: Number(uses.value), request_only: req.input.checked, public: pub.input.checked,
      role_ids: roleBoxes.filter((b) => b.input.checked).map((b) => b.roleId),
    });
    clear(out).appendChild(codeBlock(r.invite, { cls: 'invite-code', copyLabel: t('Copy invite') }));
    if (after) after();
    return false;
  } }] });
}

function raidDialog(k, after) {
  const hours = h('input', { type: 'number', min: 1, max: 720, value: 24 });
  modal({ title: t('Raid revoke'), body: h('div', { class: 'stack' },
    h('p', { text: t('Revoke “{label}” and remove everyone who joined with it in the last:', { label: k.label || t('this key') }) }), field(t('Hours'), hours),
    h('p', { class: 'small muted', text: t('Removed members lose access to channels at once: the channels get new keys.') })),
  actions: [{ label: t('Cancel') }, { label: t('Revoke and remove'), kind: 'danger', run: async () => {
    const r = await api('admin', adminUI.cid, 'raid_revoke', { join_key_id: k.id, hours: Number(hours.value) });
    toast(t('Revoked; removed {n} member(s).', { n: r.removed })); if (after) after();
  } }] });
}

async function adminPending(sec) {
  const list = await api('admin_query', adminUI.cid, 'pending');
  if (!list.length) { sec.appendChild(emptyNote(t('Nobody is waiting.'))); return; }
  const rows = [];
  for (const m of list) {
    rows.push(h('div', { class: 'setting-row' }, h('div', null, h('b', { text: m.display_name }), h('div', { class: 'desc mono', text: m.key }), h('div', { class: 'desc', text: t('Asked {time}', { time: fmtDateTime(m.joined_at) }) })),
      h('div', { class: 'row' },
        h('button', { class: 'btn small primary', text: t('Approve'), onclick: async () => { await api('admin', adminUI.cid, 'approve', { member: m.key }); adminPending(clear(sec)); } }),
        h('button', { class: 'btn small danger', text: t('Deny'), onclick: async () => { await api('admin', adminUI.cid, 'deny', { member: m.key }); adminPending(clear(sec)); } }))));
  }
  sec.appendChild(formSection(null, rows));
}

// The permission grid for a role, grouped like Discord's. Permissions the
// editor doesn't hold are shown but can't be changed (the server refuses).
function permChecks(selected) {
  const has = new Set(selected || []);
  const can = adminUI.s.can;
  const boxes = [];
  const el = h('div', { class: 'stack perm-groups' });
  let group = null;
  let grid = null;
  for (const p of permCatalog()) {
    if (p.group !== group) {
      group = p.group;
      grid = h('div', { class: 'perm-grid' });
      el.appendChild(h('div', { class: 'perm-group small', text: group }));
      el.appendChild(grid);
    }
    const c = checkbox(p.label, has.has(p.name));
    c.permName = p.name;
    c.input.setAttribute('data-perm', p.name);
    if (!can[p.name]) { c.input.disabled = true; c.title = t("You don't have this permission, so you can't grant or remove it."); }
    boxes.push(c);
    grid.appendChild(c);
  }
  el.value = () => boxes.filter((b) => b.input.checked).map((b) => b.permName);
  return el;
}

function permLabels(names) {
  const set = new Set(names);
  return permCatalog().filter((p) => set.has(p.name)).map((p) => p.label);
}

async function adminRoles(sec) {
  await reloadAdmin();
  const roles = adminUI.s.roles.slice().sort((a, b) => b.position - a.position);
  const top = myTop();
  sec.appendChild(sectionIntro(t('Higher roles outrank lower ones: you can only manage roles and members below your own highest role, and only grant permissions you have. @everyone applies to all members.')));
  sec.appendChild(toolbar(h('button', { class: 'btn primary', text: t('Create role…'), onclick: () => roleDialog(null, () => adminRoles(clear(sec))) })));
  const ordered = roles.filter((r) => !r.builtin);
  const rows = [];
  for (const r of roles) {
    const idx = ordered.indexOf(r);
    const below = r.builtin || r.position < top;
    const tags = [r.hoist ? t('shown separately') : '', r.mentionable ? t('mentionable') : ''].filter(Boolean);
    rows.push(h('div', { class: 'setting-row role-row', 'data-role': String(r.id) },
      h('div', null, h('b', { text: r.name, style: roleColorStyle(r.color) }), ...tags.map((tag) => h('span', { class: 'pill', text: tag })),
        h('div', { class: 'desc', text: permLabels(r.perms).join(', ') || t('No permissions') })),
      h('div', { class: 'row' },
        !r.builtin ? h('button', { class: 'icon-btn', title: t('Move up'), icon: 'arrow-up', disabled: idx <= 0 || !below || ordered[idx - 1].position >= top, onclick: () => moveRole(ordered, idx, -1, sec) }) : null,
        !r.builtin ? h('button', { class: 'icon-btn', title: t('Move down'), icon: 'arrow-down', disabled: idx >= ordered.length - 1 || !below, onclick: () => moveRole(ordered, idx, 1, sec) }) : null,
        h('button', { class: 'btn small', text: t('Edit…'), disabled: !below, title: below ? '' : t('Only roles below your highest role'), 'aria-label': t('Edit {name}', { name: r.name }), onclick: () => roleDialog(r, () => adminRoles(clear(sec))) }),
        !r.builtin ? h('button', { class: 'btn small danger', text: t('Delete'), disabled: !below, onclick: async () => {
          if (await confirmModal(t('Delete role?'), t('Members lose “{role}”.', { role: r.name }), t('Delete'))) {
            try { await api('admin', adminUI.cid, 'delete_role', { id: r.id }); } catch (e) { toastError(e); }
            adminRoles(clear(sec));
          }
        } }) : null)));
  }
  sec.appendChild(formSection(null, rows));
}

async function moveRole(ordered, idx, dir, sec) {
  const list = ordered.slice();
  const [r] = list.splice(idx, 1);
  list.splice(idx + dir, 0, r);
  // The server wants lowest first.
  try { await api('admin', adminUI.cid, 'reorder_roles', { role_ids: list.map((x) => x.id).reverse() }); } catch (e) { toastError(e); }
  adminRoles(clear(sec));
}

function roleDialog(r, after) {
  const builtin = r && r.builtin;
  const name = h('input', { type: 'text', value: r ? r.name : '', disabled: builtin, 'aria-label': t('Role name') });
  const color = h('input', { type: 'text', value: r ? r.color : '', placeholder: '#3366ff', 'aria-label': t('Role color') }); // i18n: not shown (a hex color example, the same in every language)
  const hoist = checkbox(t('Display separately'), !!(r && r.hoist), t('Members with this role are listed under it in the member list.'));
  const mention = checkbox(t('Allow anyone to @mention this role'), !!(r && r.mentionable), t('Members with Mention @everyone can always mention it.'));
  const perms = permChecks(r ? r.perms : []);
  modal({ title: r ? t('Edit {name}', { name: r.name }) : t('Create role'), body: h('div', { class: 'stack role-dialog' }, field(t('Name'), name), field(t('Color'), color, t('A color code such as #3366ff. Leave empty for none.')),
    builtin ? null : hoist, builtin ? null : mention, h('div', { class: 'group-label', text: t('Permissions') }), perms),
  actions: [{ label: t('Cancel') }, { label: r ? t('Save') : t('Create'), kind: 'primary', run: async () => {
    const p = { name: name.value, color: color.value, permissions: perms.value(), hoist: hoist.input.checked, mentionable: mention.input.checked };
    if (r) await api('admin', adminUI.cid, 'edit_role', { id: r.id, ...p }); else await api('admin', adminUI.cid, 'create_role', p);
    if (after) after();
  } }] });
}

async function adminMembers(sec) {
  await reloadAdmin();
  const list = await api('members', adminUI.cid);
  const top = myTop();
  const roles = adminUI.s.roles.filter((r) => !r.builtin && r.position < top);
  const me = adminUI.s.self.key;
  if (adminUI.s.can.manage_roles && adminUI.s.can.kick) {
    sec.appendChild(toolbar(h('div', { class: 'spacer small muted', text: t('Someone lost their key and rejoined? Move the old key\'s roles to the new one.') }),
      h('button', { class: 'btn small', text: t('Replace member…'), onclick: () => replaceDialog(list, () => adminMembers(clear(sec))) })));
  }
  const rows = [];
  for (const m of list) {
    const rnames = m.role_ids.map((id) => (adminUI.s.roles.find((r) => r.id === id) || {}).name).filter(Boolean);
    const redraw = () => adminMembers(clear(sec));
    // A right-click on the row (or the Menu key on one of its buttons): the
    // person's menu (people.js), with these same actions.
    const row = h('div', { class: 'setting-row member-admin-row' + (adminUI.focus === m.key ? ' card' : ''),
      oncontextmenu: (e) => userMenu(e, { cid: adminUI.cid, key: m.key, member: m }, { admin: { after: redraw } }) },
      h('div', null, h('b', { text: m.display_name }), m.nickname ? h('span', { class: 'small muted', text: ` (${m.nickname})` }) : null, m.owner ? h('span', { class: 'pill', text: t('owner') }) : null,
        timedOut(m) ? h('span', { class: 'pill warn', text: t('timed out until {time}', { time: fmtUntil(m.timeout_until) }) }) : null,
        h('div', { class: 'desc', text: t('{roles} · joined {date}', { roles: rnames.join(', ') || t('No roles'), date: fmtDateTime(m.joined_at) }) }), h('div', { class: 'desc mono', text: m.key })),
      h('div', { class: 'row wrap' },
        adminUI.s.can.manage_nicknames && m.key !== me ? h('button', { class: 'btn small', text: t('Nickname…'), 'aria-label': t('Nickname of {name}', { name: m.display_name }), onclick: () => nicknameDialog(adminUI.cid, m, redraw) }) : null,
        adminUI.s.can.manage_roles && roles.length ? h('button', { class: 'btn small', text: t('Roles…'), 'aria-label': t('Roles of {name}', { name: m.display_name }), onclick: () => assignDialog(m, roles, () => adminMembers(clear(sec))) }) : null,
        adminUI.s.can.timeout_members && !m.owner && m.key !== me ? (timedOut(m)
          ? h('button', { class: 'btn small', text: t('Remove timeout'), 'aria-label': t('Remove timeout of {name}', { name: m.display_name }), onclick: () => liftTimeout(adminUI.cid, m, () => adminMembers(clear(sec))) })
          : h('button', { class: 'btn small', text: t('Time out…'), 'aria-label': t('Time out {name}', { name: m.display_name }), onclick: () => timeoutDialog(adminUI.cid, m, () => adminMembers(clear(sec))) })) : null,
        adminUI.s.can.kick && !m.owner && m.key !== me ? h('button', { class: 'btn small danger', text: t('Kick'), onclick: () => kickDialog(adminUI.cid, m, redraw) }) : null,
        adminUI.s.can.ban && !m.owner && m.key !== me ? h('button', { class: 'btn small danger', text: t('Ban…'), onclick: () => banDialog(adminUI.cid, m, () => adminMembers(clear(sec))) }) : null));
    rows.push(row);
  }
  sec.appendChild(formSection(null, rows));
}

function assignDialog(m, roles, after, cid = adminUI.cid) {
  const boxes = roles.map((r) => { const c = checkbox(r.name, m.role_ids.includes(r.id)); c.roleId = r.id; return c; });
  modal({ title: t('Roles for {name}', { name: m.display_name }), body: h('div', { class: 'stack role-assign' }, ...boxes),
    actions: [{ label: t('Cancel') }, { label: t('Save'), kind: 'primary', run: async () => {
      // Roles the editor can't manage (not shown) stay as they are.
      const kept = m.role_ids.filter((id) => !roles.some((r) => r.id === id));
      await api('admin', cid, 'assign_roles', { member: m.key, role_ids: [...kept, ...boxes.filter((b) => b.input.checked).map((b) => b.roleId)] });
      if (after) after();
    } }] });
}

// Roles… from a person's menu anywhere: the roles below your highest, as in
// Members.
async function rolesDialog(cid, m, after) {
  let s;
  try { s = await api('community_state', cid); } catch (e) { toastError(e); return; }
  const roles = (s.roles || []).filter((r) => !r.builtin && r.position < myTop(s));
  if (!roles.length) { toast(t('There are no roles below yours to give.')); return; }
  assignDialog(m, roles, after, cid);
}

function replaceDialog(list, after) {
  const opts = list.map((m) => [m.key, `${m.display_name} (${m.key.slice(0, 10)}…)`]);
  const oldSel = h('input', { type: 'text', placeholder: t('Old member key (from the audit log or member list)') });
  const newSel = selectEl(opts, opts.length ? opts[0][0] : '');
  modal({ title: t('Replace member'), body: h('div', { class: 'stack' },
    h('p', { class: 'small', text: t("The new key must already be a member. The old key's roles move over and the old key is removed.") }),
    field(t('Old key'), oldSel), field(t('New key (current member)'), newSel)),
  actions: [{ label: t('Cancel') }, { label: t('Replace'), kind: 'danger', run: async () => {
    await api('admin', adminUI.cid, 'replace_member', { old_key: oldSel.value.trim(), new_key: newSel.value }); toast(t('Member replaced.')); if (after) after();
  } }] });
}

async function adminBans(sec) {
  const bans = await api('admin_query', adminUI.cid, 'bans');
  sec.appendChild(sectionIntro(t('Anyone can make a new identity, so a ban only stops casual abuse. Invites you can revoke and the approval queue give you real control.')));
  if (!bans.length) { sec.appendChild(emptyNote(t('No bans.'))); return; }
  const rows = [];
  for (const b of bans) {
    rows.push(h('div', { class: 'setting-row' }, h('div', null, h('b', { text: b.display_name || t('Unknown') }), h('div', { class: 'desc', text: (b.reason || t('No reason')) + ' · ' + fmtDateTime(b.at) }),
      banBlockNote(b) ? h('div', { class: 'desc ban-network-block', text: banBlockNote(b) }) : null, h('div', { class: 'desc mono', text: b.key })),
      h('button', { class: 'btn small', text: t('Unban'), onclick: async () => { await api('admin', adminUI.cid, 'unban', { member: b.key }); adminBans(clear(sec)); } })));
  }
  sec.appendChild(formSection(null, rows));
}

function channelsIn(categoryId) {
  return adminUI.s.channels.filter((c) => (c.category_id || 0) === categoryId).sort((a, b) => a.position - b.position || a.id - b.id);
}

function sortedCategories() { return (adminUI.s.categories || []).slice().sort((a, b) => a.position - b.position || a.id - b.id); }

async function adminAct(op, params, sec) {
  try { await api('admin', adminUI.cid, op, params); } catch (e) { toastError(e); }
  if (sec) adminChannels(clear(sec));
}

async function adminChannels(sec) {
  await reloadAdmin();
  const voiceOn = adminUI.s.features.voice;
  sec.appendChild(sectionIntro(t('Channels are listed in this order in the sidebar: channels without a category first, then each category. A channel in a category follows the category\'s permissions until you change its own ("synced").')));
  sec.appendChild(toolbar(
    h('button', { class: 'btn primary', text: t('New text channel'), onclick: () => newChannel('text', 0, sec) }),
    voiceOn ? h('button', { class: 'btn', text: t('New voice channel'), onclick: () => newChannel('voice', 0, sec) }) : null,
    h('button', { class: 'btn', text: t('New category'), onclick: async () => {
      const name = await promptModal(t('New category'), t('Name'), '', { ok: t('Create') });
      if (name) adminAct('create_category', { name }, sec);
    } })));
  const box = h('div');
  channelList(box, sec, 0);
  const cats = sortedCategories();
  cats.forEach((cat, i) => {
    box.appendChild(h('div', { class: 'setting-row category-row', 'data-category': String(cat.id) },
      h('div', null, h('b', { text: cat.name.toUpperCase() }), h('div', { class: 'desc', text: t('{n} channel(s)', { n: channelsIn(cat.id).length }) })),
      h('div', { class: 'row wrap' },
        h('button', { class: 'icon-btn', title: t('Move category up'), icon: 'arrow-up', disabled: i === 0, onclick: () => moveCategory(cats, i, -1, sec) }),
        h('button', { class: 'icon-btn', title: t('Move category down'), icon: 'arrow-down', disabled: i === cats.length - 1, onclick: () => moveCategory(cats, i, 1, sec) }),
        h('button', { class: 'btn small', text: t('New channel'), onclick: () => newChannel('text', cat.id, sec) }),
        h('button', { class: 'btn small', text: t('Rename…'), onclick: async () => {
          const name = await promptModal(t('Rename category'), t('Name'), cat.name, { ok: t('Rename') });
          if (name) adminAct('rename_category', { id: cat.id, name }, sec);
        } }),
        h('button', { class: 'btn small', text: t('Permissions…'), 'aria-label': t('Permissions of {name}', { name: cat.name }), onclick: () => overridesDialog({ category: cat }, () => adminChannels(clear(sec))) }),
        h('button', { class: 'btn small danger', text: t('Delete'), onclick: async () => {
          if (await confirmModal(t('Delete category?'), t('Its channels stay, without a category.'), t('Delete'))) adminAct('delete_category', { id: cat.id }, sec);
        } }))));
    channelList(box, sec, cat.id);
  });
  sec.appendChild(formSection(null, box.childNodes.length ? [...box.childNodes] : emptyNote(t('No channels yet.'))));
}

function channelList(box, sec, categoryId) {
  const chans = channelsIn(categoryId);
  const voiceOn = adminUI.s.features.voice;
  chans.forEach((c, i) => {
    const notes = [];
    if (c.topic) notes.push(c.topic);
    if (c.slowmode) notes.push(t('slowmode {seconds}s', { seconds: c.slowmode }));
    if (c.category_id) notes.push(c.synced ? t('permissions synced') : t('own permissions'));
    notes.push(c.message_retention_days != null || c.file_retention_days != null
      ? t('retention: messages {messages}, files {files} days', { messages: c.message_retention_days ?? t('default'), files: c.file_retention_days ?? t('default') }) : t('community retention'));
    box.appendChild(h('div', { class: 'setting-row channel-row' + (categoryId ? ' indented' : '') + (adminUI.focus === c.id ? ' card' : ''), 'data-channel': String(c.id) },
      h('div', null, h('b', null, c.kind === 'voice' ? icon('volume-2', 'inline') : icon('hash', 'inline'), c.name), c.kind === 'voice' && !voiceOn ? h('span', { class: 'pill warn', text: t('voice off') }) : null,
        h('div', { class: 'desc', text: notes.join(' · ') })),
      h('div', { class: 'row wrap' },
        h('button', { class: 'icon-btn', title: t('Move up'), 'aria-label': t('Move {name} up', { name: c.name }), icon: 'arrow-up', disabled: i === 0, onclick: () => moveChannel(chans, i, -1, categoryId, sec) }),
        h('button', { class: 'icon-btn', title: t('Move down'), 'aria-label': t('Move {name} down', { name: c.name }), icon: 'arrow-down', disabled: i === chans.length - 1, onclick: () => moveChannel(chans, i, 1, categoryId, sec) }),
        h('button', { class: 'btn small', text: t('Edit…'), 'aria-label': t('Edit {name}', { name: c.name }), onclick: () => channelDialog(c, () => adminChannels(clear(sec))) }),
        h('button', { class: 'btn small', text: t('Permissions…'), 'aria-label': t('Permissions of {name}', { name: c.name }), onclick: () => overridesDialog({ channel: c }, () => adminChannels(clear(sec))) }),
        h('button', { class: 'btn small danger', text: t('Delete'), onclick: async () => {
          if (await confirmModal(t('Delete channel?'), t('#{name} and all its messages and files are deleted from the server.', { name: c.name }), t('Delete'))) adminAct('delete_channel', { id: c.id }, sec);
        } }))));
  });
}

async function newChannel(kind, categoryId, sec) {
  const name = await promptModal(kind === 'voice' ? t('New voice channel') : t('New text channel'), t('Name'), '', { ok: t('Create') });
  if (!name) return;
  adminAct('create_channel', { name, kind, category_id: categoryId }, sec);
}

function moveChannel(chans, i, dir, categoryId, sec) {
  const ids = chans.map((c) => c.id);
  [ids[i], ids[i + dir]] = [ids[i + dir], ids[i]];
  adminAct('reorder_channels', { category_id: categoryId, channel_ids: ids }, sec);
}

function moveCategory(cats, i, dir, sec) {
  const ids = cats.map((c) => c.id);
  [ids[i], ids[i + dir]] = [ids[i + dir], ids[i]];
  adminAct('reorder_categories', { category_ids: ids }, sec);
}

const TOGGLE_CHOICES = [['inherit', N_('Community setting')], ['off', N_('Off in this channel')]];

function channelDialog(c, after) {
  const feats = adminUI.s.features;
  const name = h('input', { type: 'text', value: c.name, 'aria-label': t('Channel name') });
  const topic = h('textarea', { rows: 2, value: c.topic || '', placeholder: t('What this channel is for'), 'aria-label': t('Channel topic') });
  const slowOpts = [[0, t('Off')], [5, t('5 seconds')], [10, t('10 seconds')], [30, t('30 seconds')], [60, t('1 minute')], [300, t('5 minutes')], [900, t('15 minutes')], [3600, t('1 hour')], [21600, t('6 hours')]];
  if (!slowOpts.some(([v]) => v === (c.slowmode || 0))) slowOpts.push([c.slowmode, t('{n} seconds', { n: c.slowmode })]);
  const slow = selectEl(slowOpts, String(c.slowmode || 0));
  slow.setAttribute('aria-label', t('Slowmode'));
  const cats = [[0, t('No category')], ...sortedCategories().map((x) => [x.id, x.name])];
  const cat = selectEl(cats, String(c.category_id || 0));
  cat.setAttribute('aria-label', t('Category'));
  const mr = h('input', { type: 'number', min: 0, value: c.message_retention_days ?? '', placeholder: t('community default'), 'aria-label': t('Keep messages (days)') });
  const fr = h('input', { type: 'number', min: 0, value: c.file_retention_days ?? '', placeholder: t('community default'), 'aria-label': t('Keep files (days)') });
  // Channel-level feature settings, only for features the community has on.
  const toggles = {};
  const toggleRows = [];
  for (const [k, label] of [['attachments', t('Attachments')], ['link_previews', t('Link previews')], ['reactions', t('Reactions')], ['history_for_new_members', t('New members see history')]]) {
    if (!feats[k]) continue;
    toggles[k] = selectEl(TOGGLE_CHOICES.map(([v, l]) => [v, t(l)]), c.settings[k]);
    toggles[k].setAttribute('aria-label', label);
    toggleRows.push(field(label, toggles[k]));
  }
  // Voice channels have a text chat too (everything but a topic).
  const text = c.kind === 'text';
  modal({ title: t('Edit {name}', { name: (text ? '#' : '') + c.name }), body: h('div', { class: 'stack channel-dialog' }, field(t('Name'), name),
    text ? field(t('Topic'), topic, t('Shown at the top of the channel.')) : null,
    field(t('Slowmode'), slow, text ? t('How long members wait between messages. Members who can manage messages or channels are exempt.')
      : t("How long members wait between messages in this voice channel's chat. Members who can manage messages or channels are exempt.")),
    field(t('Category'), cat, c.synced ? t('Permissions are synced with the category.') : ''),
    c.category_id && !c.synced ? h('button', { class: 'btn small', text: t('Sync permissions with the category'), onclick: async () => {
      try { await api('admin', adminUI.cid, 'edit_channel', { id: c.id, sync_permissions: true }); toast(t('Permissions synced.')); if (after) after(); } catch (e) { toastError(e); }
    } }) : null,
    h('div', { class: 'row' }, field(t('Keep messages (days)'), mr, t('Leave empty for the community setting. 0 keeps them forever.')), field(t('Keep files (days)'), fr, t('Leave empty for the community setting. 0 keeps them forever.'))),
    toggleRows.length ? h('div', { class: 'group-label', text: t('Features in this channel') }) : null, ...toggleRows),
  actions: [{ label: t('Cancel') }, { label: t('Save'), kind: 'primary', run: async () => {
    const p = { id: c.id, name: name.value, position: c.position, category_id: Number(cat.value) };
    if (text) p.topic = topic.value;
    p.slowmode = Number(slow.value);
    if (mr.value === '' && fr.value === '') p.clear_retention = true;
    if (mr.value !== '') p.message_retention_days = Number(mr.value);
    if (fr.value !== '') p.file_retention_days = Number(fr.value);
    for (const [k, sel] of Object.entries(toggles)) p[k] = sel.value;
    await api('admin', adminUI.cid, 'edit_channel', p);
    if (after) after();
  } }] });
}

// Overrides on a channel or a category, per role and per member: deny,
// inherit or allow for each channel permission. Only roles below the
// editor's highest role (and @everyone) and members below them can be
// changed, and only permissions the editor has (the server checks too).
async function overridesDialog(target, after) {
  const c = target.channel;
  const cat = target.category;
  const where = c ? `#${c.name}` : cat.name;
  const top = myTop();
  const can = adminUI.s.can;
  const perms = permCatalog().filter((p) => p.channel);
  const members = await api('members', adminUI.cid).catch(() => []);
  const memberName = (k) => (members.find((m) => m.key === k) || {}).display_name || k.slice(0, 10) + '…';
  const state = new Map();
  const rows = h('div', { class: 'stack' });
  const body = h('div', { class: 'stack overrides' },
    h('p', { class: 'small muted', text: c && c.category_id && c.synced ? t('This channel is synced with its category: changing it here gives it its own permissions.')
      : cat ? t('Synced channels in this category get these permissions too.') : t('Deny or allow on top of what roles normally grant. A member\'s own override beats their roles. When someone loses access, the channel gets a new key so they can\'t read what comes next.') }),
    rows);
  const addTarget = (tgt) => {
    const existing = (c ? c.overrides : cat.overrides).find((o) => (tgt.member ? o.member === tgt.member : !o.member && o.role_id === tgt.role_id));
    const o = { ...tgt, allow: new Set(existing ? existing.allow : []), deny: new Set(existing ? existing.deny : []), changed: false };
    const id = tgt.member ? 'm:' + tgt.member : 'r:' + tgt.role_id;
    if (state.has(id)) return;
    state.set(id, o);
    const role = tgt.role_id ? adminUI.s.roles.find((r) => r.id === tgt.role_id) : null;
    const editable = adminUI.s.is_owner || (role ? (role.builtin || role.position < top) : tgt.below);
    const grid = h('div', { class: 'perm-grid' });
    for (const p of perms) {
      const tri = h('div', { class: 'tri', 'data-perm': p.name });
      const set = (mode) => {
        o.changed = true;
        o.allow.delete(p.name); o.deny.delete(p.name);
        if (mode === 'allow') o.allow.add(p.name);
        if (mode === 'deny') o.deny.add(p.name);
        paint();
      };
      const locked = !editable || !can[p.name];
      const btns = { deny: h('button', { class: 'deny', icon: 'x', title: t('Deny'), disabled: locked, onclick: () => set('deny') }),
        inherit: h('button', { class: 'inherit', icon: 'slash', title: t('Inherit'), disabled: locked, onclick: () => set('inherit') }),
        allow: h('button', { class: 'allow', icon: 'check', title: t('Allow'), disabled: locked, onclick: () => set('allow') }) };
      const paint = () => {
        const mode = o.allow.has(p.name) ? 'allow' : o.deny.has(p.name) ? 'deny' : 'inherit';
        for (const [k, b] of Object.entries(btns)) { b.classList.toggle('sel', k === mode); b.setAttribute('aria-pressed', String(k === mode)); }
      };
      append(tri, [btns.deny, btns.inherit, btns.allow]);
      paint();
      grid.appendChild(h('div', { class: 'row' }, tri, h('span', { class: 'small', text: p.label })));
    }
    const label = tgt.member ? t('{name} (member)', { name: memberName(tgt.member) }) : role ? role.name : t('role');
    rows.appendChild(h('div', { class: 'card override', 'data-target': id }, h('b', { text: label }), editable ? null : h('span', { class: 'small muted', text: ' ' + t('(above you)') }), grid));
  };
  for (const r of adminUI.s.roles.slice().sort((a, b) => a.position - b.position)) addTarget({ role_id: r.id });
  const myRank = adminUI.s.is_owner ? Infinity : top;
  const rank = (m) => {
    if (m.owner) return Infinity;
    const pos = adminUI.s.roles.filter((r) => m.role_ids.includes(r.id)).map((r) => r.position);
    return pos.length ? Math.max(...pos) : 0;
  };
  for (const o of (c ? c.overrides : cat.overrides).filter((x) => x.member)) {
    const m = members.find((x) => x.key === o.member);
    addTarget({ member: o.member, below: m ? rank(m) < myRank : true });
  }
  const pickable = members.filter((m) => m.key !== adminUI.s.self.key && rank(m) < myRank);
  if (pickable.length) {
    const pick = selectEl(pickable.map((m) => [m.key, m.display_name]), pickable[0].key);
    pick.setAttribute('aria-label', t('Member to add'));
    body.appendChild(h('div', { class: 'row add-member-override' }, pick, h('button', { class: 'btn small', text: t('Add member override'), onclick: () => {
      const m = pickable.find((x) => x.key === pick.value);
      if (m) addTarget({ member: m.key, below: true });
    } })));
  }
  modal({ title: t('Permissions in {where}', { where }), body, actions: [{ label: t('Cancel') }, { label: t('Save'), kind: 'primary', run: async () => {
    for (const o of state.values()) {
      if (!o.changed) continue;
      const p = { allow: [...o.allow], deny: [...o.deny] };
      if (c) p.channel_id = c.id; else p.category_id = cat.id;
      if (o.member) p.member = o.member; else p.role_id = o.role_id;
      await api('admin', adminUI.cid, 'set_override', p);
    }
    if (after) after();
  } }] });
}

// ---------------------------------------------------------------- feature toggles

function adminFeatures(sec) {
  const s = adminUI.s.settings;
  const boxes = {};
  const body = [h('p', { class: 'small muted', text: t('A feature turned off here is unavailable everywhere in this community: the server refuses it and it disappears from the app. When it is on, each channel can still turn attachments, link previews, reactions and history for new members off, and permissions decide who may use it.') })];
  for (const [group, items] of FEATURE_TOGGLES) {
    body.push(h('h4', { text: t(group) }));
    for (const [k, label, desc] of items) {
      boxes[k] = checkbox(t(label), !!s.features[k], desc ? t(desc) : desc);
      boxes[k].input.setAttribute('data-feature', k);
      body.push(boxes[k]);
    }
  }
  const kinds = selectEl([['all', t('All kinds of files')], ['images_only', t('Images only (PNG, JPEG, GIF, WebP)')]], s.attachment_kinds);
  kinds.setAttribute('aria-label', t('Allowed attachments'));
  body.push(field(t('Allowed attachments'), kinds));
  const text = adminUI.s.channels.filter((c) => c.kind === 'text');
  const where = selectEl([['0', t('The first text channel')], ...text.map((c) => [String(c.id), '#' + c.name])], String(s.join_leave_channel || 0));
  where.setAttribute('aria-label', t('Join and leave messages channel'));
  body.push(field(t('Join and leave messages go to'), where));
  sec.appendChild(formSection(null, body, saveButton(() => ({
    features: Object.fromEntries(Object.entries(boxes).map(([k, b]) => [k, b.input.checked])),
    attachment_kinds: kinds.value, join_leave_channel: Number(where.value),
  }))));
}

function adminRetention(sec) {
  const s = adminUI.s.settings;
  const mr = h('input', { type: 'number', min: 0, max: 36500, value: s.message_retention_days });
  const fr = h('input', { type: 'number', min: 0, max: 36500, value: s.file_retention_days });
  sec.appendChild(formSection(null, [h('div', { class: 'row' }, field(t('Keep messages (days)'), mr, t('0 = forever')), field(t('Keep files (days)'), fr, t('0 = forever'))),
    h('p', { class: 'small muted', text: t('Channels can set shorter or longer limits under Channels. Members\' devices follow the same limits for what they cache. Search is under Features.') })],
  saveButton(() => ({ message_retention_days: Number(mr.value), file_retention_days: Number(fr.value) }))));
}

function adminFiles(sec) {
  const s = adminUI.s.settings;
  const size = h('input', { type: 'number', min: 1, max: 25, step: 1, value: Math.round(s.max_file_size / 1048576) });
  const bw = h('input', { type: 'number', min: 0, value: Math.round(s.file_bandwidth_limit / 1024) });
  sec.appendChild(formSection(null, [field(t('Maximum file size (MB)'), size, t('Up to 25 MB. New communities start at 10 MB.')),
    field(t('Download bandwidth limit (KB/s)'), bw, t('Protects a home connection when many members open the same file. 0 = no limit; otherwise at least 16.'))],
  saveButton(() => ({ max_file_size: Math.round(Number(size.value) * 1048576), file_bandwidth_limit: Math.round(Number(bw.value) * 1024) }))));
}

function adminNetwork(sec) {
  const s = adminUI.s.settings;
  const mode = selectEl([['automatic', t('Automatic (relays listed by the maintainer nodes)')], ['pinned', t('A pinned list')]], s.relay_mode);
  const list = h('textarea', { rows: 4, value: s.pinned_relays.join('\n'), placeholder: 'wss://relay.example.invalid/ws' }); // i18n: not shown (an example URL, the same in every language)
  sec.appendChild(formSection(null, [field(t('Relays'), mode, t('The server stays connected to these relays and tells members to use them. Automatic uses the relays the server was set up with.')),
    field(t('Pinned relays'), list, t('One ws:// or wss:// address per line, used when the list is pinned.'))],
  saveButton(() => ({ relay_mode: mode.value, pinned_relays: list.value }))));
}

function adminLimits(sec) {
  const s = adminUI.s.settings;
  const dev = h('input', { type: 'number', min: 1, max: 50, value: s.max_devices_per_member });
  sec.appendChild(formSection(null, [field(t('Maximum devices per member'), dev),
    h('p', { class: 'small muted', text: t('Member contact cards (DMs between members) are under Features.') })],
  saveButton(() => ({ max_devices_per_member: Number(dev.value) }))));
}

function joinPolicyOptions() {
  return [['invite_only', t('Invite only')], ['request', t('Request to join (approval queue)')], ['open', t('Open (anyone who finds it can join, no invite needed)')]];
}

const SINGLE_OWNER_WARNING = N_('If you lose your owner key, nobody can change the server key or move the community. Add a second owner.');

async function ownershipAction(sec, run, done) {
  try { await run(); if (done) toast(done); } catch (e) { toastError(e); }
  adminOwnership(clear(sec));
}

async function adminOwnership(sec) {
  await reloadAdmin();
  const s = adminUI.s;
  const owner = s.is_owner;
  const cid = adminUI.cid;
  sec.appendChild(sectionIntro(t('Owner keys vouch for the community\'s server, so members know they reach the real one. Any owner can change the server key, move the community to new hardware, and add or remove owners. Each owner\'s key stays on their own devices.')));
  if (owner && s.single_owner) sec.appendChild(h('div', { class: 'notice warn single-owner section-notice', text: t(SINGLE_OWNER_WARNING) }));

  if (s.self.owner_invited && !owner) {
    sec.appendChild(h('div', { class: 'card stack owner-invite' },
      h('div', { text: t('You\'ve been invited to become an owner.') }),
      h('div', { class: 'small muted', text: t('This device makes a new owner key and shares only the part others need to check it. You become an owner once an owner confirms it.') }),
      h('div', { class: 'row' },
        h('button', { class: 'btn small primary', text: t('Accept'), onclick: () => ownershipAction(sec, () => api('accept_owner_invite', cid), t('Owner key sent. An owner needs to confirm it.')) }),
        h('button', { class: 'btn small', text: t('Decline'), onclick: () => ownershipAction(sec, () => api('decline_owner_invite', cid), t('Invitation declined.')) }))));
  }

  const owners = [];
  for (const o of s.owners) {
    const pills = [o.founder ? h('span', { class: 'pill', text: t('founder') }) : null, o.me ? h('span', { class: 'pill', text: t('you') }) : null];
    owners.push(h('div', { class: 'setting-row owner-row' },
      h('div', null, h('b', { text: o.display_name }), ...pills, h('div', { class: 'desc mono', text: o.owner_key })),
      owner ? h('button', { class: 'btn small danger', text: o.me ? t('Remove me') : t('Remove'), disabled: s.single_owner,
        'aria-label': o.me ? t('Remove yourself as an owner') : t('Remove {name} as an owner', { name: o.display_name }),
        title: s.single_owner ? t('The last owner can\'t be removed.') : '',
        onclick: async () => {
          const text = o.me ? t('Your owner key stops working for this community. Another owner stays in charge.')
            : t("{name}'s owner key stops working for this community. They stay a member.", { name: o.display_name });
          if (await confirmModal(o.me ? t('Remove yourself as an owner?') : t('Remove {name} as an owner?', { name: o.display_name }), text, t('Remove'))) {
            ownershipAction(sec, () => api('remove_owner', cid, o.owner_key), t('Owner removed.'));
          }
        } }) : null));
  }
  if (owner) {
    for (const p of s.pending_owners) {
      owners.push(h('div', { class: 'setting-row pending-owner' },
        h('div', null, h('b', { text: p.display_name }),
          h('div', { class: 'desc', text: p.submitted ? t('Their owner key arrived. Confirm to add it.') : t('Invited. Waiting for their device to send an owner key.') })),
        h('div', { class: 'row' },
          p.submitted ? h('button', { class: 'btn small primary', text: t('Confirm'), onclick: () => ownershipAction(sec, () => api('confirm_owner', cid, p.member), t('{name} is now an owner.', { name: p.display_name })) }) : null,
          h('button', { class: 'btn small', text: t('Cancel'), onclick: () => ownershipAction(sec, () => api('cancel_owner_invite', cid, p.member), t('Invitation cancelled.')) }))));
    }
    const members = (await api('members', cid)).filter((m) => m.status === 'active' && !m.owner && !m.owner_invited);
    if (members.length) {
      const pick = selectEl(members.map((m) => [m.key, m.display_name]), members[0].key);
      owners.push(h('div', { class: 'setting-row add-owner' },
        h('div', null, h('div', { text: t('Add an owner') }), h('div', { class: 'desc', text: t('They accept on their device, then an owner confirms their key.') })),
        h('div', { class: 'row' }, pick, h('button', { class: 'btn small', text: t('Invite as owner'), onclick: () => ownershipAction(sec, () => api('invite_owner', cid, pick.value), t('Invitation sent.')) }))));
    }
  }
  sec.appendChild(formSection(t('Owners'), owners));

  const server = [];
  if (s.can.manage_community) {
    server.push(h('div', { class: 'setting-row' }, h('div', null, h('div', { text: t('Server backup') }), h('div', { class: 'desc', text: t('Saves an encrypted backup on the server (it also runs on a schedule).') })),
      h('button', { class: 'btn small', text: t('Back up now'), onclick: async () => { const r = await api('admin', cid, 'server_backup', {}); toast(t('Backup written on the server: {path}', { path: r.backup_path })); } })));
  }
  if (owner) {
    server.push(h('div', { class: 'setting-row' }, h('div', null, h('div', { text: t('Change server key') }), h('div', { class: 'desc', text: t('The server gets a new key, vouched for by your owner key. Members accept it by themselves.') })),
      h('button', { class: 'btn small', text: t('Change…'), onclick: async () => {
        if (await confirmModal(t('Change the server key?'), t('Members reconnect by themselves. Existing invites keep working.'), t('Change'), false)) {
          try { await api('rotate_server_key', cid); toast(t('Server key changed.')); } catch (e) { toastError(e); }
        }
      } })));
    server.push(h('div', { class: 'setting-row' }, h('div', null, h('div', { text: t('Move to new hardware') }),
      h('div', { class: 'desc', text: t('On the new machine run this command, then paste the move code it prints. Any owner can do this.') }),
      codeBlock('community restore -data DIR -backup FILE -storage-key KEY')), // i18n: not shown (a shell command, typed the same in every language)
    h('button', { class: 'btn small', text: t('Paste move code…'), onclick: async () => {
      const code = await promptModal(t('Move to new hardware'), t('Move code'), '', { multiline: true, ok: t('Adopt server') });
      if (!code) return;
      try { await api('adopt_server', cid, code); toast(t('The new server is now this community. Members follow automatically.')); } catch (e) { toastError(e); }
    } })));
    server.push(h('div', { class: 'notice', text: t('Keep your backup current: it holds your owner key.') }));
  }
  if (server.length) sec.appendChild(formSection(null, server));
}
