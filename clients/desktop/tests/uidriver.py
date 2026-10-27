"""Drives the real client window for UI tests.

The app runs as a normal process (`python -m openarcanum --home DIR`): on
Linux under Xvfb with a private session bus, on Windows and macOS on the
desktop session (CI runners have one), with Chromium's remote debugging
turned on through the environment (`QTWEBENGINE_REMOTE_DEBUGGING`), which the
app keeps only when started with `--allow-remote-debugging` (a documented
developer option; the app has no test hooks). Clicks and keys are trusted
input events sent through the DevTools protocol's Input
domain: they go through Chromium's input pipeline exactly like a user's
(hit testing, focus, default actions), unlike `element.click()` in page
script. Page state is read with Runtime.evaluate.
"""

from __future__ import annotations

import asyncio
import contextlib
import itertools
import json
import os
import shlex
import signal
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

import websockets
from conftest import free_port, headless

# Finds the visible element to act on: by CSS selector, optionally narrowed to
# the one whose trimmed text (or aria-label) equals `text`, scrolled into view.
FIND = """
(sel, text, nth) => {
  const vis = (e) => { const r = e.getBoundingClientRect(); const s = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'; };
  const label = (e) => (e.getAttribute('aria-label') || e.textContent || e.value || '').trim();
  let els = [...document.querySelectorAll(sel)].filter(vis);
  if (text !== null) els = els.filter((e) => label(e) === text);
  const modal = document.querySelector('#modal-root .backdrop:last-child');
  if (modal) { const inModal = els.filter((e) => modal.contains(e)); if (inModal.length) els = inModal; }
  const e = els[nth < 0 ? els.length + nth : nth];
  if (!e) return null;
  e.scrollIntoView({ block: 'center', inline: 'center' });
  // Inline text that wraps has a gap between its lines at the middle of its
  // bounding box: aim at its first line instead.
  const rects = e.getClientRects();
  const r = rects.length > 1 ? rects[0] : e.getBoundingClientRect();
  const x = r.left + r.width / 2, y = r.top + r.height / 2;
  // Something on top (a toast over a modal's buttons) would get the click:
  // report it as not clickable yet, as a user would wait for it to go.
  const top = document.elementFromPoint(x, y);
  const covered = !!top && top !== e && !e.contains(top);
  const desc = (t) => !t ? '' : t.tagName.toLowerCase() + (t.id ? '#' + t.id : '')
    + (t.className ? '.' + String(t.className).trim().split(' ').join('.') : '');
  return { x, y, disabled: !!e.disabled || covered, coveredBy: covered ? desc(top) : '' };
}
"""

KEYS = {
    "Enter": {"code": "Enter", "windowsVirtualKeyCode": 13, "text": "\r"},
    "Escape": {"code": "Escape", "windowsVirtualKeyCode": 27},
    "Tab": {"code": "Tab", "windowsVirtualKeyCode": 9},
    "Backspace": {"code": "Backspace", "windowsVirtualKeyCode": 8},
    "ArrowDown": {"code": "ArrowDown", "windowsVirtualKeyCode": 40},
    "ArrowUp": {"code": "ArrowUp", "windowsVirtualKeyCode": 38},
    " ": {"code": "Space", "windowsVirtualKeyCode": 32, "text": " "},
}


class UIError(AssertionError):
    pass


# Every app started, so a failed or timed-out test can't leave windows behind
# (conftest kills whatever is still running after each test).
RUNNING: set[App] = set()


class App:
    """One running client with its own --home, driven over DevTools."""

    def __init__(
        self,
        home: Path,
        env: dict | None = None,
        name: str = "app",
        session_helper: list[str] | None = None,
        args: list[str] | None = None,
        session_ready: Path | None = None,
    ):
        # A program to start in the app's own session (bus and display)
        # before the app, e.g. a stand-in desktop service; with
        # session_ready, the app starts only once the helper has written
        # that file (a service the app looks for at startup, like the tray).
        self.session_helper = session_helper
        self.session_ready = session_ready
        self.home = home
        self.name = name
        self.args = list(args or [])
        self.port = free_port()
        # No real keychain unless a test brings its own (staying signed in
        # is the default, conftest._no_os_keychain): the key goes to the key
        # file under this app's --home.
        self.env = dict(
            os.environ,
            QTWEBENGINE_REMOTE_DEBUGGING=f"127.0.0.1:{self.port}",
            # File dialogs can't be driven: each one answers with the path
            # typed in the page (app.answer_dialogs_for_tests).
            **{
                "PYTHON_KEYRING_BACKEND": "keyring.backends.fail.Keyring",
                "OPENARCANUM_TEST_FILE_DIALOGS": "accept",
                **(env or {}),
            },
        )
        self.proc: subprocess.Popen | None = None
        self.ws = None
        self.ids = itertools.count(1)
        self.pending: dict[int, asyncio.Future] = {}
        self.errors: list[str] = []
        # Every URL the page asked to load (Network domain), for the
        # "no remote content" check.
        self.requests: list[str] = []
        self.reader: asyncio.Task | None = None

    async def start(self, timeout: float = 90.0) -> App:
        RUNNING.add(self)
        cmd = [sys.executable, "-m", "openarcanum", "--home", str(self.home), "--allow-remote-debugging", *self.args]
        if self.session_helper:
            # Linux only (a stand-in service on the app's own session bus).
            assert sys.platform.startswith("linux"), "session helpers need Linux's private session bus"
            wait = ""
            if self.session_ready is not None:
                ready = shlex.quote(str(self.session_ready))
                wait = f"while [ ! -e {ready} ]; do sleep 0.05; done; "
            cmd = ["sh", "-c", f"{shlex.join(self.session_helper)} & {wait}exec {shlex.join(cmd)}"]
        # Its own process group, so the whole tree (the engine's helper
        # processes too) can be ended at once.
        group = (
            {"creationflags": subprocess.CREATE_NEW_PROCESS_GROUP} if os.name == "nt" else {"start_new_session": True}
        )
        # The engine's console warnings go to a file, never a pipe nobody
        # reads: a full pipe blocks the app's GUI thread (and with it
        # DevTools) in the middle of a test.
        self.stderr_log = self.home.parent / f"{self.name}-stderr.log"
        self.stderr_log.parent.mkdir(parents=True, exist_ok=True)
        with open(self.stderr_log, "ab") as err:
            self.proc = subprocess.Popen(
                headless(cmd),
                env=self.env,
                stdout=subprocess.DEVNULL,
                stderr=err,
                **group,
            )
        deadline = time.monotonic() + timeout
        url = None
        while time.monotonic() < deadline and url is None:
            if self.proc.poll() is not None:
                err = self.stderr_log.read_text(encoding="utf-8", errors="replace")[-2000:]
                raise UIError(f"{self.name} exited at start: {err}")
            with (
                contextlib.suppress(OSError, ValueError),
                urllib.request.urlopen(f"http://127.0.0.1:{self.port}/json", timeout=1) as r,
            ):
                pages = [p for p in json.load(r) if p.get("type") == "page" and p["url"].startswith("file:")]
                if pages:
                    url = pages[0]["webSocketDebuggerUrl"]
            if url is None:
                await asyncio.sleep(0.2)
        if url is None:
            raise UIError(f"{self.name}: DevTools never came up")
        self.ws = await websockets.connect(url, max_size=None)
        self.reader = asyncio.ensure_future(self._read())
        await self.send("Runtime.enable")
        await self.send("Network.enable")
        await self.wait_js(
            "window.pywebview && window.pywebview.api && document.readyState === 'complete'", "the bridge"
        )
        # What loaded before Network.enable took effect (the page itself and
        # its scripts and styles, often all of them): the page's own record.
        self.requests += await self.js(
            "[location.href, ...performance.getEntriesByType('resource').map((e) => e.name)]"
        )
        return self

    async def _read(self) -> None:
        async for raw in self.ws:
            msg = json.loads(raw)
            if "id" in msg and msg["id"] in self.pending:
                self.pending.pop(msg["id"]).set_result(msg)
            elif msg.get("method") == "Runtime.exceptionThrown":
                d = msg["params"]["exceptionDetails"]
                self.errors.append((d.get("exception") or {}).get("description") or d.get("text", "error"))
            elif msg.get("method") == "Network.requestWillBeSent":
                self.requests.append(msg["params"]["request"]["url"])
            elif msg.get("method") == "Runtime.consoleAPICalled" and msg["params"]["type"] == "error":
                args = msg["params"]["args"]
                self.errors.append(" ".join(str(a.get("value", a.get("description", ""))) for a in args))

    async def send(self, method: str, **params):
        i = next(self.ids)
        fut = asyncio.get_running_loop().create_future()
        self.pending[i] = fut
        await self.ws.send(json.dumps({"id": i, "method": method, "params": params}))
        res = await asyncio.wait_for(fut, 30)
        if "error" in res:
            raise UIError(f"{method}: {res['error']}")
        return res.get("result", {})

    async def js(self, expr: str):
        res = await self.send("Runtime.evaluate", expression=expr, awaitPromise=True, returnByValue=True)
        if "exceptionDetails" in res:
            raise UIError(f"js failed: {res['exceptionDetails'].get('text')} in {expr[:120]}")
        return res.get("result", {}).get("value")

    async def call(self, fn: str, *args):
        return await self.js(f"({fn})(...{json.dumps(list(args))})")

    async def wait_js(self, expr: str, what: str, timeout: float = 20.0):
        deadline = time.monotonic() + timeout
        while True:
            with contextlib.suppress(UIError):
                if await self.js(f"!!({expr})"):
                    return True
            if time.monotonic() > deadline:
                await self._fail_shot(what)
                raise UIError(f"{self.name}: timed out waiting for {what}")
            await asyncio.sleep(0.1)

    async def find(self, sel: str, text: str | None = None, nth: int = 0, timeout: float = 20.0) -> dict:
        deadline = time.monotonic() + timeout
        while True:
            r = await self.call(FIND, sel, text, nth)
            if r and not r["disabled"]:
                return r
            if time.monotonic() > deadline:
                await self._fail_shot(sel)
                why = f" (covered by {r['coveredBy']})" if r and r.get("coveredBy") else (" (disabled)" if r else "")
                raise UIError(f"{self.name}: no clickable {sel!r}" + (f" with text {text!r}" if text else "") + why)
            await asyncio.sleep(0.1)

    async def exists(self, sel: str, text: str | None = None) -> bool:
        return bool(await self.call(FIND, sel, text, 0))

    async def click(self, sel: str, text: str | None = None, nth: int = 0, timeout: float = 20.0) -> None:
        r = await self.find(sel, text, nth, timeout)
        x, y = r["x"], r["y"]
        await self.send("Input.dispatchMouseEvent", type="mouseMoved", x=x, y=y)
        await self.send("Input.dispatchMouseEvent", type="mousePressed", x=x, y=y, button="left", clickCount=1)
        await self.send("Input.dispatchMouseEvent", type="mouseReleased", x=x, y=y, button="left", clickCount=1)
        await asyncio.sleep(0.05)

    async def right_click(self, sel: str, text: str | None = None, nth: int = 0, timeout: float = 20.0) -> None:
        """A secondary-button click (opens context menus)."""
        r = await self.find(sel, text, nth, timeout)
        x, y = r["x"], r["y"]
        await self.send("Input.dispatchMouseEvent", type="mouseMoved", x=x, y=y)
        await self.send("Input.dispatchMouseEvent", type="mousePressed", x=x, y=y, button="right", clickCount=1)
        await self.send("Input.dispatchMouseEvent", type="mouseReleased", x=x, y=y, button="right", clickCount=1)
        await asyncio.sleep(0.05)

    async def hover(self, sel: str, text: str | None = None, nth: int = 0, timeout: float = 20.0) -> None:
        r = await self.find(sel, text, nth, timeout)
        await self.send("Input.dispatchMouseEvent", type="mouseMoved", x=r["x"], y=r["y"])
        await asyncio.sleep(0.05)

    async def message_action(self, msg_sel: str, action: str, nth: int = -1) -> None:
        """Hover a message and click one of its action buttons (they're only
        visible on hover or keyboard focus, so only that message's match)."""
        await self.hover(msg_sel, None, nth)
        await self.click(f"{msg_sel} .actions button", action)

    async def button(self, text: str, timeout: float = 20.0) -> None:
        await self.click("button, [role=button], .tab, .item, .mi", text, -1, timeout)

    async def key(self, name: str, modifiers: int = 0) -> None:
        k = KEYS.get(name, {"code": f"Key{name.upper()}" if len(name) == 1 else name})
        down = {"type": "keyDown", "key": name, "modifiers": modifiers, **k}
        up = {"type": "keyUp", "key": name, "modifiers": modifiers, **{a: b for a, b in k.items() if a != "text"}}
        await self.send("Input.dispatchKeyEvent", **down)
        await self.send("Input.dispatchKeyEvent", **up)
        await asyncio.sleep(0.05)

    async def type(self, text: str) -> None:
        """Type into the focused element (as an input method would commit it)."""
        await self.send("Input.insertText", text=text)
        await asyncio.sleep(0.05)

    async def fill(self, sel: str, text: str, label: str | None = None, nth: int = 0) -> None:
        """Click a field, select what's in it, and type over it."""
        await self.click(sel, label, nth)
        await self.js("document.activeElement && document.activeElement.select && document.activeElement.select()")
        await self.type(text)

    async def select(self, sel: str, label: str, nth: int = 0, timeout: float = 20.0) -> None:
        """Choose an option in a <select> with the keyboard, as a user would:
        open it, move to the option, press Enter. Waits for the select and
        its option (a settings page lists devices after asking the engine)."""
        deadline = time.monotonic() + timeout
        while True:
            pos = await self.call(
                "(s, n, l) => { const e = [...document.querySelectorAll(s)][n]; if (!e) return null;"
                " return [e.selectedIndex, [...e.options].findIndex((o) => o.textContent.trim() === l)]; }",
                sel,
                nth,
                label,
            )
            if pos is not None and pos[1] >= 0:
                break
            if time.monotonic() > deadline:
                await self._fail_shot(sel)
                raise UIError(f"{self.name}: no option {label!r} in {sel}" + ("" if pos else " (no such select)"))
            await asyncio.sleep(0.1)
        await self.click(sel, None, nth)
        step = "ArrowDown" if pos[1] >= pos[0] else "ArrowUp"
        for _ in range(abs(pos[1] - pos[0])):
            await self.key(step)
        await self.key("Enter")

    async def texts(self, sel: str) -> list[str]:
        return await self.call("(s) => [...document.querySelectorAll(s)].map((e) => e.textContent.trim())", sel)

    async def wait_text(self, sel: str, text: str, timeout: float = 30.0) -> None:
        deadline = time.monotonic() + timeout
        while True:
            if any(text in t for t in await self.texts(sel)):
                return
            if time.monotonic() > deadline:
                await self._fail_shot(sel)
                raise UIError(f"{self.name}: {text!r} never appeared in {sel} (have {await self.texts(sel)})")
            await asyncio.sleep(0.15)

    async def wait_gone(self, sel: str, text: str | None = None, timeout: float = 20.0) -> None:
        deadline = time.monotonic() + timeout
        while await self.exists(sel, text):
            if time.monotonic() > deadline:
                await self._fail_shot(sel)
                raise UIError(f"{self.name}: {sel} {text or ''} never went away")
            await asyncio.sleep(0.1)

    async def _fail_shot(self, what: str) -> None:
        """Keep a screenshot of the failure next to the profile (for CI artifacts)."""
        with contextlib.suppress(Exception):
            await self.screenshot(self.home.parent / f"{self.name}-failure.png")

    async def screenshot(self, path: Path) -> None:
        import base64

        res = await self.send("Page.captureScreenshot", format="png")
        path.write_bytes(base64.b64decode(res["data"]))

    def assert_local_only(self) -> None:
        """The window only ever loaded the app's own files and data: URIs."""
        remote = [u for u in self.requests if not u.startswith(("file:", "data:", "devtools:"))]
        if remote:
            raise UIError(f"{self.name}: loaded remote content: {remote[:5]}")
        if not self.requests:
            raise UIError(f"{self.name}: no requests were recorded at all")

    def assert_no_errors(self) -> None:
        if self.errors:
            raise UIError(f"{self.name}: JavaScript errors: {self.errors}")

    def kill(self) -> None:
        """Synchronous last resort: end the whole process tree."""
        RUNNING.discard(self)
        if self.proc is not None and self.proc.poll() is None:
            _kill_tree(self.proc)
            self.proc.wait()

    def app_pid(self) -> int | None:
        """The app's own Python process. On Linux the driver starts it under
        dbus-run-session and xvfb-run (which carry the same arguments), so
        it is found in /proc; elsewhere it is the process the driver started."""
        if not sys.platform.startswith("linux"):
            return self.proc.pid if self.proc is not None and self.proc.poll() is None else None
        want = str(self.home).encode()
        for d in Path("/proc").iterdir():
            if not d.name.isdigit():
                continue
            try:
                args = (d / "cmdline").read_bytes().split(b"\0")
            except OSError:
                continue
            python = Path(args[0].decode(errors="replace")).name.startswith("python")
            if python and args[1:3] == [b"-m", b"openarcanum"] and want in args:
                return int(d.name)
        return None

    def terminate(self) -> int | None:
        """Ask the app to close the way the system does (logout, shutdown,
        kill): SIGTERM on Linux and macOS, the sign-out messages to its
        windows on Windows. Returns the app's pid (None if it isn't running)."""
        pid = self.app_pid()
        if pid is None:
            return None
        if os.name == "nt":
            _close_windows(pid)
        else:
            with contextlib.suppress(ProcessLookupError):
                os.kill(pid, signal.SIGTERM)
        return pid

    def wait_exit(self, pid: int, timeout: float) -> float:
        """Seconds until the app process `pid` ended; AssertionError after timeout."""
        start = time.monotonic()
        while time.monotonic() - start < timeout:
            if not _running(pid, self.proc):
                return time.monotonic() - start
            time.sleep(0.05)
        raise AssertionError(f"process {pid} still running after {timeout} s")

    async def stop(self) -> None:
        RUNNING.discard(self)
        if self.ws is not None:
            with contextlib.suppress(Exception):
                await self.ws.close()
        if self.reader is not None:
            self.reader.cancel()
        # Close the app the way the system would (it saves and exits), and
        # only then take its display and bus away.
        pid = self.terminate()
        if pid is not None:
            with contextlib.suppress(AssertionError):
                await asyncio.to_thread(self.wait_exit, pid, 10)
        if self.proc is not None and self.proc.poll() is None:
            if os.name == "nt":
                try:
                    await asyncio.to_thread(self.proc.wait, 10)
                except subprocess.TimeoutExpired:
                    _kill_tree(self.proc)
                    self.proc.wait()
                return
            with contextlib.suppress(ProcessLookupError):
                os.killpg(self.proc.pid, signal.SIGTERM)
            try:
                await asyncio.to_thread(self.proc.wait, 20)
            except subprocess.TimeoutExpired:
                os.killpg(self.proc.pid, signal.SIGKILL)
                self.proc.wait()


def _running(pid: int, proc: subprocess.Popen | None) -> bool:
    if proc is not None and proc.pid == pid:  # our own child (Windows, macOS)
        return proc.poll() is None
    status = Path(f"/proc/{pid}/status")
    try:
        return "zombie" not in status.read_text().lower()
    except OSError:
        return False


def _kill_tree(proc: subprocess.Popen) -> None:
    if os.name == "nt":
        # The engine's helper processes too.
        subprocess.run(["taskkill", "/T", "/F", "/PID", str(proc.pid)], capture_output=True, timeout=30)
        return
    with contextlib.suppress(ProcessLookupError):
        os.killpg(proc.pid, signal.SIGKILL)


def _close_windows(pid: int) -> None:
    """End the process's session the way Windows does when the user signs
    out: WM_QUERYENDSESSION and WM_ENDSESSION to its top-level windows, then
    WM_CLOSE to the visible ones. (WM_CLOSE alone is the user closing the
    window, which with a system tray only hides it there.)"""
    import ctypes
    from ctypes import wintypes

    user32 = ctypes.WinDLL("user32", use_last_error=True)
    user32.GetWindowThreadProcessId.argtypes = [wintypes.HWND, ctypes.POINTER(wintypes.DWORD)]
    user32.SendMessageTimeoutW.argtypes = [
        wintypes.HWND,
        wintypes.UINT,
        wintypes.WPARAM,
        wintypes.LPARAM,
        wintypes.UINT,
        wintypes.UINT,
        ctypes.POINTER(ctypes.c_size_t),
    ]
    user32.PostMessageW.argtypes = [wintypes.HWND, wintypes.UINT, wintypes.WPARAM, wintypes.LPARAM]
    user32.IsWindowVisible.argtypes = [wintypes.HWND]
    wm_close = 0x0010
    wm_queryendsession, wm_endsession = 0x0011, 0x0016
    endsession_logoff = 0x80000000
    smto_abortifhung = 0x0002
    found: list[int] = []
    proto = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)

    def each(hwnd, _lparam):
        owner = wintypes.DWORD()
        user32.GetWindowThreadProcessId(hwnd, ctypes.byref(owner))
        if owner.value == pid:
            found.append(hwnd)
        return True

    user32.EnumWindows(proto(each), 0)
    result = ctypes.c_size_t()
    for msg, wparam in ((wm_queryendsession, 0), (wm_endsession, 1)):
        for hwnd in found:
            user32.SendMessageTimeoutW(
                hwnd, msg, wparam, endsession_logoff, smto_abortifhung, 5000, ctypes.byref(result)
            )
    for hwnd in found:
        if user32.IsWindowVisible(hwnd):
            user32.PostMessageW(hwnd, wm_close, 0, 0)
