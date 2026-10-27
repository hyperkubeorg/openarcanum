# OpenArcanum web client

OpenArcanum in a browser, at https://openarcanum.org/app/. It is the desktop
app's Python core (`../desktop/src/openarcanum`) and interface
(`../shared/ui`), unchanged, running under [Pyodide](https://pyodide.org/)
in a Web Worker. Nothing here reimplements the protocol or the cryptography.

## How it fits together

- `web.js` runs in the page, before the shared UI's `bridge.js`. It gives the
  UI a `window.pywebview.api` that forwards every call to the worker, and
  hands Python's events to `window.__openarcanumEvent`, the same way the
  desktop window does.
- `worker.js` loads Pyodide, the packages and the app, and keeps the profile
  folder (`/home/web`) in the browser's IndexedDB.
- `python/openarcanum_web` starts the core and stands in for the desktop's
  window layer: one thread instead of a loop thread, HTTP through
  `pyodide_http`, file pickers and downloads through the page.
- `python/websockets` is the part of the websockets API the client uses,
  on the browser's WebSocket. `python/keyring` has no backend, so the unlock
  key is kept as a file in the profile (Settings → Security).

What the browser can't do: host a community (run the community server
program) or open folders on the computer. Hosting on your own server and
claiming it here works.

## Trust

The page and its code come from the site on every visit, so whoever controls
the site (or its TLS certificate) controls the client. The desktop app is
installed once and checked. The profile, keys included, is in the browser's
storage for this site, encrypted under your passphrase unless "stay signed
in" keeps the key next to it. See `../../docs/threat-model.md`.

## Building

```sh
clients/web/build-vodozemac.sh /tmp/wheels        # Python 3.14 and Rust 1.93.0 (see the script)
python3 clients/web/build.py --vodozemac /tmp/wheels/vodozemac-*_wasm32.whl --out /tmp/webclient
```

`build.py` downloads Pyodide and the wheels it needs, checks each against a
pinned hash, and writes a folder of static files. The site serves it at
/app/ when `webclient_dir` names it; the site image
(`deploy/container/Containerfile`, stage `webclient`) builds it the same way.

To try a build locally, serve the folder over http on localhost with the
`Content-Security-Policy` the site sends (`WebClientCSP` in
`internal/site/server.go`), and make sure `.mjs` is served as JavaScript and
`.wasm` as `application/wasm`.

Pyodide, vodozemac and the protobuf and spake2 wheels are pinned in
`build.py` and `build-vodozemac.sh`; keep them in step with
`../desktop/uv.lock`.
