// The browser's stand-in for pywebview: window.pywebview.api forwards every
// bridge call to the Python core in worker.js, and Python's events reach
// bridge.js's window.__openarcanumEvent. Loaded before bridge.js.
'use strict';

(function () {
  window.openarcanumWeb = true;
  const worker = new Worker('worker.js', { type: 'module' });
  const pending = new Map();
  const held = [];
  let nextId = 1;

  const loading = document.createElement('div');
  loading.id = 'web-loading';
  loading.textContent = 'Starting OpenArcanum…';
  document.addEventListener('DOMContentLoaded', () => document.body.appendChild(loading));

  function deliver(msg) {
    if (typeof window.__openarcanumEvent === 'function') window.__openarcanumEvent(msg);
    else held.push(msg);
  }
  document.addEventListener('DOMContentLoaded', () => { while (held.length) deliver(held.shift()); });

  function pick(id, multiple, accept) {
    const input = document.createElement('input');
    input.type = 'file';
    input.multiple = multiple;
    if (accept) input.accept = accept;
    let answered = false;
    const answer = async (files) => {
      if (answered) return;
      answered = true;
      const names = [];
      const datas = [];
      for (const f of files) {
        names.push(f.name);
        datas.push(new Uint8Array(await f.arrayBuffer()));
      }
      worker.postMessage({ type: 'picked', id, names, datas }, datas.map((d) => d.buffer));
    };
    input.addEventListener('change', () => answer(Array.from(input.files || [])));
    input.addEventListener('cancel', () => answer([]));
    input.click();
  }

  function download(name, data) {
    const url = URL.createObjectURL(new Blob([data]));
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60000);
  }

  worker.onmessage = (e) => {
    const msg = e.data;
    switch (msg.type) {
      case 'reply': {
        const resolve = pending.get(msg.id);
        pending.delete(msg.id);
        if (resolve) resolve(JSON.parse(msg.json));
        break;
      }
      case 'event': deliver(JSON.parse(msg.body)); break;
      case 'started': loading.remove(); break;
      case 'failed': loading.textContent = 'OpenArcanum could not start here (' + msg.error + '). Try reloading the page.'; break;
      case 'pick': pick(msg.id, msg.multiple, msg.accept); break;
      case 'download': download(msg.name, msg.data); break;
      case 'copy': navigator.clipboard.writeText(msg.text).catch(() => {}); break;
      case 'notify':
        if (window.Notification && Notification.permission === 'granted' && document.hidden) {
          new Notification(msg.title, { body: msg.body });
        }
        break;
      case 'attention': document.title = msg.count ? `(${msg.count}) OpenArcanum` : 'OpenArcanum'; break;
    }
  };

  // Notifications (mentions, direct messages while the tab is in the
  // background) need the browser's permission, asked for on a click.
  if (window.Notification && Notification.permission === 'default') {
    document.addEventListener('click', () => { Notification.requestPermission().catch(() => {}); }, { once: true });
  }

  const invite = location.hash.length > 1 ? decodeURIComponent(location.hash.slice(1)) : '';
  if (invite) history.replaceState(null, '', location.pathname);
  worker.postMessage({ type: 'start', lang: navigator.language || '', invite });

  window.pywebview = {
    api: new Proxy({}, {
      get(_target, method) {
        if (typeof method !== 'string') return undefined;
        return (...args) => new Promise((resolve) => {
          const id = nextId++;
          pending.set(id, resolve);
          worker.postMessage({ type: 'call', id, method, args: JSON.stringify(args) });
        });
      },
    }),
  };
})();
