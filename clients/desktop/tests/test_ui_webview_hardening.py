"""The window never leaves the app's own page (app.harden_webview) and
DevTools stay off unless asked for (app.devtools_allowed), in the real app.

- script and links can't navigate the main frame anywhere else (another
  local file, a remote page), and the bridge stays the app's;
- window.open opens nothing: no new window, not the system browser;
- dropped files and links never navigate (the page cancels drag events, and
  Qt refuses a drop at the engine);
- the page asking for DevTools opens nothing unless the app was started with
  --debug or OPENARCANUM_DEVTOOLS=1;
- the page reads no local file outside its own folder (its sibling
  scripts, styles and images still load).
"""

from __future__ import annotations

import asyncio
import base64
import json
import os
import stat
import subprocess
import sys
import textwrap
import urllib.request

import pytest
from uidriver import App

pytestmark = [pytest.mark.gui, pytest.mark.timeout(240)]


def _targets(app: App) -> list[dict]:
    with urllib.request.urlopen(f"http://127.0.0.1:{app.port}/json", timeout=5) as r:
        return json.load(r)


def _browser(tmp_path):
    """A stand-in system browser (webbrowser honours $BROWSER) that records
    being started."""
    opened = tmp_path / "browser-opened"
    if os.name == "nt":
        script = tmp_path / "browser.cmd"
        script.write_text(f'@echo %* >> "{opened}"\n')
    else:
        script = tmp_path / "browser.sh"
        script.write_text(f'#!/bin/sh\necho "$@" >> "{opened}"\n')
        script.chmod(script.stat().st_mode | stat.S_IEXEC)
    return {"BROWSER": str(script)}, opened


async def _still_home(app: App, index: str) -> None:
    await asyncio.sleep(1.5)
    assert await app.js("location.href.split('#')[0]") == index
    assert await app.js("document.title") != "EVIL"
    assert await app.js("typeof window.pywebview.api.status === 'function'")


def test_the_window_stays_on_the_apps_page(tmp_path):
    evil = tmp_path / "evil.html"
    evil.write_text("<!doctype html><title>EVIL</title><body>evil</body>")
    env, opened = _browser(tmp_path)

    async def go():
        app = await App(tmp_path / "olive", name="olive", env=env).start()
        try:
            index = await app.js("location.href.split('#')[0]")
            assert index.startswith("file:") and index.endswith("/index.html")
            # Script navigating the main frame: another local file, a remote page.
            for target in (evil.as_uri(), "https://example.invalid/", "data:text/html,<title>EVIL</title>"):
                await app.js(f"location.href = {json.dumps(target)}; 0")
                await _still_home(app, index)
            # A link the user (or script) clicks, and a form posted elsewhere.
            await app.js(
                f"(() => {{ const a = document.createElement('a'); a.href = {json.dumps(evil.as_uri())};"
                " document.body.appendChild(a); a.click(); a.remove(); return 0; })()"
            )
            await _still_home(app, index)
            await app.js(
                "(() => { const f = document.createElement('form'); f.method = 'post';"
                " f.action = 'https://example.invalid/x'; document.body.appendChild(f); f.submit(); return 0; })()"
            )
            await _still_home(app, index)
            # A new window: nothing opens, not even the system browser.
            await app.js("window.open('https://example.invalid/'); 0")
            await app.js(f"window.open({json.dumps(evil.as_uri())}, '_blank', 'popup'); 0")
            await app.js(
                "(() => { const a = document.createElement('a'); a.href = 'https://example.invalid/';"
                " a.target = '_blank'; document.body.appendChild(a); a.click(); a.remove(); return 0; })()"
            )
            await asyncio.sleep(1.5)
            pages = [t for t in _targets(app) if t.get("type") == "page"]
            assert [t["url"].split("#")[0] for t in pages] == [index], pages
            assert not opened.exists(), opened.read_text()
            # Drops: the page cancels every drag event (so nothing dropped is
            # opened), and a drop the engine is handed doesn't navigate.
            for kind in ("dragenter", "dragover", "drop"):
                assert await app.js(
                    f"(() => {{ const e = new DragEvent('{kind}', {{ bubbles: true, cancelable: true }});"
                    " document.body.dispatchEvent(e); return e.defaultPrevented; })()"
                ), kind
            data = {"items": [{"mimeType": "text/uri-list", "data": evil.as_uri()}], "dragOperationsMask": 1}
            for kind in ("dragEnter", "dragOver", "drop"):
                await app.send("Input.dispatchDragEvent", type=kind, x=200, y=200, data=data)
            await _still_home(app, index)
            log = (tmp_path / "olive" / "log" / "client.log").read_text(encoding="utf-8")
            assert "refused a navigation away from the app's page" in log
            assert "refused a new window from the page" in log
            app.assert_no_errors()
        finally:
            await app.stop()

    asyncio.run(go())


PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
)


def test_the_page_reads_no_local_file_outside_its_own_folder(tmp_path):
    """Script in the page can't read other files on disk: a file:// image
    from outside the UI's folder fails to load and a file:// fetch fails,
    while the page's own scripts, styles and brand images load as before."""
    outside = tmp_path / "outside.png"
    outside.write_bytes(PNG)

    async def go():
        app = await App(tmp_path / "olive", name="olive").start()
        try:
            await app.wait_js("document.querySelector('#lock')", "the lock screen")
            # The page's own files loaded: its scripts ran and its styles apply.
            assert await app.js("typeof render === 'function' && typeof lockNow === 'function'")
            assert await app.js("getComputedStyle(document.body).backgroundColor") != "rgba(0, 0, 0, 0)"
            r = await app.call(
                """
                async (outside) => {
                  const load = (src) => new Promise((r) => {
                    const i = new Image(); i.onload = () => r('loaded'); i.onerror = () => r('error'); i.src = src;
                  });
                  const read = (u) => fetch(u).then((x) => 'ok ' + x.status, (e) => 'failed');
                  const brand = new URL('brand/openarcanum-32.png', location.href).href;
                  return [await load(brand), await load(outside), await read(outside), await read(brand)];
                }
                """,
                outside.as_uri(),
            )
            # (Both fetches stop at the page's own CSP, connect-src 'none',
            # before any request is made: the outside image is the one
            # request the interceptor refuses.)
            assert r == ["loaded", "error", "failed", "failed"], r
            log = (tmp_path / "olive" / "log" / "client.log").read_text(encoding="utf-8")
            assert log.count("refused a request for a local file outside the app's page") == 1
        finally:
            await app.stop()

    asyncio.run(go())


@pytest.mark.parametrize("enabled", [False, True])
def test_devtools_only_when_asked_for(tmp_path, enabled):
    env = {"OPENARCANUM_DEVTOOLS": "1" if enabled else ""}
    log_file = tmp_path / "olive" / "log" / "client.log"

    async def go():
        app = await App(tmp_path / "olive", name="olive", env=env).start()
        try:
            await app.wait_js("document.querySelector('#lock')", "the lock screen")
            await app.wait_js("window.pywebview && window.pywebview.api", "the bridge")
            r = await app.js("window.pywebview.api.open_devtools().then((v) => v.ok, () => false)")
            assert r is True
            # Ctrl+Shift+I, as the page sees it.
            await app.key("I", modifiers=2 | 8)
            want = "developer tools opened" if enabled else "developer tools are off"
            for _ in range(50):
                if want in log_file.read_text(encoding="utf-8"):
                    break
                await asyncio.sleep(0.2)
            text = log_file.read_text(encoding="utf-8")
            assert want in text
            assert ("developer tools opened" in text) == enabled
        finally:
            await app.stop()

    asyncio.run(go())


DROPS = textwrap.dedent(
    """
    import json
    from qtpy.QtCore import QMimeData, QPoint, QPointF, Qt, QUrl
    from qtpy.QtGui import QDragEnterEvent, QDragMoveEvent, QDropEvent
    from qtpy.QtWidgets import QApplication, QWidget
    from openarcanum.app import _DropBlocker

    class Target(QWidget):
        def __init__(self, parent=None):
            super().__init__(parent)
            self.got = []
            self.setAcceptDrops(True)

        def dragEnterEvent(self, e):
            self.got.append("enter"); e.acceptProposedAction()

        def dragMoveEvent(self, e):
            self.got.append("move"); e.acceptProposedAction()

        def dropEvent(self, e):
            self.got.append("drop"); e.acceptProposedAction()

    def drag(w):
        md = QMimeData()
        md.setUrls([QUrl("file:///tmp/evil.html")])
        for ev in (
            QDragEnterEvent(QPoint(5, 5), Qt.CopyAction, md, Qt.LeftButton, Qt.NoModifier),
            QDragMoveEvent(QPoint(6, 6), Qt.CopyAction, md, Qt.LeftButton, Qt.NoModifier),
            QDropEvent(QPointF(6, 6), Qt.CopyAction, md, Qt.LeftButton, Qt.NoModifier),
        ):
            QApplication.sendEvent(w, ev)
        return w.got

    app = QApplication([])
    out = {"unguarded": drag(Target())}
    view = Target()
    early = Target(view)
    _DropBlocker().install(view)
    late = Target(early)  # like the engine's render widget, made later
    for w in (view, early, late):
        w.setAcceptDrops(True)  # even if something turns drops back on
    out.update(view=drag(view), early=drag(early), late=drag(late))
    print(json.dumps(out))
    """
)


def test_qt_refuses_drops_before_the_engine():
    """The Qt half of drop blocking: the view and every child (the engine's
    render widget appears after the view exists) never see a drag event."""
    env = dict(os.environ, QT_QPA_PLATFORM="offscreen")
    r = subprocess.run([sys.executable, "-c", DROPS], capture_output=True, text=True, timeout=60, env=env)
    assert r.returncode == 0, r.stderr[-2000:]
    out = json.loads(r.stdout.strip().splitlines()[-1])
    assert out["unguarded"] == ["enter", "move", "drop"], out  # the stand-in works
    assert out["view"] == out["early"] == out["late"] == [], out
