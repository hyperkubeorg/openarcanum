// Updates (docs/release.md): Settings → Updates and the prompt that asks
// before anything is installed. Python checks, downloads and verifies; the
// page only shows the state and passes on the user's answer.
'use strict';

const updateUI = {
  status: null,
  prompt: null, // { version, close } while the prompt is open
  statusEl: null, // the status line in Settings → Updates, while it's open
  actionsEl: null,
};

function updateStateText(s) {
  if (!s) return '';
  if (s.state === 'downloading' && s.total) return `${s.message} ${Math.floor((100 * (s.done || 0)) / s.total)}%`;
  if (s.state === 'idle') return t('Not checked yet.');
  return s.message || '';
}

// Every status change from Python (events, and answers to our calls).
function updateChanged(s) {
  updateUI.status = s;
  if (updateUI.statusEl && updateUI.statusEl.isConnected) {
    updateUI.statusEl.textContent = updateStateText(s);
    updateUI.statusEl.dataset.state = s.state;
    renderUpdateActions();
  }
  // A prompt removed with the rest of the page (locking) is gone: show it again.
  if (updateUI.prompt && !updateUI.prompt.box.isConnected) updateUI.prompt = null;
  if (updateUI.prompt && (!s.prompt || s.version !== updateUI.prompt.version)) { updateUI.prompt.close(); updateUI.prompt = null; }
  // Only over the unlocked app: the lock screen shows nothing about updates.
  if (s.prompt && !updateUI.prompt && !$('#app').classList.contains('hidden')) updatePrompt(s);
}

on('update', updateChanged);

// After unlocking, show an update that became ready while locked.
function updateAfterUnlock() {
  api('update_status').then(updateChanged).catch(() => {});
}

// "Version X is ready": the only way to install. Each answer is a real
// click (event.isTrusted); Python also requires the token it issued with
// this exact version.
function updatePrompt(s) {
  let answered = false;
  const version = s.version;
  const token = s.token;
  const m = modal({
    title: t('Update ready'),
    body: h('div', { class: 'stack update-prompt' },
      h('p', null, h('b', { text: t('Version {version} is ready.', { version }) })),
      h('p', { class: 'muted', text: st.status
        ? t("{product} restarts to install it. You're running {current}. The download was checked and is genuine.", { product: st.status.product, current: s.current })
        : t("The app restarts to install it. You're running {current}. The download was checked and is genuine.", { current: s.current }) })),
    actions: [
      { label: t('Later'), run: () => { answered = true; api('update_later', version).catch(() => {}); } },
      { label: t('Skip this version'), run: async () => { answered = true; await api('update_skip', version); toast(t('Version {version} skipped. "Check now" in Settings → Updates still finds it.', { version })); } },
      { label: t('Restart and install'), kind: 'primary', run: async (ev) => {
        if (!ev || !ev.isTrusted) return false;
        await api('update_install', version, token);
        answered = true;
      } },
    ],
    onclose: () => {
      updateUI.prompt = null;
      if (!answered) api('update_later', version).catch(() => {});
    },
  });
  m.box.classList.add('update-dialog');
  updateUI.prompt = { version, box: m.box, close: () => { answered = true; m.close(); } };
}

function renderUpdateActions() {
  const box = updateUI.actionsEl;
  const s = updateUI.status;
  if (!box || !box.isConnected || !s) return;
  clear(box);
  if (s.state === 'ready' && !s.prompt) {
    box.appendChild(h('button', { class: 'btn small primary', text: t('Install…'), onclick: () => updatePrompt(s) }));
  }
  box.appendChild(h('button', {
    class: 'btn small', text: t('Check now'), disabled: !s.configured || ['checking', 'downloading', 'installing'].includes(s.state),
    onclick: async () => {
      try { updateChanged(await api('update_check')); } catch (e) { toastError(e); }
    },
  }));
}

async function renderUpdates(sec) {
  const s = await api('update_status');
  const p = settingsUI.data.prefs;
  sec.appendChild(sectionIntro(`${st.status ? st.status.product : ''} ${s.current} · ${t('the version you are running')}`));
  const auto = toggle(p.update_check);
  updateUI.statusEl = h('div', { class: 'update-status desc', role: 'status', 'aria-live': 'polite' });
  updateUI.actionsEl = h('div', { class: 'row' });
  sec.appendChild(formSection(null, [
    settingRow(t('Check for updates automatically'),
      t('At startup and every 24 hours. Each check contacts the update server, which sees your IP address. Nothing is installed until you choose "Restart and install".'),
      auto, async () => { const v = await api('set_setting', 'update_check', auto.checked); p.update_check = v.value; }),
    h('div', { class: 'setting-row' }, h('div', null, h('div', { text: t('Status') }), updateUI.statusEl), updateUI.actionsEl),
    s.test_source ? h('p', { class: 'small muted', text: t('Using a test update source on this computer (set in the environment).') }) : null]));
  updateChanged(s);
}
