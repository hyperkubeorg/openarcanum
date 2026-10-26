// Linked devices (docs/protocol.md §10): Settings → Devices, and the two
// halves of linking.
'use strict';

async function renderDevicesSettings(sec) {
  const list = await api('devices');
  sec.appendChild(sectionIntro(t('Every linked device holds your keys and has its own passphrase. Removing a device makes every community refuse it and takes it out of all your conversations.')));
  sec.appendChild(toolbar(h('button', { class: 'btn primary', text: t('Link a new device…'), onclick: linkExistingDevice })));
  const table = h('table', { class: 'list' }, h('tr', null, ...[t('Device'), t('Linked'), t('Last seen'), ''].map((x) => h('th', { text: x }))));
  for (const d of list) {
    table.appendChild(h('tr', null,
      h('td', null, h('b', { text: d.name }), d.this ? h('span', { class: 'pill ok', text: t('this device') }) : null, h('div', { class: 'small muted', text: d.platform })),
      h('td', { class: 'small', text: fmtDateTime(d.linked_at * 1000) }),
      h('td', { class: 'small', text: d.this ? t('now') : fmtDateTime(d.last_seen * 1000) }),
      h('td', null, h('div', { class: 'row' },
        h('button', { class: 'btn small', text: t('Rename'), onclick: async () => {
          const n = await promptModal(t('Rename device'), t('Name'), d.name); if (!n) return;
          try { await api('rename_device', d.id, n); renderDevicesSettings(clear(sec)); } catch (e) { toastError(e); }
        } }),
        !d.this ? h('button', { class: 'btn small danger', text: t('Remove'), onclick: () => removeDevice(sec, d) }) : null))));
  }
  sec.appendChild(table);
}

// Removing a device takes the passphrase again (Python checks it).
function removeDevice(sec, d) {
  const pass = h('input', { type: 'password', autocomplete: 'current-password' });
  const err = h('div', { class: 'error' });
  modal({ title: t('Remove {name}?', { name: d.name }), body: h('div', { class: 'stack' },
    h('p', { text: t('Communities will refuse it, and if it comes online it wipes its profile. If its passphrase may be known too, also replace your identity in each community.') }),
    field(t('Your passphrase'), pass, t('The passphrase of your profile, to confirm it is you.')), err),
  actions: [{ label: t('Cancel') }, { label: t('Remove'), kind: 'danger', run: async () => {
    err.textContent = '';
    if (!pass.value) { err.textContent = t('Enter your passphrase to confirm.'); return false; }
    try {
      const r = await api('remove_device', d.id, pass.value);
      if (r.failures.length) toast(t('Some communities were unreachable: {failures}', { failures: r.failures.join('; ') }), { error: true });
      else toast(t('{name} removed.', { name: d.name }));
      renderDevicesSettings(clear(sec));
    } catch (e) { err.textContent = e.message; return false; }
  } }] });
}

// On an unlocked, existing device: type the code shown on the new one, and
// the passphrase again (Python checks it before anything is contacted).
function linkExistingDevice() {
  const code = h('input', { type: 'text', placeholder: '42-purple-sausage-anchor', autocomplete: 'off' }); // i18n: not shown (an example link code, the same in every language)
  const pass = h('input', { type: 'password', autocomplete: 'current-password' });
  const err = h('div', { class: 'error' });
  modal({ title: t('Link a new device'), body: h('div', { class: 'stack' },
    h('p', { class: 'small', text: t('On the new device choose "Link to an existing device", then type the code it shows.') }),
    field(t('Code'), code), field(t('Your passphrase'), pass, t('The passphrase of your profile, to confirm it is you.')), err),
  actions: [{ label: t('Cancel'), run: () => api('link_cancel') }, { label: t('Continue'), kind: 'primary', run: async () => {
    err.textContent = '';
    if (!pass.value) { err.textContent = t('Enter your passphrase to confirm.'); return false; }
    try { const info = await api('link_enter_code', code.value, pass.value); approveLink(info); }
    catch (e) { err.textContent = e.message; return false; }
  } }] });
}

function approveLink(info) {
  const boxes = info.communities.map((c) => { const b = checkbox(c.name, !c.local_only, c.messages_size ? t('{size} of cached messages', { size: fmtBytes(c.messages_size) }) : null); b.cid = c.id; return b; });
  const dms = checkbox(t('Copy direct message history'), true, t('{size}. Your devices hold the only copy of your direct messages.', { size: fmtBytes(info.dm_size) }));
  const msgs = checkbox(t('Copy downloaded community messages'), false, t('Optional: the new device can fetch them from the servers.'));
  modal({ title: t('Approve new device'), dismissable: false, body: h('div', { class: 'stack' },
    h('div', { class: 'card' }, h('b', { text: info.device_name }), h('div', { class: 'small muted', text: info.platform })),
    h('div', { class: 'notice', text: info.warning }),
    h('div', { class: 'group-label', text: t('Communities to share:') }), ...boxes, dms, msgs),
  actions: [{ label: t('Cancel'), run: () => api('link_cancel') }, { label: t('Approve and send'), kind: 'primary', run: async () => {
    const r = await api('link_approve', { communities: boxes.filter((b) => b.input.checked).map((b) => b.cid), copy_dms: dms.input.checked, copy_messages: msgs.input.checked });
    toast(t('New device linked: {device}', { device: r.device }));
  } }] });
}

// On the new device (first run): show a code and wait.
function linkNewDevice(status) {
  const name = h('input', { type: 'text', value: t('{platform} device', { platform: status.platform }) });
  const err = h('div', { class: 'error' });
  const go = h('button', { class: 'btn primary', text: t('Get a code'), onclick: async () => {
    err.textContent = '';
    go.disabled = true;
    try {
      // Through this device's maintainer nodes (the row below).
      const r = await api('link_new_device_start', name.value);
      showLinkCode(status, r);
    } catch (e) { err.textContent = e.message; go.disabled = false; }
  } });
  lockCard(h('h1', { class: 'center', text: t('Link to an existing device') }),
    h('div', { class: 'stack' }, field(t('Name this device'), name), err,
      h('p', { class: 'small muted', text: t("The code goes through this device's maintainer nodes. If your other device uses its own list, use the same nodes here.") })),
    h('div', { class: 'form-actions' }, h('button', { class: 'btn link', text: t('Back'), onclick: () => lockUI.firstRun(status) }), go),
    maintainerRow(status));
}

function showLinkCode(status, r) {
  const left = h('span');
  const state = h('div', { class: 'muted small center', text: t('Waiting for the other device…') });
  const timer = setInterval(() => {
    const s = Math.max(0, Math.round((r.expires_at - Date.now()) / 1000));
    left.textContent = `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
    if (s === 0) { clearInterval(timer); state.textContent = t('The code expired. Go back and get a new one.'); }
  }, 500);
  const [expiresBefore, expiresAfter] = t('Expires in {time}', { time: '{time}' }).split('{time}'); // text around the live countdown
  lockCard(h('h1', { class: 'center', text: t('Your link code') }),
    h('p', { class: 'small', text: t('On a device that is already set up and unlocked, open Settings → Devices → Link a new device and type:') }),
    h('div', { class: 'stack' }, h('div', { class: 'code-display', text: r.code }), h('p', { class: 'center small muted' }, expiresBefore, left, expiresAfter), state),
    h('div', { class: 'form-actions' }, h('button', { class: 'btn link', text: t('Cancel'), onclick: () => { clearInterval(timer); api('link_cancel'); lockUI.firstRun(status); } })));
  bridge.linkHandler = (ev) => {
    if (ev.stage === 'waiting_approval') state.textContent = t('Codes match. Approve the link on the other device.');
    else if (ev.stage === 'failed') { clearInterval(timer); state.textContent = ev.error; state.className = 'error center'; }
    else if (ev.stage === 'received') { clearInterval(timer); finishLink(status, ev); }
  };
}

function finishLink(status, ev) {
  const again = h('input', { type: 'password', autocomplete: 'new-password' });
  const pass = passphraseField(t('Passphrase for this device'), { generate: true, repeat: again });
  const err = h('div', { class: 'error' });
  const go = h('button', { class: 'btn primary', text: t('Finish'), onclick: async () => {
    err.textContent = '';
    if (pass.input.value !== again.value) { err.textContent = t("The passphrases don't match."); return; }
    go.disabled = true;
    try { const st2 = await api('link_new_device_finish', pass.input.value); enterApp(st2); }
    catch (e) { err.textContent = e.message; go.disabled = false; }
  } });
  lockCard(h('h1', { class: 'center', text: t('Almost done') }),
    h('p', { class: 'small', text: t('Received from {device}: {communities} communities, {dms} conversations.', { device: ev.from, communities: ev.communities.length, dms: ev.dms }) }),
    h('div', { class: 'stack' }, pass, field(t('Repeat passphrase'), again), err),
    h('div', { class: 'form-actions' }, go));
}

on('link', (ev) => { if (bridge.linkHandler) bridge.linkHandler(ev); });
on('devices', () => {
  const sec = $('.settings section > div');
  if (sec && $('.settings nav .item.sel') && $('.settings nav .item.sel').dataset.id === 'devices') renderDevicesSettings(clear(sec)).catch(() => {});
});
