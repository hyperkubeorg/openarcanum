// Bridge to Python. Every call goes through window.pywebview.api (async) and
// comes back as {ok, value} or {ok: false, error}. Python pushes events by
// calling window.__openarcanumEvent({event, data}). Nothing is stored in the
// webview: anything that must persist goes to Python.
'use strict';

const bridge = {
  listeners: {},
  dialogs: {},
  nextDialog: 1,
  ready: null,
};

bridge.ready = new Promise((resolve) => {
  if (window.pywebview && window.pywebview.api) resolve();
  else window.addEventListener('pywebviewready', () => resolve(), { once: true });
});

class ApiError extends Error {}

async function api(method, ...args) {
  await bridge.ready;
  const fn = window.pywebview.api[method];
  if (typeof fn !== 'function') throw new ApiError(`missing API method ${method}`); // i18n: not shown (a programming error, logged)
  const res = await fn(...args);
  if (res && res.ok) return res.value;
  throw new ApiError((res && res.error) || t('Something went wrong.'));
}

function on(event, fn) {
  (bridge.listeners[event] = bridge.listeners[event] || []).push(fn);
}

window.__openarcanumEvent = function (msg) {
  if (!msg || !msg.event) return;
  if (msg.event === 'dialog') {
    const cb = bridge.dialogs[msg.data.id];
    delete bridge.dialogs[msg.data.id];
    if (cb) cb(msg.data.path || '', msg.data.paths || []);
    return;
  }
  for (const fn of bridge.listeners[msg.event] || []) {
    try { fn(msg.data); } catch (e) { console.error('event handler', msg.event, e); }
  }
};

// Native file dialogs run on a Python daemon thread; the path arrives as an
// event. Python reads and writes only paths chosen in its own dialogs
// (api.PickedPaths), so a typed path goes through one too: `start` opens the
// dialog on it to be confirmed (or resolves at once, without a dialog, when
// that path was already chosen in one).
function pickFile(kind = 'any', start = '') {
  const id = String(bridge.nextDialog++);
  return new Promise((resolve) => {
    bridge.dialogs[id] = resolve;
    api('pick_file', id, kind, String(start || '').trim()).catch(() => resolve(''));
  });
}

// Several files at once: resolves to a list of paths (empty if cancelled).
function pickFiles() {
  const id = String(bridge.nextDialog++);
  return new Promise((resolve) => {
    bridge.dialogs[id] = (path, paths) => resolve(paths || (path ? [path] : []));
    api('pick_file', id, 'files').catch(() => resolve([]));
  });
}

function pickSave(kind = 'any', defaultName = '', start = '') {
  const id = String(bridge.nextDialog++);
  return new Promise((resolve) => {
    bridge.dialogs[id] = resolve;
    api('pick_save', id, kind, defaultName, String(start || '').trim()).catch(() => resolve(''));
  });
}

// Every typed path in a list confirmed in a file dialog (each one already
// chosen in a dialog passes at once); cancelled ones are left out.
async function confirmOpenAll(paths, kind = 'any') {
  const out = [];
  for (const p of paths) { const ok = await pickFile(kind, p); if (ok && !out.includes(ok)) out.push(ok); }
  return out;
}

// Nothing is ever dropped on the page: a dropped file or link would navigate
// the window away from the app (Python refuses that too, app.harden_webview).
for (const type of ['dragenter', 'dragover', 'drop']) {
  window.addEventListener(type, (e) => {
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'none';
  }, true);
}

// Ctrl/Cmd+Shift+I opens local DevTools (only when the app was started with
// --debug or OPENARCANUM_DEVTOOLS=1; Python ignores it otherwise). Caught here because the web view
// swallows most keys before a Qt shortcut would see them.
document.addEventListener('keydown', (e) => {
  if ((e.key === 'i' || e.key === 'I') && (e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey) {
    e.preventDefault();
    api('open_devtools').catch(() => {});
  }
}, true);
