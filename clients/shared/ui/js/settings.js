// User settings (listed in docs/settings.md). Every control saves through
// Python, which validates it; errors show next to the control.
'use strict';

const settingsUI = {
  data: null,
  closers: [],
  // Run fn when the Settings page closes (stop a mic test, drop media access).
  onClose(fn) { this.closers.push(fn); },
};

// A labelled control row that saves one setting.
let settingIds = 0;
function settingRow(label, desc, control, save) {
  // The row's label names the control for screen readers, and its
  // description describes it.
  if (control && control.setAttribute && !control.getAttribute('aria-label')) control.setAttribute('aria-label', label);
  const descId = `setting-desc-${++settingIds}`;
  if (desc && control && control.setAttribute) control.setAttribute('aria-describedby', descId);
  const status = h('span', { class: 'saved' });
  const err = h('div', { class: 'error' });
  const run = async () => {
    err.textContent = ''; status.textContent = '';
    try { await save(); status.textContent = t('Saved'); setTimeout(() => (status.textContent = ''), 1500); }
    catch (e) { err.textContent = e.message; }
  };
  // A control made of several (the sensitivity slider and its meter) saves
  // when the input inside it changes: the change event bubbles to it.
  control.addEventListener('change', run);
  return h('div', { class: 'setting-row' }, h('div', null, h('div', { text: label }), desc ? h('div', { class: 'desc', id: descId, text: desc }) : null, err), h('div', { class: 'row' }, status, control));
}

function vaultSetting(key, label, desc, control, read = (c) => c.value) {
  return settingRow(label, desc, control, async () => {
    const v = await api('set_setting', key, read(control));
    settingsUI.data.vault[key] = v.value;
    if (control.type !== 'checkbox' && control.tagName !== 'SELECT' && typeof v.value !== 'object') control.value = v.value;
  });
}

function toggle(checked) { return h('input', { type: 'checkbox', checked }); }

async function openSettings(initial) {
  settingsUI.data = await api('get_settings');
  // Sounds follow what's chosen here at once (sounds.js).
  soundsUI.vault = settingsUI.data.vault;
  const sections = [
    { group: t('Account'), id: 'security', label: t('Security'), render: renderSecurity },
    { group: t('Account'), id: 'identity', label: t('Identity'), render: renderIdentity },
    { group: t('Account'), id: 'communities', label: t('Communities'), render: renderCommunitiesSettings },
    { group: t('Account'), id: 'devices', label: t('Devices'), render: renderDevicesSettings },
    { group: t('Account'), id: 'privacy', label: t('Privacy & DMs'), render: renderPrivacy },
    { group: t('App'), id: 'appearance', label: t('Appearance'), render: renderAppearance },
    { group: t('App'), id: 'notifications', label: t('Notifications'), render: renderNotifications },
    { group: t('App'), id: 'voice', label: t('Voice & video'), render: renderVoiceSettings },
    { group: t('App'), id: 'network', label: t('Network'), render: renderNetwork },
    { group: t('App'), id: 'data', label: t('Data & storage'), render: renderData },
    { group: t('App'), id: 'updates', label: t('Updates'), render: renderUpdates },
    { group: t('App'), id: 'about', label: t('About'), render: renderAbout },
  ];
  return showPage({ title: t('Settings'), icon: 'settings', cls: 'settings', nav: sections, initial, onClose: () => {
    for (const fn of settingsUI.closers.splice(0)) { try { fn(); } catch (e) { console.error(e); } }
    refreshCommunities().then(render).catch(() => {});
  } });
}

async function renderSecurity(sec) {
  const d = settingsUI.data;
  const b = d.backup;
  sec.appendChild(formSection(t('Passphrase'), h('div', { class: 'muted small', text: t('Unlocks this device only. Each device has its own.') }),
    formFoot(h('button', { class: 'btn', text: t('Change passphrase…'), onclick: changePassphraseDialog }))));
  sec.appendChild(await signInSection());
  const lock = selectEl([[1, t('1 minute')], [5, t('5 minutes')], [15, t('15 minutes')], [30, t('30 minutes')], [60, t('1 hour')], [240, t('4 hours')], [0, t('Never')]], String(d.vault.auto_lock_minutes));
  if (![...lock.options].some((o) => o.value === String(d.vault.auto_lock_minutes))) lock.appendChild(h('option', { value: d.vault.auto_lock_minutes, text: t('{n} minutes', { n: d.vault.auto_lock_minutes }) }));
  lock.value = String(d.vault.auto_lock_minutes);
  sec.appendChild(formSection(t('Auto-lock'),
    vaultSetting('auto_lock_minutes', t('Lock after idle'), t('Locking hides everything and clears your keys from memory. Messages keep arriving. Your passphrase unlocks it, also after a restart.'), lock, (c) => Number(c.value)),
    formFoot(h('button', { class: 'btn', text: t('Lock now'), onclick: () => { leavePage(); lockNow(); } }))));
  sec.appendChild(formSection(t('Backup'), [
    h('div', { class: 'row' }, h('span', { class: 'pill ' + (b.current ? 'ok' : 'warn'), text: b.current ? t('Up to date') : t('Out of date') }),
      h('span', { class: 'muted small', text: b.exported_at ? t('Last exported {time}', { time: fmtDateTime(b.exported_at * 1000) }) : t('Never exported') })),
    h('div', { class: 'small muted', text: t('Holds your keys and what\'s needed to reach your {n} communities. No message history.', { n: b.communities }) })],
  formFoot(h('button', { class: 'btn primary', text: t('Export backup…'), onclick: exportBackupFlow }))));
}

// Signing in: staying signed in on this device is the default after setup;
// "Ask for my passphrase when OpenArcanum starts" turns it off. The text says
// where the key is kept and who could then open the profile.
async function signInSection() {
  const holder = h('div', { class: 'sign-in-section' });
  const paint = (rem) => {
    const ask = !rem.enabled;
    const box = h('input', { type: 'checkbox', checked: ask, 'aria-label': t('Ask for my passphrase when OpenArcanum starts') });
    let desc;
    if (ask) desc = t('OpenArcanum asks for your passphrase every time it starts. No key is kept on this device.');
    else if (rem.store === 'keychain') desc = t("OpenArcanum opens without your passphrase on this device: the key that opens your profile is kept in this computer's keychain (Windows Credential Manager, the macOS Keychain, or your desktop's keyring). Anyone who can use your account on this computer can open your profile.");
    else desc = t("OpenArcanum opens without your passphrase on this device. This system has no keychain, so the key that opens your profile is kept in a file only your user account can read: {file}", { file: rem.file });
    const warning = !ask && rem.store === 'file'
      ? h('div', { class: 'notice warn key-file-notice', role: 'note', text: t("On this system your profile opens without a passphrase for anyone who can read your user's files (another administrator, a backup of your home folder, malware running as you). Turn on \"Ask for my passphrase\" if that matters to you.") })
      : null;
    const row = settingRow(t('Ask for my passphrase when OpenArcanum starts'), desc, box, async () => {
      // Either way the passphrase confirms it: asking again also re-keys
      // the profile file, so a copy of the kept key stops working.
      const nowAsk = box.checked;
      const pass = h('input', { type: 'password', autocomplete: 'current-password', 'aria-label': t('Passphrase') });
      const next = await new Promise((resolve) => modal({
        title: nowAsk ? t('Ask for your passphrase') : t('Stay signed in on this device'),
        body: h('div', { class: 'stack' }, h('p', { class: 'small muted', text: nowAsk
          ? t('Enter your passphrase to confirm. The key kept on this device is deleted and your profile file is re-encrypted, so a copy of the old key no longer opens it.')
          : (rem.available
            ? t("Enter your passphrase to confirm. The key is kept in this computer's keychain.")
            : t("Enter your passphrase to confirm. This system has no keychain: the key is kept in a file, and anyone who can read your user's files could open your profile.")) }),
        field(t('Passphrase'), pass)),
        actions: [{ label: t('Cancel'), run: () => resolve(null) }, { label: nowAsk ? t('Ask for it') : t('Stay signed in'), kind: 'primary', run: async () => {
          try { resolve(await api('set_remember', !nowAsk, pass.value)); } catch (e) { toastError(e); return false; }
        } }],
        onclose: () => resolve(null),
      }));
      // Cancelled: back as it was, without a "Saved" (an empty error shows nothing).
      if (!next) { box.checked = !nowAsk; throw new Error(''); }
      paint(next);
    });
    clear(holder).appendChild(formSection(t('Signing in'), [row, warning].filter(Boolean)));
  };
  paint(await api('remember_status'));
  return holder;
}

function changePassphraseDialog() {
  const old = h('input', { type: 'password', autocomplete: 'current-password' });
  const again = h('input', { type: 'password', autocomplete: 'new-password' });
  const next = passphraseField(t('New passphrase'), { generate: true, repeat: again });
  const err = h('div', { class: 'error' });
  modal({ title: t('Change passphrase'), body: h('div', { class: 'stack' }, field(t('Current passphrase'), old), next, field(t('Repeat new passphrase'), again), err),
    actions: [{ label: t('Cancel') }, { label: t('Change'), kind: 'primary', run: async () => {
      err.textContent = '';
      if (next.input.value !== again.value) { err.textContent = t("The new passphrases don't match."); return false; }
      try { await api('change_passphrase', old.value, next.input.value); toast(t('Passphrase changed.')); }
      catch (e) { err.textContent = e.message; return false; }
    } }] });
}

async function renderIdentity(sec) {
  const d = settingsUI.data;
  const display = h('input', { type: 'text', maxlength: 64, value: d.vault.display_name || '', placeholder: t('Optional') });
  sec.appendChild(formSection(null, settingRow(t('Your name'),
    t('Shown in direct messages and in your contact code. A community you join starts with this name, and you can use another one there.'),
    display, async () => {
      const v = await api('set_setting', 'display_name', display.value);
      d.vault.display_name = display.value = v.value;
      if (st.status) { st.status.display_name = v.value; render(); }
    })));
  const mode = selectEl([['separate', t('A new key for each community (recommended)')], ['reuse', t('Reuse an existing identity')]], d.vault.default_identity);
  sec.appendChild(formSection(null, vaultSetting('default_identity', t('Default when joining'), t("Separate keys mean communities can't tell that two memberships are the same person. You can still choose each time you join."), mode)));
  const list = await api('list_communities');
  const rows = list.filter((x) => x.status === 'active').map((c) => h('div', { class: 'setting-row' }, h('div', null, h('div', { text: c.name }), h('div', { class: 'desc', text: t('Shown as “{name}”', { name: c.display_name }) })),
    h('button', { class: 'btn small', text: t('Edit…'), onclick: () => communityProfileDialog(c) })));
  sec.appendChild(formSection(t('Your name and avatar in each community'), rows.length ? rows : emptyNote(t('No communities yet.'))));
}

async function renderCommunitiesSettings(sec) {
  const list = await api('list_communities');
  if (!list.length) { sec.appendChild(emptyNote(t('No communities yet.'))); return; }
  const table = h('table', { class: 'list' }, h('tr', null, h('th', { text: t('Community') }), h('th', { text: t('Status') }), h('th', { text: '' })));
  for (const c of list) {
    const active = c.status === 'active' || c.status === 'pending';
    table.appendChild(h('tr', null, h('td', null, h('b', { text: c.name }), h('div', { class: 'small muted', text: c.relay || '' })),
      h('td', null, h('span', { class: 'pill', text: connectionLabel(c) })),
      h('td', null, h('div', { class: 'row wrap' },
        h('button', { class: 'btn small', text: t('Delete local data'), title: t("Delete this device's copy of messages and files"), onclick: async () => {
          if (await confirmModal(t('Delete local data?'), t('Messages and files from {community} cached on this device are deleted. They download again from the server for as long as it keeps them.', { community: c.name }), t('Delete'))) {
            await api('clear_community_cache', c.id); toast(t('Local data deleted.'));
          }
        } }),
        active ? h('button', { class: 'btn small danger', text: t('Leave…'), onclick: () => leaveDialog(c) })
          : h('button', { class: 'btn small', text: t('Remove from list'), onclick: async () => { await api('dismiss', c.id); sec.parentElement && renderCommunitiesSettings(clear(sec)); } })))));
  }
  sec.appendChild(table);
}

async function renderAppearance(sec) {
  const p = settingsUI.data.prefs;
  const theme = selectEl([['dark', t('Dark')], ['light', t('Light')], ['system', t('Match the system')]], p.theme);
  const size = selectEl([['small', t('Small')], ['medium', t('Medium')], ['large', t('Large')], ['larger', t('Larger')]], p.text_size);
  const compact = toggle(p.compact);
  const pref = (key, label, desc, c, read = (x) => x.value) => settingRow(label, desc, c, async () => { const v = await api('set_setting', key, read(c)); p[key] = v.value; });
  sec.appendChild(sectionIntro(t('Appearance is saved unencrypted so the lock screen can use it; it says nothing about you.')));
  sec.appendChild(formSection(null, [
    pref('theme', t('Theme'), t("Match the system follows your desktop's light or dark setting when the desktop shares it with apps; otherwise it shows the light theme. It also applies to the screens before you unlock."), theme),
    pref('text_size', t('Text size'), null, size),
    pref('compact', t('Compact mode'), t('Tighter spacing in messages and lists.'), compact, (c) => c.checked)]));
  // Closing the window (openarcanum/tray.py, app.py): only where there is a tray.
  const tray = await api('tray_status');
  const keep = h('input', { type: 'checkbox', checked: tray.available && p.close_to_tray, disabled: !tray.available });
  sec.appendChild(formSection(t('Window'), pref('close_to_tray', t('Keep running in the tray when the window is closed'),
    tray.available
      ? t('Closing the window leaves OpenArcanum running in the system tray, so messages and calls still reach you. Quit from the tray icon\'s menu or with {keys}.', { keys: quitKeys() })
      : tray.reason,
    keep, (c) => c.checked)));
}

function quitKeys() { return /Mac/.test(navigator.platform) ? 'Cmd+Q' : 'Ctrl+Q'; } // i18n: not shown (key names)

async function renderNotifications(sec) {
  const v = settingsUI.data.vault;
  sec.appendChild(sectionIntro(t('New messages and reports show as badges in the window. These settings are for desktop notifications and for the sounds the app plays.')));
  sec.appendChild(formSection(null, [
    vaultSetting('notifications', t('Desktop notifications'), t('While locked, notifications never show message content.'), toggle(v.notifications), (c) => c.checked),
    vaultSetting('notify_mode', t('Notify me about'), t('Mentions are @your-name or @everyone. Mentions still notify in muted channels.'),
      selectEl([['all', t('All messages')], ['mentions', t('Mentions only')]], v.notify_mode))]));
  const list = await api('list_communities');
  const active = list.filter((c) => c.status === 'active' || c.status === 'pending');
  if (active.length) {
    sec.appendChild(formSection(t('Each community'), active.map((c) => {
      // Also in the right-click menu of the community's icon.
      const level = selectEl([['', t('Default (as above)')], ...NOTIFY_LEVELS.map(([value, label]) => [value, t(label)])], c.notify || '');
      level.setAttribute('data-community', c.id);
      return settingRow(c.name, t('Notify me about'), level, async () => {
        const r = await api('set_community_notify', c.id, level.value); settingsUI.data.vault.community_notify = r.value;
      });
    })));
  }
  const mutedChannels = v.muted_channels || [];
  const muted = list.map((c) => {
    const cb = toggle(c.muted);
    return settingRow(c.name, t('Mute the whole community'), cb, async () => {
      const r = await api('toggle_mute', c.id, null); settingsUI.data.vault.muted_communities = r.value;
    });
  });
  sec.appendChild(formSection(t('Muted'), [
    ...(muted.length ? muted : [emptyNote(t('No communities yet.'))]),
    mutedChannels.length ? h('p', { class: 'small muted', text: t('{n} muted channel(s). Unmute a channel with the bell in its header, or right-click it in the channel list.', { n: mutedChannels.length }) }) : null]));
  sec.appendChild(soundsSection(v));
}

async function renderNetwork(sec) {
  const d = settingsUI.data;
  // This device's own list (preferences.toml, not synced): the same row and
  // editor as on the screens before unlock.
  const relays = h('textarea', { rows: 3, value: (d.vault.preferred_relays || []).join('\n'), placeholder: t('Automatic') });
  sec.appendChild(formSection(null, [
    h('div', { class: 'setting-row' }, h('div', null, h('div', { text: t('Maintainer nodes') }),
      h('div', { class: 'desc', text: t('They run directory search, help you reach communities, keep direct messages for people who are offline, and pass on codes when you link a device. Kept on this device only: each of your devices has its own list, and you can change it before unlocking.') })),
    maintainerRow(d)),
    clusterStatus(d),
    clusterToggle(d),
    vaultSetting('directory_local', t('Search the directory on this device'),
      t('Downloads a copy of the public directory (about 1 KB per community) and searches it here, so maintainer nodes never see what you search for. The copy is refreshed every hour while you search.'),
      toggle(!!d.vault.directory_local), (c) => c.checked),
    vaultSetting('preferred_relays', t('Preferred relays'), t("Tried first when a community lists them. Leave empty to use each community's own relays. One ws:// or wss:// address per line."),
      relays, (c) => c.value.split(/\s+/).filter(Boolean))]));
  const conns = (await api('list_communities')).map((c) => h('div', { class: 'setting-row' }, h('div', null, h('div', { text: c.name }), h('div', { class: 'desc', text: c.relay ? t('via {relay}', { relay: c.relay }) : connectionLabel(c) })),
    h('button', { class: 'btn small', text: t('Reconnect'), onclick: () => api('retry', c.id).then(() => toast(t('Reconnecting…')), toastError) })));
  sec.appendChild(formSection(t('Connections'), conns.length ? conns : emptyNote(t('No communities yet.'))));
}

// The maintainer networks this device's entries pin: the key's fingerprint,
// the members (read-only, from the verified list) with whether each is
// reachable, and the list's number. Follows `maintainers` events.
function clusterStatus(d) {
  const box = h('div', { class: 'cluster-status' });
  box.update = (m) => {
    d.maintainers = m;
    clear(box);
    for (const c of m.clusters || []) {
      const members = c.members.length ? c.members : [{ url: c.url, role: '' }];
      box.appendChild(h('div', { class: 'cluster-card', role: 'group', 'aria-label': t('Maintainer network {fingerprint}', { fingerprint: c.fingerprint }) },
        h('div', { class: 'row wrap' }, h('b', { text: t('Network {fingerprint}', { fingerprint: c.fingerprint }) }),
          h('span', { class: 'muted small', text: c.seq ? tn('{n} node · updated from the network (list #{seq})', '{n} nodes · updated from the network (list #{seq})', c.members.length, { seq: c.seq })
            : t('Member list not fetched yet') })),
        h('ul', { class: 'cluster-members' }, members.map((x) => {
          const st = (m.health || {})[x.url];
          const state = !st ? 'unused' : st.state;
          const label = { ok: t('reachable'), backoff: t('not reachable, trying again later'), retry: t('trying again'), unknown: t('not tried yet'), unused: t('not used') }[state];
          return h('li', { class: 'cluster-member' },
            h('span', { class: `node-dot ${state}`, 'aria-hidden': 'true' }),
            h('span', { class: 'mono', text: x.url }),
            x.role === 'root' ? h('span', { class: 'pill', text: t('main') }) : null,
            h('span', { class: 'small muted', text: label }),
            st && st.error && state !== 'ok' ? h('span', { class: 'small error-text', text: st.error }) : null);
        }))));
    }
  };
  box.update(d.maintainers);
  return box;
}

on('maintainers', (m) => { for (const b of $$('.cluster-status')) if (b.update) b.update(m); });

// "Use this network's other nodes": for an entry that names its maintainer
// network (URL#key), every member, or only that node (a preference).
function clusterToggle(d) {
  const cb = toggle(d.prefs.maintainer_cluster_nodes !== false);
  return settingRow(t("Use this network's other nodes"),
    t('When a maintainer node belongs to a network of nodes that share their work, use all of them, so direct messages still arrive when one is down.'),
    cb, async () => {
      const r = await api('set_setting', 'maintainer_cluster_nodes', cb.checked);
      d.prefs.maintainer_cluster_nodes = r.value;
    });
}

async function renderData(sec) {
  const d = settingsUI.data;
  const limit = h('input', { type: 'number', min: 64, max: 102400, value: d.vault.cache_limit_mb });
  sec.appendChild(formSection(t('Cache'), [
    vaultSetting('cache_limit_mb', t('Local cache size (MB)'), t("Messages and downloaded files kept on this device, for as long as each community keeps them. Using {size} now.", { size: fmtBytes(d.cache_bytes) }), limit, (c) => Number(c.value)),
    h('div', { class: 'small muted', text: t('Clear cache deletes cached messages and files for every community. Nothing is lost: it downloads again.') })],
  formFoot(h('button', { class: 'btn', text: t('Clear cache'), onclick: async () => { await api('clear_cache'); toast(t('Cache cleared.')); settingsUI.data = await api('get_settings'); renderData(clear(sec)); } }))));
  const moved = async (p) => {
    try { await api('set_setting', 'data_dir', p); toast(t('Data moved.')); settingsUI.data = await api('get_settings'); renderData(clear(sec)); } catch (e) { toastError(e); }
  };
  sec.appendChild(formSection(t('Data folder'), [
    h('div', { class: 'small muted', text: t('Where the encrypted profile, UI state and cache live. Moving it moves the files now.') }),
    h('div', { class: 'data-folder-choice' }, d.prefs.data_dir ? codeBlock(d.prefs.data_dir) : h('span', { class: 'muted', text: t('OS default') }))],
  formFoot(
    h('button', { class: 'btn', text: t('Choose…'), onclick: async () => { const p = await pickFile('folder'); if (p) moved(p); } }),
    h('button', { class: 'btn', text: t('Use default'), onclick: () => moved('') }))));
  const locations = await api('storage_locations');
  const groups = [
    ['config', t('Settings: readable before unlock')],
    ['data', t('Profile and messages: encrypted')],
    ['cache', t('Cache: encrypted, safe to delete')],
    ['logs', t('Logs')],
    ['keychain', t('System keychain')],
    ['toolkit', t('Window toolkit')],
  ];
  const list = h('div', { class: 'storage-list' });
  for (const [group, title] of groups) {
    const items = locations.filter((l) => l.group === group);
    if (!items.length) continue;
    list.appendChild(h('h4', { text: title }));
    for (const l of items) list.appendChild(storageRow(l));
  }
  sec.appendChild(formSection(t('Where OpenArcanum keeps things'), [
    h('p', { class: 'small muted', text: t('Every folder and file this app writes on this device, with its full path. To back up everything, copy the settings and data folders while OpenArcanum is closed (the profile in them stays locked with your passphrase). An exported backup (Settings → Security) is what moves your keys to a new device; community messages download again from their servers.') }),
    list]));
  sec.appendChild(formSection(t('Reset all data'), [
    h('p', { class: 'small muted', text: window.openarcanumWeb
      ? t('Start over in this browser: your profile, keys, settings and every message stored here are deleted. Your other devices are not affected.')
      : t('Start over on this device: your profile is set aside (not deleted) and this device starts fresh. Your other devices are not affected.') })],
  formFoot(h('button', { class: 'btn danger', text: t('Reset all data…'), onclick: resetAllDialog }))));
}

// Settings → Reset all data. Owners are warned first: their owner keys are
// in this profile, so the dialog offers a recovery backup to copy (every
// key, encrypted with a passphrase of its own; Restore from a backup takes
// it pasted in), and resetting waits until they say it's kept.
function resetAllDialog() {
  const owned = st.communities.filter((c) => c.is_owner && (c.status === 'active' || c.status === 'pending'));
  const current = h('input', { type: 'password', autocomplete: 'current-password' });
  const err = h('div', { class: 'error', role: 'alert' });
  const body = h('div', { class: 'stack' },
    h('p', { text: window.openarcanumWeb
      ? t('Everything OpenArcanum keeps in this browser is deleted: your profile, keys, settings and messages. This can\'t be undone.')
      : t('Your profile is set aside and this device starts over, as on first launch.') }));
  let kept = null;
  if (owned.length) {
    const pass = passphraseField(t('Recovery backup passphrase'), { generate: true,
      saveNote: t('Save this passphrase in your password manager, next to the recovery backup. Without it, nobody can open the backup.') });
    kept = h('input', { type: 'checkbox' });
    const copy = h('button', { class: 'btn primary', text: t('Copy recovery backup'), onclick: async () => {
      err.textContent = '';
      if (!pass.input.value) { err.textContent = t('Choose a passphrase for the recovery backup (Generate makes one).'); return; }
      if (!current.value) { err.textContent = t('Enter your passphrase to confirm.'); return; }
      copy.disabled = true;
      try {
        const r = await api('export_backup_text', pass.input.value, current.value);
        await api('copy_text', r.text);
        toast(t('Recovery backup copied. Paste it into your password manager with its passphrase.'));
      } catch (e) { err.textContent = e.message; } finally { copy.disabled = false; }
    } });
    body.append(
      h('div', { class: 'notice' },
        h('b', { text: tn('You own {n} community:', 'You own {n} communities:', owned.length, { n: owned.length }) }), ' ',
        owned.map((c) => c.name).join(', '), '. ',
        t('Your owner keys are in this profile. Without them nobody can manage these communities, move them to new hardware or recover their ownership.')),
      h('p', { class: 'small', text: t('Copy a recovery backup first: it holds every key you have, owner keys included, encrypted with the passphrase below. To recover, choose Restore from a backup when starting OpenArcanum and paste it in.') }),
      pass, copy,
      h('label', { class: 'check' }, kept, h('span', { text: t('I saved the recovery backup and its passphrase somewhere safe.') })));
  }
  body.append(field(t('Your passphrase'), current, t('The passphrase of your profile, to confirm it is you.')), err);
  modal({ title: t('Reset all data'), body, actions: [{ label: t('Cancel') }, { label: t('Reset all data'), kind: 'danger', run: async () => {
    err.textContent = '';
    if (kept && !kept.checked) { err.textContent = t('Copy the recovery backup and tick the box first: your owner keys would be lost.'); return false; }
    if (!current.value) { err.textContent = t('Enter your passphrase to confirm.'); return false; }
    try {
      const status = await api('reset_all', current.value);
      if (window.openarcanumWebReset) { await window.openarcanumWebReset(); return true; }
      closeDialogs();
      showLock(status);
    } catch (e) { err.textContent = e.message; return false; }
    return true;
  } }] });
}

// One place the app keeps something: what it is, its full path (Copy), and
// Open folder for a folder or a file on disk.
function storageRow(l) {
  const open = l.kind === 'keychain' ? null : h('button', { class: 'btn small', type: 'button',
    text: l.kind === 'folder' ? t('Open folder') : t('Open its folder'),
    'aria-label': t('Open the folder of {name}', { name: l.label }),
    disabled: l.kind === 'folder' && !l.exists,
    onclick: () => api('open_folder', l.id).catch(toastError) });
  return h('div', { class: 'storage-row', 'data-location': l.id },
    h('div', { class: 'row wrap' }, h('b', { text: l.label }), !l.exists ? h('span', { class: 'pill', text: t('not created yet') }) : null),
    h('div', { class: 'desc', text: l.desc }),
    codeBlock(l.path, { copyAria: t('Copy the path of {name}', { name: l.label }), actions: open ? [open] : [] }));
}

async function renderPrivacy(sec) {
  const v = settingsUI.data.vault;
  sec.appendChild(formSection(null, vaultSetting('dm_policy', t('Who can DM me'), t('Contacts are people you added yourself.'),
    selectEl([['contacts', t('Contacts only')], ['members', t('Members of communities we share')]], v.dm_policy))));
  // Communities that turned contact cards off don't offer them.
  const cards = (await api('list_communities')).filter((x) => x.status === 'active' && (!x.features || x.features.contact_cards !== false)).map((c) => {
    const cb = toggle(c.publish_card);
    return settingRow(c.name, null, cb, () => api('set_contact_card', c.id, cb.checked));
  });
  sec.appendChild(formSection(t('Contact cards'), [
    h('p', { class: 'small muted', text: t('A contact card lets members of a community start a direct message with you. It holds only what they need to reach you, and it is different in each community. Off by default.') }),
    ...(cards.length ? cards : [emptyNote(t('No communities yet.'))])]));
  // Who sees your status (status.js).
  sec.appendChild(await statusPrivacySection());
}

async function renderAbout(sec) {
  const s = await api('status');
  const d = settingsUI.data;
  sec.appendChild(formSection(null, h('div', { class: 'stack' },
    h('div', { class: 'about-head' }, h('img', { class: 'about-logo', src: 'brand/openarcanum.svg', alt: '' }),
      h('div', null, h('b', { text: `${s.product} ${s.version}` }), h('span', { class: 'muted', text: ' · ' + s.platform }))),
    h('div', { class: 'small' }, t('Data folder:'), ' ', h('span', { class: 'mono', text: d.data_dir_effective })),
    h('div', { class: 'small muted', text: t('The app window stores nothing itself: keys, settings and history live in encrypted files in the folders above (Data & storage lists every path). Developer tools (Ctrl+Shift+I) work only when OpenArcanum was started with --debug.') }),
    h('div', { class: 'small muted', text: t("Generated passphrases use EFF's Long Wordlist by the Electronic Frontier Foundation (eff.org/dice), under the Creative Commons Attribution 3.0 US license.") }),
    h('div', { class: 'small muted', text: t('Interface icons from Lucide (lucide.dev), under the ISC license.') }))));
}
