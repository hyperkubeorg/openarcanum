// First run, unlock, restore and "forgot passphrase".
'use strict';

const lockUI = {};

function lockCard(...children) {
  const root = clear($('#lock'));
  root.appendChild(h('div', { class: 'lock-card' }, themeSwitch(), h('img', { class: 'logo', src: 'brand/openarcanum.svg', alt: '' }), ...children));
  return root;
}

// The theme, on every screen before unlock (preferences.toml is readable and
// writable without the profile). Settings → Appearance has the same choice.
const THEMES = [['dark', N_('Dark')], ['light', N_('Light')], ['system', N_('System')]];
function themeSwitch() {
  const current = () => document.documentElement.dataset.theme || 'dark';
  const group = h('div', { class: 'theme-switch', role: 'group', 'aria-label': t('Theme') });
  const paint = () => {
    for (const b of $$('button', group)) b.setAttribute('aria-pressed', String(b.dataset.theme === current()));
  };
  for (const [value, label] of THEMES) {
    group.appendChild(h('button', { class: 'btn small', type: 'button', 'data-theme': value, text: t(label),
      onclick: async () => {
        try {
          const r = await api('set_setting', 'theme', value);
          applyAppearance({ ...(st.status && st.status.appearance), theme: r.value });
          if (st.status && st.status.appearance) st.status.appearance.theme = r.value;
          paint();
        } catch (e) { toastError(e); }
      } }));
  }
  paint();
  return group;
}

function showLock(status) {
  $('#app').classList.add('hidden');
  $('#boot').classList.add('hidden');
  $('#lock').classList.remove('hidden');
  if (status.state === 'no_profile') return lockUI.firstRun(status);
  return lockUI.unlock(status);
}

lockUI.firstRun = function (status) {
  lockCard(
    h('h1', { class: 'center', text: t('Welcome to {product}', { product: status.product }) }),
    h('p', { class: 'center muted', text: t('There are no accounts. Your keys live in an encrypted profile on this device.') }),
    h('div', { class: 'stack' },
      h('button', { class: 'btn primary block', text: t('Create a profile'), onclick: () => lockUI.create(status) }),
      h('button', { class: 'btn block', text: t('Link to an existing device'), onclick: () => linkNewDevice(status) }),
      h('button', { class: 'btn block', text: t('Restore from backup'), onclick: () => lockUI.restore(status) }),
    ),
  );
};

lockUI.create = function (status) {
  const again = h('input', { type: 'password', autocomplete: 'new-password' });
  const pass = passphraseField(t('Passphrase'), { generate: true, repeat: again });
  const device = h('input', { type: 'text', value: t('{platform} device', { platform: status.platform }) });
  const err = h('div', { class: 'error' });
  const go = h('button', { class: 'btn primary', text: t('Create profile'), onclick: async () => {
    err.textContent = '';
    if (pass.input.value !== again.value) { err.textContent = t("The passphrases don't match."); return; }
    go.disabled = true; go.textContent = t('Creating…');
    try { const st = await api('create_profile', pass.input.value, device.value); enterApp(st); }
    catch (e) { err.textContent = e.message; }
    finally { go.disabled = false; go.textContent = t('Create profile'); }
  } });
  lockCard(
    h('h1', { class: 'center', text: t('Create your profile') }),
    h('p', { class: 'muted small', text: t('Pick a passphrase you can remember, or press Generate and keep it in your password manager. There is no reset: if you lose it, you can only link again from another device or restore a backup.') }),
    h('div', { class: 'stack' }, pass, field(t('Repeat passphrase'), again), field(t('Name this device'), device, t('Shown in your device list.')), err),
    h('div', { class: 'form-actions' }, h('button', { class: 'btn link', text: t('Back'), onclick: () => lockUI.firstRun(status) }), go),
    maintainerRow(status),
  );
  again.addEventListener('keydown', (e) => { if (e.key === 'Enter') go.click(); });
};

lockUI.unlock = function (status, note = '') {
  const pass = h('input', { type: 'password', autocomplete: 'current-password', placeholder: t('Passphrase'), 'aria-label': t('Passphrase') });
  const err = h('div', { class: 'error', text: note });
  const go = h('button', { class: 'btn primary block', text: t('Unlock') });
  let wait = null;
  const countdown = (seconds) => {
    clearInterval(wait);
    let left = Math.ceil(seconds);
    if (left <= 0) return;
    go.disabled = true;
    const tick = () => {
      if (left <= 0) { clearInterval(wait); go.disabled = false; go.textContent = t('Unlock'); return; }
      go.textContent = t('Wait {seconds} s', { seconds: left }); left--;
    };
    tick(); wait = setInterval(tick, 1000);
  };
  // One unlock at a time: both buttons stay disabled while either runs.
  // The keychain button is there only while a key is kept in the keychain
  // (opening at start failed, say a keychain prompt was dismissed); a lock
  // deletes the key, so after one the passphrase is the way back in.
  const keychainBtn = status.remember && status.remember.stored === 'keychain'
    ? h('button', { class: 'btn block', text: t("Unlock with this device's keychain") }) : null;
  const busy = (on) => { go.disabled = on; if (keychainBtn) keychainBtn.disabled = on; };
  go.onclick = async () => {
    err.textContent = '';
    busy(true); go.textContent = t('Unlocking…');
    try { const st = await api('unlock', pass.value); clearInterval(wait); enterApp(st); }
    catch (e) {
      err.textContent = e.message;
      pass.select();
      const st = await api('status');
      busy(false); go.textContent = t('Unlock');
      countdown(st.backoff);
    }
  };
  if (keychainBtn) keychainBtn.onclick = async () => {
    err.textContent = '';
    busy(true);
    try { enterApp(await api('unlock_remembered')); }
    catch (e) { err.textContent = e.message; busy(false); }
  };
  pass.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !go.disabled) go.click(); });
  lockCard(
    h('h1', { class: 'center', text: t('Unlock') }),
    h('p', { class: 'center muted', text: t('Enter the passphrase for this device.') }),
    h('div', { class: 'stack' }, pass, err, go, keychainBtn),
    h('div', { class: 'lock-links' }, h('button', { class: 'btn link', text: t('Forgot your passphrase?'), onclick: () => lockUI.forgot(status) })),
    maintainerRow(status),
  );
  countdown(status.backoff);
  setTimeout(() => pass.focus(), 50);
};

// Profiles from before staying signed in was the default keep asking for
// the passphrase until told otherwise: asked once, right after the
// passphrase opened the profile. `where` is where the key would be kept.
lockUI.offerStaySignedIn = function (where) {
  let answered = false;
  const answer = async (accept) => {
    answered = true;
    try { await api('answer_stay_signed_in', accept); } catch (e) { toastError(e); }
  };
  modal({
    title: t('Stay signed in on this device?'),
    body: h('div', { class: 'stack stay-signed-in-offer' },
      h('p', { text: t("OpenArcanum can open without your passphrase when it starts on this computer. Change this any time in Settings → Security.") }),
      where === 'file'
        ? h('p', { class: 'small muted', text: t("This system has no keychain, so the key would be kept in a file: anyone who can read your user's files could open your profile.") })
        : null),
    actions: [
      { label: t('Keep asking'), run: () => answer(false) },
      { label: t('Stay signed in'), kind: 'primary', run: () => answer(true) },
    ],
    // Closed without an answer: asked once, so that counts as Keep asking.
    // (Closed by a lock, the offer is already withdrawn: nothing to say.)
    onclose: () => { if (!answered) { answered = true; api('answer_stay_signed_in', false).catch(() => {}); } },
  });
};

lockUI.forgot = function (status) {
  lockCard(
    h('h1', { class: 'center', text: t('Forgot your passphrase?') }),
    h('p', { class: 'muted', text: t("There's no reset: the passphrase is the only way into this profile. You can:") }),
    h('div', { class: 'stack' },
      h('div', { class: 'card' }, h('b', { text: t('Link again from another device') }),
        h('p', { class: 'small muted', text: t('If one of your other devices is still unlocked, start a new profile here and choose "Link to an existing device". Each device has its own passphrase.') })),
      h('div', { class: 'card' }, h('b', { text: t('Restore from a backup file') }),
        h('p', { class: 'small muted', text: t('You need the backup file and its passphrase.') })),
      h('div', { class: 'card' }, h('b', { text: t('Start a new profile') }),
        h('p', { class: 'small muted', text: t("Your old communities won't know the new keys; their admins would need to re-admit you.") }))),
    h('div', { class: 'form-actions' },
      h('button', { class: 'btn link', text: t('Back'), onclick: () => lockUI.unlock(status) }),
      h('button', { class: 'btn danger', text: t('Set this profile aside and start over…'), onclick: async () => {
        const ok = await confirmModal(t('Start over?'), t('This profile is moved aside (not deleted) and this device starts fresh. You can then link it to another device, restore a backup, or create a new profile.'), t('Start over'));
        if (!ok) return;
        try { const st = await api('reset_profile'); showLock(st); } catch (e) { toastError(e); }
      } })),
  );
};

lockUI.restore = function (status) {
  const file = h('input', { type: 'text', placeholder: t('/path/to/openarcanum-backup.fbak'), autocomplete: 'off', 'aria-label': t('Backup file') });
  const bpass = h('input', { type: 'password', autocomplete: 'off' });
  const npass = passphraseField(t('New passphrase for this device'), { generate: true });
  const device = h('input', { type: 'text', value: t('{platform} device', { platform: status.platform }) });
  const err = h('div', { class: 'error' });
  const go = h('button', { class: 'btn primary', text: t('Restore'), onclick: async () => {
    err.textContent = '';
    const path = file.value.trim();
    if (!path) { err.textContent = t('Choose the backup file.'); return; }
    go.disabled = true; go.textContent = t('Restoring…');
    try {
      const chosen = await pickFile('backup', path);
      if (chosen) { const st = await api('restore_backup', chosen, bpass.value, npass.input.value, device.value); enterApp(st); }
    }
    catch (e) { err.textContent = e.message; }
    finally { go.disabled = false; go.textContent = t('Restore'); }
  } });
  lockCard(
    h('h1', { class: 'center', text: t('Restore from backup') }),
    h('p', { class: 'muted small', text: t('This sets up the profile from a backup file: your keys and communities, but not message history (that comes back from the community servers). This device gets its own keys.') }),
    h('div', { class: 'stack' },
      h('div', { class: 'field' }, h('span', { text: t('Backup file') }), h('div', { class: 'row' }, file, h('button', { class: 'btn', text: t('Choose file…'), onclick: async () => {
        const p = await pickFile('backup'); if (p) file.value = p;
      } })), h('div', { class: 'hint', text: t('Or paste a recovery backup (openarcanum-backup:…) copied from Settings → Data & storage → Reset all data.') })),
      field(t('Backup passphrase'), bpass), npass, field(t('Name this device'), device), err),
    h('div', { class: 'form-actions' }, h('button', { class: 'btn link', text: t('Back'), onclick: () => lockUI.firstRun(status) }), go),
    maintainerRow(status),
  );
};

// ---------------------------------------------------------------- maintainer nodes (docs/settings.md)
// Per device, in preferences.toml: shown and changed on every screen before
// unlock that talks to the network or opens a profile, and in Settings →
// Network. `holder.maintainers` is the core's maintainers_status().

let maintainerSeq = 0;

function maintainerHost(url) {
  try { return new URL(maintainerURL(url)).host || url; } catch (e) { return url; }
}

// An entry is a node's URL, or URL#KEY for a node of a maintainer network
// whose member list this device verifies with that key (docs/protocol.md §7.5).
function maintainerURL(entry) { return String(entry).split('#')[0]; }

function maintainerSourceText(m) {
  if (m.source === 'own') return t('your own list');
  if (m.source === 'launch') return t('from the launch option');
  return t('the defaults');
}

// The compact row: what's in use, where it comes from, and Change….
function maintainerRow(holder) {
  const summary = h('span', { class: 'maintainer-summary' });
  const source = h('span', { class: 'maintainer-source muted' });
  const change = h('button', { class: 'btn small', type: 'button', text: t('Change…'), 'aria-label': t('Change maintainer nodes'),
    'aria-haspopup': 'dialog', onclick: () => maintainerEditor(holder.maintainers) });
  const row = h('div', { class: 'maintainer-row small', role: 'group', 'aria-label': t('Maintainer nodes') },
    h('div', { class: 'maintainer-text' }, summary, ' ', source), change);
  row.update = (m) => {
    holder.maintainers = m;
    const nodes = (m.effective || []).map(maintainerURL);
    summary.textContent = nodes.length === 1
      ? t('Maintainer nodes: {node}', { node: maintainerHost(nodes[0]) })
      : tn('{n} maintainer node', '{n} maintainer nodes', nodes.length);
    summary.title = nodes.join('\n');
    source.textContent = t('({source})', { source: maintainerSourceText(m) });
  };
  row.update(holder.maintainers);
  return row;
}

// Saved here or elsewhere (Settings, another screen): every visible row follows.
on('maintainers', (m) => { for (const row of $$('.maintainer-row')) if (row.update) row.update(m); });

// The editor: remove nodes, add one (checked in Python with the setting's own
// rules), or go back to the defaults. Nothing is saved until Save.
function maintainerEditor(m) {
  const fallback = (m.launch && m.launch.length) ? m.launch : m.builtin;
  let own = m.source === 'own';
  let nodes = [...(own ? m.own : fallback)];
  const seq = ++maintainerSeq;
  const inputId = `maintainer-add-${seq}`;
  const errId = `maintainer-error-${seq}`;
  const list = h('ul', { class: 'maintainer-list', 'aria-label': t('Maintainer nodes') });
  const state = h('p', { class: 'small muted maintainer-state' });
  const live = h('div', { class: 'sr-only', role: 'status', 'aria-live': 'polite' });
  const input = h('input', { type: 'text', id: inputId, inputmode: 'url', autocomplete: 'off', spellcheck: 'false',
    placeholder: t('https://node.example'), 'aria-describedby': errId });
  const err = h('div', { class: 'error maintainer-error', id: errId, role: 'alert' });
  const saveErr = h('div', { class: 'error maintainer-save-error', role: 'alert' });
  const defaults = h('button', { class: 'btn small', type: 'button', text: t('Use the defaults') });
  const say = (text) => { live.textContent = text; };
  const showError = (text) => {
    err.textContent = text;
    if (text) input.setAttribute('aria-invalid', 'true'); else input.removeAttribute('aria-invalid');
  };
  // Fingerprints of the networks entries pin (saved ones, and ones just
  // confirmed here), and the offer to use a just-added node's network.
  const fingerprints = new Map((m.clusters || []).map((c) => [c.entry, c.fingerprint]));
  const offerBox = h('div', { class: 'maintainer-offer', role: 'group', 'aria-label': t('Maintainer network'), hidden: true });
  const render = (focusIndex = null) => {
    clear(list);
    nodes.forEach((entry, i) => {
      const url = maintainerURL(entry);
      const fp = fingerprints.get(entry);
      list.appendChild(h('li', { class: 'maintainer-item' },
        h('span', { class: 'maintainer-entry' }, h('span', { class: 'mono', text: url }),
          entry.includes('#') ? h('span', { class: 'pill', text: fp ? t('network {fingerprint}', { fingerprint: fp }) : t('network') }) : null),
        h('button', { class: 'btn small', type: 'button', text: t('Remove'), 'aria-label': t('Remove {node}', { node: url }), onclick: () => {
          nodes.splice(i, 1);
          own = true;
          showError('');
          offerBox.hidden = true;
          say(t('Removed {node}', { node: url }));
          render(i);
        } })));
    });
    if (!nodes.length) list.appendChild(h('li', { class: 'muted maintainer-none', text: t('No nodes in the list. Add one, or use the defaults.') }));
    if (own) state.textContent = t('Your own list, kept on this device only.');
    else if (m.launch && m.launch.length) state.textContent = t('The defaults: the nodes given with the --maintainer launch option.');
    else state.textContent = t('The defaults: the built-in nodes.');
    defaults.disabled = !own;
    if (focusIndex !== null) {
      // Removing a node moves focus to the next Remove button (or the field).
      const buttons = $$('button', list);
      (buttons[Math.min(focusIndex, buttons.length - 1)] || input).focus();
    }
  };
  // A node that belongs to a maintainer network (it serves a signed member
  // list): offer to use the whole network, showing the fingerprint of the
  // key that signs the list, which is pinned if accepted.
  const offerNetwork = async (entry) => {
    if (entry.includes('#')) return;
    let offer = null;
    try { offer = await api('maintainer_cluster_offer', entry); } catch (e) { return; }
    if (!offer || !nodes.includes(entry)) return;
    const n = offer.members.length;
    const close = () => { offerBox.hidden = true; input.focus(); };
    append(clear(offerBox), [
      h('p', { class: 'small', text: tn('{node} belongs to a maintainer network of {n} node that share their work. The network\'s fingerprint is {fingerprint}: check it with the people who run it.',
        '{node} belongs to a maintainer network of {n} nodes that share their work. The network\'s fingerprint is {fingerprint}: check it with the people who run it.', n,
        { node: maintainerHost(entry), fingerprint: offer.fingerprint }) }),
      h('ul', { class: 'small maintainer-offer-members' }, offer.members.map((x) => h('li', { class: 'mono', text: x.role === 'root' ? t('{node} (main)', { node: x.url }) : x.url }))),
      h('div', { class: 'row' },
        h('button', { class: 'btn small primary', type: 'button', text: t('Use the network'), onclick: () => {
          nodes = nodes.map((x) => (x === entry ? offer.entry : x));
          fingerprints.set(offer.entry, offer.fingerprint);
          say(t('Using the network of {node}', { node: maintainerHost(entry) }));
          render();
          close();
        } }),
        h('button', { class: 'btn small', type: 'button', text: t('Only this node'), onclick: close }))]);
    offerBox.hidden = false;
    say(t('{node} belongs to a maintainer network.', { node: maintainerHost(entry) }));
  };
  const add = async () => {
    showError('');
    try {
      const next = await api('check_new_maintainer', input.value, nodes);
      const added = next[next.length - 1];
      nodes = next;
      own = true;
      input.value = '';
      say(t('Added {node}', { node: added }));
      render();
      offerNetwork(added);
    } catch (e) { showError(e.message); }
    input.focus();
  };
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); add(); } });
  defaults.onclick = () => {
    own = false;
    nodes = [...fallback];
    showError('');
    say(t('Using the defaults.'));
    render();
    input.focus();
  };
  render();
  modal({
    title: t('Maintainer nodes'),
    body: h('div', { class: 'stack maintainer-editor' },
      h('p', { class: 'small', text: t('Maintainer nodes run the public directory, help you reach communities, keep direct messages for people who are offline, and pass on codes when you link a device. This list is saved on this device only, unencrypted, and is not copied to your other devices.') }),
      state, list,
      h('div', { class: 'field' }, h('label', { for: inputId, text: t('Add a node') }),
        h('div', { class: 'row' }, input, h('button', { class: 'btn', type: 'button', text: t('Add'), onclick: add })),
        h('div', { class: 'hint', text: t('https:// addresses; http:// only for a node on this computer. Up to {max}.', { max: m.max }) }), err),
      offerBox,
      h('div', { class: 'row' }, defaults), live, saveErr),
    actions: [
      { label: t('Cancel') },
      { label: t('Save'), kind: 'primary', run: async () => {
        saveErr.textContent = '';
        if (own && !nodes.length) { saveErr.textContent = t('Add a node, or choose Use the defaults.'); return false; }
        try {
          const saved = await api('set_maintainers', own ? nodes : []);
          for (const row of $$('.maintainer-row')) if (row.update) row.update(saved);
          toast(t('Maintainer nodes saved.'));
          return true;
        } catch (e) { saveErr.textContent = e.message; return false; }
      } },
    ],
  });
}
