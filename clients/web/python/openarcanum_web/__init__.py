"""The desktop client's core, run in a browser Web Worker under Pyodide.

worker.js loads Pyodide, unpacks the openarcanum package and this one, then
calls `boot` once and `call` for every bridge call the page makes. The page
(web.js) gives the shared UI a `window.pywebview.api` that forwards here, so
api.JsApi, the core and the UI are the desktop's, unchanged. What differs:

- One thread. `WebLoop` stands in for api.LoopThread: `run` hands back the
  pending task and `call` (below) awaits it, instead of a bridge thread
  blocking on the core's loop. asyncio.to_thread runs the function inline:
  the worker is not the page's thread, so blocking it doesn't freeze the UI.
- HTTP goes through pyodide_http (synchronous XHR, allowed in a worker), so
  meta.py's urllib calls work as they are; WebSockets through the shim in
  clients/web/python/websockets.
- Files live in the worker's filesystem; /home/web is kept in the browser's
  IndexedDB (worker.js syncs it). A file picked in the page is copied into
  /picked, and what the core saves under /downloads is handed to the browser
  as a download.
"""

from __future__ import annotations

import asyncio
import concurrent.futures
import hashlib
import inspect
import json
import logging
import os
import sys
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

import js
import pyodide_http
from pyodide.ffi import to_js

log = logging.getLogger("openarcanum.web")

HOME = Path("/home/web")
PICKED = Path("/picked")
DOWNLOADS = Path("/downloads")


def post(kind: str, **data: Any) -> None:
    js.postMessage(to_js({"type": kind, **data}, dict_converter=js.Object.fromEntries))


async def _inline(func, /, *args, **kwargs):
    return func(*args, **kwargs)


def _raise_http_errors() -> None:
    """urllib raises HTTPError for a 4xx or 5xx reply, and the client relies
    on it (meta.py reads the status, the body and the headers of an
    error). pyodide_http's urlopen returns those replies like any other, so
    wrap it to raise as urllib does."""
    import io
    import urllib.error
    import urllib.request

    plain = urllib.request.urlopen

    def urlopen(url, *args, **kwargs):
        resp = plain(url, *args, **kwargs)
        status = getattr(resp, "status", None) or getattr(resp, "code", 200)
        if status >= 400:
            full = url.full_url if isinstance(url, urllib.request.Request) else str(url)
            body = resp.read()
            raise urllib.error.HTTPError(
                full, status, getattr(resp, "reason", "") or "", resp.headers, io.BytesIO(body)
            )
        return resp

    urllib.request.urlopen = urlopen


def _scrypt(password: bytes, *, salt: bytes, n: int, r: int, p: int, maxmem: int = 0, dklen: int = 64) -> bytes:
    """hashlib.scrypt, which Pyodide's OpenSSL build leaves out; the same
    function (RFC 7914) from cryptography, so vaults open on either client."""
    from cryptography.hazmat.primitives.kdf.scrypt import Scrypt

    return Scrypt(salt=salt, length=dklen, n=n, r=r, p=p).derive(password)


class WebLoop:
    """api.LoopThread for a single thread (see the module docstring)."""

    closed = False

    @property
    def loop(self) -> asyncio.AbstractEventLoop:
        return asyncio.get_event_loop()

    def run(self, coro, timeout: float):
        return asyncio.ensure_future(asyncio.wait_for(coro, timeout))

    def call(self, fn: Callable, timeout: float = 10.0):
        return fn()

    def submit(self, coro, done: Callable[[], None] | None = None) -> None:
        def finished(task: asyncio.Task) -> None:
            if not task.cancelled() and task.exception() is not None:
                log.error("background call failed", exc_info=task.exception())
            if done is not None:
                done()

        asyncio.ensure_future(coro).add_done_callback(finished)

    def close(self, timeout: float = 5.0) -> None:
        self.closed = True


class WebBus:
    """bus.Bus without the sender thread: events go straight to the page,
    which holds them until the UI has loaded."""

    def set_window(self, window) -> None:
        """No window object: events go through postMessage."""

    def mark_ready(self) -> None:
        """web.js holds events until the UI has loaded."""

    def push(self, event: str, data: Any = None) -> None:
        from openarcanum.bus import _json_default

        try:
            body = json.dumps({"event": event, "data": data}, default=_json_default)
        except TypeError, ValueError:
            log.exception("event %s not serializable", event)
            return
        post("event", body=body)


ACCEPT = {"image": "image/png,image/jpeg,image/gif,image/webp", "backup": ".fbak"}


class WebGui:
    """api.GuiHooks in the browser."""

    def __init__(self, bus: WebBus, invite_link: str | None):
        self.bus = bus
        self._invite = invite_link
        self._waiting: dict[str, Callable[[list[str]], None] | None] = {}

    def open_file_dialog(self, request_id, kind, save, default_name="", start="", grant=None) -> None:
        from openarcanum.names import safe_name

        if save or kind == "folder":
            # The browser asks where to put a download itself; the core
            # writes here and the page hands the file over (flush_downloads).
            folder = DOWNLOADS / safe_name(str(request_id))
            folder.mkdir(parents=True, exist_ok=True)
            path = str(folder if kind == "folder" else folder / (safe_name(default_name) or "download"))
            self._answer(request_id, [path], grant)
            return
        self._waiting[str(request_id)] = grant
        post("pick", id=str(request_id), multiple=kind == "files", accept=ACCEPT.get(kind, ""))

    def picked(self, request_id: str, files: list[tuple[str, bytes]]) -> None:
        from openarcanum.names import safe_name

        if request_id not in self._waiting:
            return
        grant = self._waiting.pop(request_id)
        folder = PICKED / safe_name(request_id)
        folder.mkdir(parents=True, exist_ok=True)
        paths = []
        for name, data in files:
            p = folder / (safe_name(name) or "file")
            p.write_bytes(data)
            paths.append(str(p))
        self._answer(request_id, paths, grant)

    def _answer(self, request_id, paths: list[str], grant) -> None:
        if grant is not None:
            grant(paths)
        self.bus.push("dialog", {"id": request_id, "path": paths[0] if paths else "", "paths": paths})

    def copy_text(self, text: str) -> bool:
        post("copy", text=str(text))
        return True

    def open_folder(self, path: str) -> None:
        """A page can't open a folder on the computer."""

    def open_devtools(self) -> None:
        """The browser has its own developer tools."""

    def smoke_ready(self, info: dict) -> None:
        """Only the desktop smoke test (--smoke) waits for this."""

    def notify(self, title: str, body: str) -> None:
        post("notify", title=str(title), body=str(body))

    def ui_loaded(self) -> None:
        """web.js removes its loading screen when the worker has started."""

    def media_access(self, allowed: bool) -> None:
        """The browser asks for the microphone and camera itself."""

    def screen_pick(self, kind: str, index: int) -> None:
        """The browser shows its own screen picker for screen sharing."""

    def take_invite_link(self) -> str | None:
        link, self._invite = self._invite, None
        return link

    def quit(self) -> None:
        """Closing the tab is quitting."""

    def confirm_quit(self) -> None:
        """Nothing is hosted in the browser, so nothing to warn about."""

    def attention(self, mentions: int, direct: int, review: int) -> None:
        post("attention", count=int(mentions) + int(direct) + int(review))

    def tray_status(self) -> dict:
        return {"available": False, "reason": ""}


_api = None
_gui: WebGui | None = None
_allowed: frozenset[str] = frozenset()
_seen: dict[str, tuple[int, float]] = {}


def boot(lang: str = "", invite_link: str = "") -> None:
    global _api, _gui, _allowed
    os.environ["HOME"] = str(HOME)
    if lang:
        os.environ.setdefault("LANG", lang.replace("-", "_") + ".UTF-8")
    logging.basicConfig(level=logging.INFO, stream=sys.stderr, format="%(levelname)s %(name)s: %(message)s")
    pyodide_http.patch_all()
    _raise_http_errors()
    asyncio.to_thread = _inline
    if not hasattr(hashlib, "scrypt"):
        hashlib.scrypt = _scrypt
    for d in (PICKED, DOWNLOADS):
        d.mkdir(parents=True, exist_ok=True)

    from openarcanum import api as api_mod
    from openarcanum.core import Core
    from openarcanum.paths import Paths

    paths = Paths.under(HOME)
    paths.ensure()
    bus = WebBus()
    _gui = WebGui(bus, invite_link or None)
    loop = WebLoop()
    core = Core(paths, emit=bus.push)
    gui = _gui

    # As app.run: the core's notify events also become notifications.
    def forward(event: str, data: Any) -> None:
        bus.push(event, data)
        if event == "notify":
            gui.notify(data.get("title", "OpenArcanum"), data.get("body", ""))

    core.emit = forward
    core.media_access = gui.media_access
    # The maintainer networks and whether their nodes serve the public
    # directory (the app hides "Find a community" when none does). No
    # updater (the site serves the current release) and no hosting here.
    core.start_clusters()
    _api = api_mod.JsApi(loop, core, bus, _gui)
    _allowed = api_mod.bridge_methods()
    log.info("web client started")


def _fail(e: BaseException) -> dict:
    if isinstance(e, asyncio.CancelledError):
        e = concurrent.futures.CancelledError()

    def raise_it():
        raise e

    return _api._wrap(raise_it)


async def call(name: str, args_json: str) -> str:
    """One bridge call from the page; the {ok, value} / {ok, error} reply as JSON."""
    from openarcanum import api as api_mod
    from openarcanum.bus import _json_default

    if not api_mod.bridge_allows(name, _allowed):
        reply = {"ok": False, "error": "unknown method"}
    else:
        try:
            args = json.loads(args_json)
            reply = getattr(_api, name)(*args)
        except Exception as e:
            reply = _fail(e)
        value = reply.get("value") if isinstance(reply, dict) and reply.get("ok") else None
        if inspect.isawaitable(value):
            try:
                reply = {"ok": True, "value": await value}
            except BaseException as e:
                reply = _fail(e)
    return json.dumps(reply, default=_json_default)


def picked(request_id: str, names, datas) -> None:
    files = [(str(n), d.to_bytes()) for n, d in zip(names, datas, strict=True)]
    _gui.picked(str(request_id), files)


def flush_downloads() -> None:
    """Hand the browser each file the core finished writing under /downloads:
    one whose size and time haven't changed since the last look."""
    now = {}
    for p in DOWNLOADS.rglob("*"):
        if p.is_file():
            st = p.stat()
            now[str(p)] = (st.st_size, st.st_mtime)
    for path, sig in now.items():
        if _seen.get(path) == sig and time.time() - sig[1] > 1:
            p = Path(path)
            post("download", name=p.name, data=to_js(p.read_bytes()))
            p.unlink()
            _seen.pop(path, None)
        else:
            _seen[path] = sig
