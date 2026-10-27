// The client's Python core, under Pyodide, off the page's thread. web.js
// sends {type: 'start'}, then {type: 'call', id, method, args} for each
// bridge call and {type: 'picked', id, names, datas} for files chosen in the
// page; this answers {type: 'reply', id, json} and forwards what Python posts
// (events, file pickers, downloads, notifications: openarcanum_web).
import { loadPyodide } from './pyodide/pyodide.mjs';

const HOME = '/home/web';
let py = null;
let web = null;
let started = null;
let syncing = null;
let dirtyAgain = false;

// /home/web (the profile, settings and keys) lives in IndexedDB. Writes are
// copied there after every call and every few seconds; one sync at a time.
function persist() {
  if (syncing) { dirtyAgain = true; return syncing; }
  syncing = new Promise((resolve) => {
    py.FS.syncfs(false, (err) => {
      if (err) console.error('saving the profile', err);
      resolve();
    });
  }).then(() => {
    syncing = null;
    if (dirtyAgain) { dirtyAgain = false; return persist(); }
  });
  return syncing;
}

async function start(opts) {
  const manifest = await (await fetch('manifest.json', { cache: 'no-cache' })).json();
  py = await loadPyodide({ indexURL: new URL('pyodide/', self.location).href });
  py.FS.mkdirTree(HOME);
  py.FS.mount(py.FS.filesystems.IDBFS, {}, HOME);
  await new Promise((resolve, reject) => py.FS.syncfs(true, (err) => (err ? reject(err) : resolve())));
  await py.loadPackage(manifest.packages, { checkIntegrity: true });
  await py.loadPackage(manifest.wheels.map((w) => new URL('wheels/' + w, self.location).href));
  const app = await (await fetch(manifest.app)).arrayBuffer();
  py.unpackArchive(app, 'zip', { extractDir: '/opt/oa' });
  py.runPython("import sys; sys.path[:0] = ['/opt/oa/clients/web/python', '/opt/oa/clients/desktop/src']");
  web = py.pyimport('openarcanum_web');
  web.boot(opts.lang || '', opts.invite || '');
  setInterval(() => {
    try { web.flush_downloads(); } catch (e) { console.error('downloads', e); }
    persist();
  }, 2000);
}

self.onmessage = async (e) => {
  const msg = e.data;
  if (msg.type === 'start') {
    started = start(msg);
    try {
      await started;
      self.postMessage({ type: 'started' });
    } catch (err) {
      console.error(err);
      self.postMessage({ type: 'failed', error: String(err && err.message || err) });
    }
    return;
  }
  await started;
  if (msg.type === 'call') {
    let json;
    try {
      json = await web.call(msg.method, msg.args);
    } catch (err) {
      console.error('bridge call', msg.method, err);
      json = JSON.stringify({ ok: false, error: 'Something went wrong.' });
    }
    self.postMessage({ type: 'reply', id: msg.id, json });
    persist();
  } else if (msg.type === 'picked') {
    web.picked(msg.id, msg.names, msg.datas);
    persist();
  }
};
