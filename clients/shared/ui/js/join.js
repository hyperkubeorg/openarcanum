// Adding a community: a page in the center section (docs/design.md
// "Pages": a title, an intro, boxed sections with their own footers) with
// four ways in: join with an invite, find a community, host one on this
// computer, or host one on your own server and claim it here. The page replaces the channel view until it's closed or the
// community it made opens.
'use strict';

const addUI = { el: null, prev: null, tab: 'join', select: null };

const ADD_WAYS = [
  { id: 'join', icon: 'log-in', label: N_('Join with an invite'), desc: N_('Someone sent you an invite link or code.') },
  { id: 'find', icon: 'search', label: N_('Find a community'), desc: N_('Search the public communities in the directory.') },
  { id: 'host', icon: 'monitor', label: N_('Host on this computer'), desc: N_('Start your own community, served by this app.') },
  { id: 'server', icon: 'monitor-up', label: N_('Host on your own server'), desc: N_('Run the community server on a computer that stays on, and claim it here.') },
];

// `link`: an invite link the OS opened the app with; it only prefills the field.
function openAddCommunity(tab = 'join', link = '') {
  if (st.view !== 'add') addUI.prev = { view: st.view };
  st.view = 'add';
  addUI.el = addPage(tab, link);
  render();
  const sel = $('.add-way.sel', addUI.el);
  if (sel) sel.focus();
  return addUI.el;
}

function closeAddCommunity() {
  addUI.el = null;
  st.view = addUI.prev && addUI.prev.view === 'dms' ? 'dms' : 'community';
  render();
}

// chat.js renderMainView: the page keeps its elements (and what was typed)
// across redraws of the main area.
function renderAddPage(main) {
  if (!addUI.el) addUI.el = addPage(addUI.tab);
  main.appendChild(addUI.el);
}

// Leaves the page for the community just joined or created.
async function leaveAddPageFor(cid) {
  addUI.el = null;
  await refreshCommunities();
  await selectCommunity(cid);
}

function addPage(tab, link = '') {
  // Without a directory on any maintainer node, there is nothing to find.
  // The web client (clients/web) can't run the community server program.
  const waysIn = ADD_WAYS.filter((w) => (w.id !== 'find' || directoryAvailable()) && (w.id !== 'host' || !window.openarcanumWeb));
  if (!waysIn.some((w) => w.id === tab)) tab = 'join';
  const titleId = 'add-page-title';
  const panel = h('div', { class: 'add-panel', role: 'tabpanel', id: 'add-panel' });
  const ways = h('div', { class: 'add-ways', role: 'tablist', 'aria-label': t('Ways to add a community') });
  const buttons = {};
  const select = (name, prefill = '') => {
    addUI.tab = name;
    for (const [id, b] of Object.entries(buttons)) {
      b.classList.toggle('sel', id === name);
      b.setAttribute('aria-selected', String(id === name));
      b.tabIndex = id === name ? 0 : -1;
    }
    panel.setAttribute('aria-labelledby', 'add-way-' + name);
    const pane = { join: joinPane, find: findPane, host: hostPane, server: serverPane }[name];
    clear(panel).appendChild(pane(prefill));
  };
  addUI.select = select;
  for (const w of waysIn) {
    const b = h('button', { class: 'tab add-way', role: 'tab', id: 'add-way-' + w.id, 'data-tab': w.id, 'aria-controls': 'add-panel',
      onclick: () => select(w.id) },
    h('span', { class: 'add-way-icon' }, icon(w.icon)),
    h('span', { class: 'add-way-text' }, h('b', { class: 'add-way-label', text: t(w.label) }), h('span', { class: 'add-way-desc', text: t(w.desc) })));
    buttons[w.id] = b;
    ways.appendChild(b);
  }
  // Arrow keys move between the four ways, as in any tab list.
  ways.addEventListener('keydown', (e) => {
    if (!['ArrowRight', 'ArrowLeft', 'ArrowDown', 'ArrowUp'].includes(e.key)) return;
    const ids = waysIn.map((w) => w.id);
    const i = ids.indexOf(addUI.tab);
    const next = ids[(i + (e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : ids.length - 1)) % ids.length];
    e.preventDefault();
    select(next);
    buttons[next].focus();
  });
  const page = h('div', { class: 'add-page', role: 'region', 'aria-labelledby': titleId },
    h('div', { class: 'add-page-head' },
      h('h1', { id: titleId, class: 'add-page-title', text: t('Add a community') }),
      h('div', { class: 'spacer' }),
      h('button', { class: 'icon-btn add-page-close', title: t('Close'), icon: 'x', onclick: closeAddCommunity })),
    h('div', { class: 'add-page-body' },
      sectionIntro(window.openarcanumWeb
        ? (directoryAvailable()
          ? t('Join a community someone invited you to, find a public one, or start your own on a server of yours that stays on.')
          : t('Join a community someone invited you to, or start your own on a server of yours that stays on.'))
        : (directoryAvailable()
          ? t('Join a community someone invited you to, find a public one, or start your own: served by this computer while OpenArcanum runs, or by a server of yours that stays on.')
          : t('Join a community someone invited you to, or start your own: served by this computer while OpenArcanum runs, or by a server of yours that stays on.'))),
      ways, panel));
  page.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !$('#modal-root .backdrop')) { e.preventDefault(); closeAddCommunity(); }
  });
  select(tab, tab === 'join' ? link : '');
  return page;
}

// ---------------------------------------------------------------- join with an invite

function joinPane(link = '') {
  const input = h('textarea', { rows: 3, placeholder: t('openarcanum-invite:… or openarcanum://invite/…') });
  input.value = link;
  const err = h('div', { class: 'error', role: 'alert' });
  const go = h('button', { class: 'btn primary', text: t('Continue'), onclick: async () => {
    err.textContent = '';
    const kind = await api('detect_code', input.value);
    if (kind === 'claim') { err.textContent = t('That is a claim code. Use "Host on your own server".'); return; }
    if (kind !== 'invite') { err.textContent = t("That doesn't look like an invite."); return; }
    go.disabled = true; go.textContent = t('Looking it up…');
    try { const pv = await api('preview', { invite: input.value }); joinFlow({ invite: input.value }, pv); }
    catch (e) { err.textContent = e.message; }
    finally { go.disabled = false; go.textContent = t('Continue'); }
  } });
  return formSection(t('Invite'), field(t('Invite'), input, t('Paste the invite someone sent you.')), formFoot(err, go));
}

// ---------------------------------------------------------------- find a community

// A directory result's server status (docs/protocol.md §7.4).
function statusPill(s) {
  if (!s) return null;
  if (s.state === 'online') return h('span', { class: 'pill ok', text: t('Online') });
  if (s.state === 'degraded') return h('span', { class: 'pill warn', text: t('Online, some relays down') });
  if (s.state === 'offline') {
    return h('span', { class: 'pill warn', title: s.last_seen ? t('Last seen {when}', { when: fmtDateTime(s.last_seen) }) : null,
      text: s.last_seen ? t('Offline · last seen {ago}', { ago: agoText(s.last_seen) }) : t('Offline') });
  }
  if (s.state === 'terminated') return h('span', { class: 'pill bad', text: t('Shut down') });
  return null;
}

function findPane() {
  const q = h('input', { type: 'search', placeholder: t('Search public communities by name, tag or description'), 'aria-label': t('Search') });
  const results = h('div', { class: 'stack add-results' });
  const run = async () => {
    clear(results).appendChild(h('div', { class: 'muted', text: t('Searching…') }));
    try {
      const tag = q.value.startsWith('#') ? q.value.slice(1) : '';
      const list = await api('directory_search', tag ? '' : q.value, tag);
      clear(results);
      if (!list.length) results.appendChild(emptyNote(t('No public communities match.')));
      for (const l of list) {
        results.appendChild(h('div', { class: 'result', onclick: async () => {
          try { const pv = await api('preview', { listing: l }); joinFlow({ listing: l }, pv); } catch (e) { toastError(e); }
        } },
        h('div', { class: 'row wrap' }, h('b', { text: l.name }), h('span', { class: 'pill', text: t('{range} members', { range: l.member_range }) }),
          h('span', { class: 'pill', text: policyLabel(l.join_policy) }), statusPill(l.status)),
        h('div', { class: 'small', text: l.description }),
        h('div', { class: 'row wrap' }, ...l.tags.map((tag) => h('span', { class: 'pill', text: '#' + tag })))));
      }
    } catch (e) { clear(results).appendChild(h('div', { class: 'error', text: e.message })); }
  };
  q.addEventListener('keydown', (e) => { if (e.key === 'Enter') runAndShow(); });
  const hint = h('div', { class: 'hint directory-source', text: t('Searches the maintainer nodes in Settings → Network. Private communities never appear here.') });
  const refresh = h('button', { class: 'btn small hidden', text: t('Refresh copy'), onclick: async () => {
    refresh.disabled = true;
    try { showSource(await api('directory_refresh')); await run(); } catch (e) { toastError(e); } finally { refresh.disabled = false; }
  } });
  // Where results come from: the maintainer nodes, or the copy on this device.
  const showSource = (st) => {
    refresh.classList.toggle('hidden', !st.local);
    if (!st.local) return;
    const age = st.age_seconds == null ? t('not downloaded yet') : st.age_seconds < 90 ? t('downloaded just now')
      : t('downloaded {minutes} min ago', { minutes: Math.round(st.age_seconds / 60) });
    hint.textContent = st.complete
      ? t("Searching on this device, in a copy of the directory ({count} communities, {age}). The maintainer nodes don't see what you search for.", { count: st.listings, age })
      : t("Searching on this device, in part of the directory ({count} communities, {age}; the whole directory is larger than this device keeps). The maintainer nodes don't see what you search for.", { count: st.listings, age });
  };
  const runAndShow = async () => { await run(); try { showSource(await api('directory_status')); } catch (e) { toastError(e); } };
  setTimeout(runAndShow, 0);
  return h('div', null,
    formSection(t('Search'), [h('div', { class: 'row' }, q, h('button', { class: 'btn', text: t('Search'), onclick: runAndShow })), hint, h('div', { class: 'row' }, refresh)]),
    results);
}

// An openarcanum://invite/ link the OS opened (openarcanum/instance.py) waits in
// Python until the profile is unlocked, then prefills the Join page.
async function openPendingInviteLink() {
  if ($('#app').classList.contains('hidden')) return;
  const link = await api('take_invite_link');
  if (link) openAddCommunity('join', link);
}
on('invite_link', () => { openPendingInviteLink().catch(toastError); });

function policyLabel(p) { return { open: t('Open'), request: t('Request to join'), invite_only: t('Invite only') }[p] || p; }

// The rest of joining, in the page: preview, choose an identity, submit,
// the result, a backup reminder.
function joinFlow(source, pv) {
  if (pv.already) { toast(t("You're already in {community}.", { community: pv.name })); leaveAddPageFor(pv.community).catch(toastError); return; }
  const panel = addUI.el ? $('.add-panel', addUI.el) : null;
  if (!panel) return;
  const back = addUI.tab;
  const name = h('input', { type: 'text', placeholder: t('How others will see you here') });
  const choices = h('div', { class: 'stack', role: 'radiogroup', 'aria-label': t('Identity') });
  const newRadio = h('input', { type: 'radio', name: 'ident', value: 'new', checked: pv.default_identity !== 'reuse' || !pv.identities.length });
  choices.appendChild(h('label', { class: 'check' }, newRadio, h('div', null, h('b', { text: t('A new identity (recommended)') }),
    h('div', { class: 'hint', text: t("Nobody can connect this community to any other you're in.") }))));
  pv.identities.forEach((idn, i) => {
    const r = h('input', { type: 'radio', name: 'ident', value: idn.id, checked: pv.default_identity === 'reuse' && i === 0 });
    choices.appendChild(h('label', { class: 'check' }, r, h('div', null, h('b', { text: t('Reuse {identity}', { identity: idn.label }) }),
      h('div', { class: 'hint', text: idn.communities.length ? t('These communities could link you: {communities}', { communities: idn.communities.join(', ') }) : t('Not used in any community right now.') }))));
  });
  const err = h('div', { class: 'error', role: 'alert' });
  const join = h('button', { class: 'btn primary', text: t('Join'), onclick: async () => {
    err.textContent = '';
    const ident = $('input[name=ident]:checked', panel).value;
    join.disabled = true;
    try {
      const r = await api('join', source, ident, name.value);
      if (r.status === 'refused') { err.textContent = r.reason; return; }
      await leaveAddPageFor(r.community);
      if (r.status === 'pending') toast(t('Request sent. You can read the community once an admin approves you.'), { title: t('Pending approval'), ms: 8000 });
      backupReminder();
    } catch (e) { err.textContent = e.message; }
    finally { join.disabled = false; }
  } });
  clear(panel).append(
    formSection(t('Join {community}', { community: pv.name }), [
      h('div', { class: 'card' },
        h('div', { class: 'row wrap' }, h('b', { class: 'card-title', text: pv.name }), h('div', { class: 'spacer' }),
          h('span', { class: 'pill', text: t('{range} members', { range: pv.member_range }) }), h('span', { class: 'pill', text: policyLabel(pv.join_policy) })),
        pv.description ? h('p', { text: pv.description }) : null,
        pv.tags.length ? h('div', { class: 'row wrap' }, ...pv.tags.map((tag) => h('span', { class: 'pill', text: '#' + tag }))) : null),
      h('div', { class: 'notice', text: pv.notice }),
      pv.join_policy === 'open' && !pv.has_key ? h('div', { class: 'hint open-join', text: t('This community is open: anyone who finds it can join, no invite needed.') }) : null,
      pv.join_policy === 'request' && !pv.has_key ? h('div', { class: 'hint', text: t('An admin has to approve your request before you can read anything.') }) : null,
      pv.join_policy === 'invite_only' && !pv.has_key ? h('div', { class: 'error', text: t('This community needs an invite; you can still ask, but the server will refuse without one.') }) : null,
      h('h4', { text: t('Identity') }), choices,
      field(t('Display name in this community'), name)],
    formFoot(err, h('button', { class: 'btn', text: t('Back'), onclick: () => addUI.select(back) }), join)));
  name.focus();
}

function backupReminder(force = false) {
  api('backup_status').then((b) => {
    if (b.current && !force) return;
    modal({ title: t('Back up your keys'), body: h('div', { class: 'stack' },
      h('p', { text: t('Your backup is out of date. A backup is the only way to get your keys back if you lose every device.') }),
      h('p', { class: 'muted small', text: t('It holds your keys and community list (no messages), encrypted with a passphrase you choose.') })),
      actions: [{ label: t('Later') }, { label: t('Export backup…'), kind: 'primary', run: () => exportBackupFlow() }] });
  }).catch(() => {});
}

async function exportBackupFlow() {
  const again = h('input', { type: 'password', autocomplete: 'new-password' });
  const pass = passphraseField(t('Backup passphrase'), { generate: true, repeat: again,
    saveNote: t('Save this passphrase in your password manager now, apart from the backup file. Without it, nobody can open the backup.') });
  const current = h('input', { type: 'password', autocomplete: 'current-password' });
  const err = h('div', { class: 'error' });
  let dir = '';
  try { dir = await api('downloads_dir'); } catch (e) { dir = ''; }
  const path = h('input', { type: 'text', value: dir ? `${dir}/openarcanum-backup.fbak` : 'openarcanum-backup.fbak', 'aria-label': t('Save to') });
  modal({ title: t('Export backup'), body: h('div', { class: 'stack' },
    h('p', { class: 'small muted', text: t('Use a different passphrase from your profile, and keep the file somewhere safe (not only on this device).') }),
    pass, field(t('Repeat backup passphrase'), again),
    field(t('Save to'), path, t('A full path, or a folder.')),
    h('button', { class: 'btn small', text: t('Browse…'), onclick: async () => { const p = await pickSave('backup', 'openarcanum-backup.fbak'); if (p) path.value = p; } }),
    field(t('Your passphrase'), current, t('The passphrase of your profile, to confirm it is you.')),
    err),
  actions: [{ label: t('Cancel') }, { label: t('Save backup'), kind: 'primary', run: async () => {
    err.textContent = '';
    if (pass.input.value !== again.value) { err.textContent = t("The passphrases don't match."); return false; }
    if (!path.value.trim()) { err.textContent = t('Choose where to save the backup.'); return false; }
    if (!current.value) { err.textContent = t('Enter your passphrase to confirm.'); return false; }
    try {
      const target = await pickSave('backup', 'openarcanum-backup.fbak', path.value);
      if (!target) return false;
      await api('export_backup', target, pass.input.value, current.value); toast(t('Backup saved.')); refreshStatus();
    }
    catch (e) { err.textContent = e.message; return false; }
  } }] });
}

// ---------------------------------------------------------------- creating: the community's settings

// The new community's settings (both ways of hosting): name, description,
// tags, who can find and join it, retention, largest file, and the owner's
// display name. `submit(settings, displayName)` runs on the footer button.
function setupForm(title, intro, submitLabel, submit, opts = {}) {
  const name = h('input', { type: 'text', placeholder: t('Community name') });
  const desc = h('textarea', { rows: 2 });
  const tags = h('input', { type: 'text', placeholder: t('e.g. gardening, local') });
  const listing = directoryAvailable();
  const vis = selectEl(listing ? [['private', t('Private (invite links only)')], ['public', t('Public (listed in the directory)')]]
    : [['private', t('Private (invite links only)')]], 'private');
  const policy = selectEl(joinPolicyOptions(), 'invite_only');
  const reach = h('p', { class: 'small muted reach-now', text: reachSummary(vis.value, policy.value) });
  for (const el of [vis, policy]) el.addEventListener('change', () => { reach.textContent = reachSummary(vis.value, policy.value); });
  const mret = h('input', { type: 'number', min: 0, max: 36500, value: 30 });
  const fret = h('input', { type: 'number', min: 0, max: 36500, value: 7 });
  const fsize = h('input', { type: 'number', min: 1, max: 25, step: 1, value: 10 });
  const me = h('input', { type: 'text', placeholder: t('Your display name') });
  const err = h('div', { class: 'error', role: 'alert' });
  const busy = h('div', { class: 'muted small', role: 'status' });
  const go = h('button', { class: 'btn primary', text: submitLabel, disabled: !!opts.disabled, onclick: async () => {
    err.textContent = '';
    go.disabled = true;
    busy.textContent = opts.busy || '';
    try {
      await submit({
        name: name.value, description: desc.value, tags: tags.value, visibility: vis.value, join_policy: policy.value,
        message_retention_days: Number(mret.value), file_retention_days: Number(fret.value),
        max_file_size: Math.round(Number(fsize.value) * 1048576),
      }, me.value);
    } catch (e) { err.textContent = e.message; }
    finally { go.disabled = false; busy.textContent = ''; }
  } });
  return formSection(title, [
    intro ? sectionIntro(intro) : null,
    field(t('Name'), name), field(t('Description'), desc), field(t('Tags'), tags, t('Comma-separated; used by directory search.')),
    h('div', { class: 'row' }, field(t('Visibility'), vis), field(t('Join policy'), policy)), reach,
    listing ? null : noDirectoryNote(),
    h('div', { class: 'row' }, field(t('Keep messages (days)'), mret, t('0 = forever')), field(t('Keep files (days)'), fret, t('0 = forever'))),
    field(t('Largest file (MB)'), fsize, t('Up to 25 MB. You can change it later under Community settings → Files.')),
    field(t('Your display name'), me)],
  formFoot(err, busy, opts.back ? h('button', { class: 'btn', text: t('Back'), onclick: opts.back }) : null, go));
}

async function created(r) {
  await leaveAddPageFor(r.community);
  claimedDialog(r);
}

// ---------------------------------------------------------------- host on this computer

function hostPane() {
  const box = h('div');
  const intro = h('div', null,
    formSection(t('Your computer serves the community'), [
      h('p', { text: t('OpenArcanum runs the community server on this computer, in the background, for as long as the app runs: also while its window is closed to the tray, and before you unlock it. Its data stays in your profile\'s data folder (Settings → Data & storage).') }),
      h('ul', { class: 'add-facts' },
        h('li', { text: t('Members connect through relays, so there\'s nothing to set up on your router.') }),
        h('li', { text: t('While this computer is off or OpenArcanum isn\'t running, members see the community as offline. After 7 days offline, the maintainer nodes shut it down.') }),
        h('li', { text: t('For a community that should always be up, host it on your own server instead.') }))]));
  box.appendChild(intro);
  api('community_binary_info').then((info) => {
    if (!info.available) {
      box.appendChild(h('div', { class: 'notice host-missing', text: t("This copy of OpenArcanum doesn't include the community server program, so it can't host communities. Host on your own server instead.") }));
    }
    box.appendChild(setupForm(t('Set up your community'), '', t('Create community'), async (settings, me) => {
      const r = await api('host_community', settings, me);
      await created(r);
    }, { disabled: !info.available, busy: t('Starting the community server and setting it up…') }));
  }).catch(toastError);
  return box;
}

// ---------------------------------------------------------------- host on your own server

const CLAIM_COMMANDS = 'community init -data ./my-community -relay wss://relay-01.openarcanum.org/ws\ncommunity run -data ./my-community'; // i18n: not shown (shell commands, typed the same in every language)

// Where the community server is downloaded from, for the web client, which
// has no copy of the program to save.
const SERVER_RELEASES_URL = 'https://github.com/hyperkubeorg/openarcanum/releases/latest'; // i18n: not shown (a URL)

function serverDownloadSection() {
  return formSection(t('1. Get the community server'), [
    h('p', { text: t('Download the server release for the computer that will run your community (a Linux server or a computer that stays on): openarcanum-servers for amd64 or arm64. It contains the community program.') }),
    h('p', {}, h('a', { href: SERVER_RELEASES_URL, target: '_blank', rel: 'noopener noreferrer', text: t('Open the releases page') }))]);
}

function serverPane() {
  const box = h('div');
  // 1. The program: the copy inside this app, for this computer's system.
  const path = h('input', { type: 'text', 'aria-label': t('Save to') });
  const saveErr = h('div', { class: 'error', role: 'alert' });
  const saveBtn = h('button', { class: 'btn', text: t('Save the community server'), onclick: async () => {
    saveErr.textContent = '';
    saveBtn.disabled = true;
    try {
      const target = await pickSave('any', path.dataset.name || 'community', path.value); // i18n: not shown (a file name)
      if (target) { const where = await api('save_community_binary', target); toast(t('Saved to {path}.', { path: where })); }
    }
    catch (e) { saveErr.textContent = e.message; }
    finally { saveBtn.disabled = false; }
  } });
  const browse = h('button', { class: 'btn', text: t('Browse…'), onclick: async () => {
    const p = await pickSave('any', path.dataset.name || 'community'); // i18n: not shown (a file name)
    if (p) path.value = p;
  } });
  const getIt = window.openarcanumWeb ? serverDownloadSection() : formSection(t('1. Copy the community server'), [
    h('p', { class: 'binary-about', text: t('The same program this app uses to host communities.') }),
    field(t('Save to'), path, t('A full path, or a folder. Copy it to the computer that will run your community.'))],
  formFoot(saveErr, browse, saveBtn));
  if (!window.openarcanumWeb) api('community_binary_info').then(async (info) => {
    path.dataset.name = info.name;
    $('.binary-about', getIt).textContent = info.available
      ? t('The same program this app uses to host communities, for {platform}. For another system, use the server release for it (docs/admin-guide.md "Installing the servers").', { platform: info.platform })
      : t("This copy of OpenArcanum doesn't include the community server program. Use the server release for your system (docs/admin-guide.md \"Installing the servers\").");
    saveBtn.disabled = browse.disabled = !info.available;
    try { const dir = await api('downloads_dir'); path.value = dir ? `${dir}/${info.name}` : info.name; } catch (e) { path.value = info.name; }
  }).catch(toastError);
  // 2. Set it up there.
  const setUp = formSection(t('2. Set it up and start it'), [
    h('p', { class: 'small', text: t('On the server, make the program executable and run these two commands. `community init` prints a one-time claim code; `community run` keeps the community up (as a service, see the admin guide). It only connects out: no port forwarding and no open ports.') }),
    codeBlock(CLAIM_COMMANDS, { cls: 'claim-commands', copyAria: t('Copy the commands') })]);
  // 3. Claim it here.
  const code = h('textarea', { rows: 3, placeholder: 'openarcanum-claim:…' }); // i18n: not shown (the claim code's literal prefix, the same in every language)
  const err = h('div', { class: 'error', role: 'alert' });
  const claimIt = formSection(t('3. Claim it'), field(t('Claim code'), code, t('`community init` prints it once. Whoever claims the server owns the community.')),
    formFoot(err, h('button', { class: 'btn primary', text: t('Next'), onclick: async () => {
      err.textContent = '';
      if ((await api('detect_code', code.value)) !== 'claim') { err.textContent = t("That doesn't look like a claim code."); return; }
      const claimCode = code.value;
      clear(box).appendChild(setupForm(t('Set up your community'), '', t('Claim and create'), async (settings, me) => {
        const r = await api('claim', claimCode, settings, me);
        await created(r);
      }, { back: () => addUI.select('server') }));
    } })));
  box.append(getIt, setUp, claimIt);
  return box;
}

function claimedDialog(r) {
  modal({ title: t('Your community is ready'), dismissable: false, body: h('div', { class: 'stack' },
    h('p', { text: t('Share this invite (it works for 7 days; make more in Community settings → Invites & join policy):') }),
    codeBlock(r.invite, { cls: 'invite-code', copyLabel: t('Copy invite') }),
    r.hosted ? h('p', { class: 'small muted', text: t('This computer serves the community while OpenArcanum runs, also from the tray. The community menu shows whether it is running.') }) : null,
    h('div', { class: 'notice', text: t("You hold the community's owner key. Export a backup now, and add a second owner in Community settings → Ownership: if the only owner key is lost, nobody can change the server key or move the community.") })),
  actions: [{ label: t('Export backup…'), kind: 'primary', run: () => exportBackupFlow() }] });
}
