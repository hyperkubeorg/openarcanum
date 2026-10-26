"""The page can call only JsApi's own public methods (api.install_bridge_guard).

pywebview resolves a bridge call by walking dotted names through any
attribute of the js_api object, private ones included, so page script could
reach `_core.vault.data` (the keys). These tests drive the real pywebview
dispatch (webview.util.js_bridge_call, and the Qt backend's JSBridge.call that
the page's calls arrive through) with the guard installed."""

from __future__ import annotations

import json
import threading
import time

import pytest

from openarcanum import api as api_mod
from openarcanum.api import JsApi, bridge_allows, bridge_methods, install_bridge_guard


class _Vault:
    def __init__(self):
        self.data = {"identity_seed": b"\x01" * 32, "passphrase_key": b"\x02" * 32}


class _Core:
    def __init__(self):
        self.vault = _Vault()


class _Gui:
    def __init__(self):
        self.copied: list[str] = []
        self.devtools = 0

    def copy_text(self, text: str) -> None:
        self.copied.append(text)

    def open_devtools(self) -> None:
        self.devtools += 1


class _Window:
    """What pywebview's dispatch reads from a window."""

    def __init__(self, js_api):
        self._functions: dict = {}
        self._js_api = js_api
        self._callbacks: dict = {}
        self.out: list[str] = []
        self.moved: list = []
        self.got = threading.Event()

    def evaluate_js(self, script: str) -> None:
        self.out.append(script)
        self.got.set()

    def move(self, *xy) -> None:
        self.moved.append(xy)


@pytest.fixture
def bridge():
    import webview.platforms.qt as qt
    import webview.util

    before_util, before_qt = webview.util.js_bridge_call, qt.js_bridge_call
    install_bridge_guard()
    core, gui = _Core(), _Gui()
    js_api = JsApi(loop=None, core=core, bus=None, gui=gui)  # type: ignore[arg-type]
    window = _Window(js_api)
    try:
        yield window, core, gui, qt
    finally:
        webview.util.js_bridge_call, qt.js_bridge_call = before_util, before_qt


def _call_through_qt(qt, window, name: str, params) -> None:
    """A call as the page makes it: through the Qt backend's bridge slot."""

    class Bridge:
        pass

    b = Bridge()
    b._parent = None
    b.window = window
    qt.BrowserView.JSBridge.call(b, name, json.dumps(params), "1")


ATTACKS = [
    "_core.vault.data.__repr__",
    "_core.vault.data.clear",
    "_core",
    "_wrap",
    "__init__",
    "__class__",
    "__repr__",
    "copy_text.__globals__",
    "copy_text.__func__.__globals__.get",
    "ready.__self__._core.vault.data.__repr__",
    "_gui.copy_text",
    "pywebviewMoveWindow",
    "pywebviewStateUpdate",
    "pywebviewStateDelete",
    "pywebviewEventHandler",
    "pywebviewAsyncCallback",
    "no_such_method",
    "",
]


@pytest.mark.parametrize("name", ATTACKS)
def test_the_page_cant_reach_anything_but_the_api(bridge, name):
    window, core, gui, qt = bridge
    params = [] if not name.startswith("pywebview") else ([5, 5] if "Move" in name else {"key": "k", "value": 1})
    _call_through_qt(qt, window, name, params)
    time.sleep(0.3)
    assert window.out == [], window.out  # no value came back to the page
    assert window.moved == []
    assert core.vault.data["identity_seed"] == b"\x01" * 32 and len(core.vault.data) == 2
    assert gui.copied == []


def test_the_api_itself_still_works(bridge):
    window, _core, gui, qt = bridge
    _call_through_qt(qt, window, "copy_text", ["hello"])
    assert window.got.wait(5)
    assert gui.copied == ["hello"]
    assert '\\"ok\\": true' in window.out[0] or '"ok": true' in window.out[0]


def test_the_allowlist_is_exactly_jsapis_public_methods():
    names = bridge_methods()
    assert "copy_text" in names and "ready" in names
    assert not any(n.startswith("_") for n in names)
    assert all(callable(getattr(JsApi, n)) for n in names)
    # Methods from object (dunders) and private helpers are never in it.
    for n in ("_wrap", "_a", "_s", "__init__", "__repr__", "__getattribute__"):
        assert n not in names
    for n in ("a.b", "_x", "__repr__", None, 3, ["copy_text"]):
        assert not bridge_allows(n, names | {"a.b", "_x", "__repr__"})


def test_the_guard_is_installed_once(bridge):
    import webview.util

    install_bridge_guard()  # twice: still one layer, over pywebview's own
    inner = webview.util.js_bridge_call._openarcanum_original
    assert not hasattr(inner, "_openarcanum_original")
    assert inner.__module__ == "webview.util"


def test_app_installs_the_guard_before_the_window():
    """run() installs the guard before it creates the window."""
    import inspect

    from openarcanum import app

    src = inspect.getsource(app.run)
    assert src.index("install_bridge_guard()") < src.index("webview.create_window(")
    assert api_mod.install_bridge_guard is app.install_bridge_guard
