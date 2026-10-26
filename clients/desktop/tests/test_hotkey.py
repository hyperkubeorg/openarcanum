"""Push to talk in other windows (hotkey.py): on X11 the key is read from
the server, so pressing it with the XTEST extension (as a keyboard would,
with no window of ours focused) must be seen."""

import ctypes
import ctypes.util
import os
import subprocess
import sys

import pytest
from conftest import headless, missing_harness

from openarcanum import hotkey

SCRIPT = r"""
import ctypes, ctypes.util, sys, time, threading
from openarcanum import hotkey
seen = []
ptt = hotkey.PushToTalk(seen.append)
assert ptt.start(sys.argv[1]), "no X11 key state"
x = ctypes.cdll.LoadLibrary(ctypes.util.find_library("X11"))
xt = ctypes.cdll.LoadLibrary(ctypes.util.find_library("Xtst"))
x.XOpenDisplay.restype = ctypes.c_void_p
x.XOpenDisplay.argtypes = [ctypes.c_char_p]
x.XStringToKeysym.restype = ctypes.c_ulong
x.XKeysymToKeycode.restype = ctypes.c_ubyte
x.XKeysymToKeycode.argtypes = [ctypes.c_void_p, ctypes.c_ulong]
xt.XTestFakeKeyEvent.argtypes = [ctypes.c_void_p, ctypes.c_uint, ctypes.c_int, ctypes.c_ulong]
x.XFlush.argtypes = [ctypes.c_void_p]
d = x.XOpenDisplay(None)
code = x.XKeysymToKeycode(d, x.XStringToKeysym(sys.argv[2].encode()))
other = x.XKeysymToKeycode(d, x.XStringToKeysym(b"q"))
def press(c, down):
    xt.XTestFakeKeyEvent(d, c, int(down), 0); x.XFlush(d); time.sleep(0.15)
press(other, True); press(other, False)   # another key: nothing
press(code, True); press(code, False)
ptt.stop()
print("SEEN", seen)
"""


@pytest.mark.skipif(
    not sys.platform.startswith("linux"),
    reason="X11 (Windows: test_push_to_talk_on_windows; macOS needs Input Monitoring: docs/user-guide.md)",
)
@pytest.mark.gui
@pytest.mark.parametrize(("key", "keysym"), [("V", "v"), ("Space", "space"), ("F8", "F8")])
def test_push_to_talk_key_seen_in_other_windows(key, keysym):
    if not ctypes.util.find_library("Xtst"):
        missing_harness("needs libXtst")
    out = subprocess.run(
        headless([sys.executable, "-c", SCRIPT, key, keysym]),
        capture_output=True,
        text=True,
        timeout=60,
        # An X11 session (Xvfb), whatever session the tests were started from.
        env={k: v for k, v in os.environ.items() if k not in ("WAYLAND_DISPLAY", "XDG_SESSION_TYPE")},
    )
    line = next((ln for ln in out.stdout.splitlines() if ln.startswith("SEEN")), "")
    assert line == "SEEN [True, False]", out.stdout + out.stderr


def test_no_global_key_state_on_wayland(monkeypatch):
    monkeypatch.setattr(sys, "platform", "linux")
    monkeypatch.setenv("WAYLAND_DISPLAY", "wayland-0")
    monkeypatch.delenv("DISPLAY", raising=False)
    assert hotkey.backend() is None
    assert not hotkey.PushToTalk(lambda d: None).start("V")


@pytest.mark.parametrize(
    "session",
    [
        {"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"},  # GNOME/KDE with XWayland
        {"XDG_SESSION_TYPE": "wayland", "DISPLAY": ":0"},
        {"XDG_SESSION_TYPE": "Wayland", "DISPLAY": ":1"},
        {"XDG_SESSION_TYPE": "wayland"},
    ],
)
def test_wayland_session_detected_even_under_xwayland(monkeypatch, session):
    """XQueryKeymap through XWayland only sees keys while an X11 window has
    focus, so a Wayland session reports push to talk in other windows as
    unavailable, with the reason Settings shows, even though DISPLAY is set."""
    monkeypatch.setattr(sys, "platform", "linux")
    for k in ("WAYLAND_DISPLAY", "XDG_SESSION_TYPE", "DISPLAY"):
        monkeypatch.delenv(k, raising=False)
    for k, v in session.items():
        monkeypatch.setenv(k, v)
    assert hotkey.wayland_session()
    assert hotkey.unavailable_reason() == hotkey.WAYLAND_REASON
    assert "Wayland" in hotkey.WAYLAND_REASON and "focused" in hotkey.WAYLAND_REASON
    assert hotkey.backend() is None
    assert not hotkey.PushToTalk(lambda d: None).start("V")


def test_unavailable_reason_elsewhere(monkeypatch):
    for k in ("WAYLAND_DISPLAY", "XDG_SESSION_TYPE", "DISPLAY"):
        monkeypatch.delenv(k, raising=False)
    monkeypatch.setattr(sys, "platform", "linux")
    # No display at all.
    assert hotkey.unavailable_reason() == hotkey.NO_X11_REASON
    # An X11 session.
    monkeypatch.setenv("DISPLAY", ":0")
    monkeypatch.setenv("XDG_SESSION_TYPE", "x11")
    monkeypatch.setattr(hotkey.ctypes.util, "find_library", lambda name: "libX11.so.6")
    assert not hotkey.wayland_session()
    assert hotkey.unavailable_reason() is None
    # Windows and macOS read the key themselves (macOS after Input
    # Monitoring is allowed, which only macOS can show).
    for plat in ("win32", "darwin"):
        monkeypatch.setattr(sys, "platform", plat)
        monkeypatch.setenv("WAYLAND_DISPLAY", "wayland-0")
        assert hotkey.unavailable_reason() is None


def test_key_names_map_on_every_system():
    w, m = hotkey._Windows.__new__(hotkey._Windows), hotkey._Mac.__new__(hotkey._Mac)
    for name, vk, mac in [
        ("Space", 0x20, 0x31),
        ("A", 0x41, 0x00),
        ("V", 0x56, 0x09),
        ("7", 0x37, 0x1A),
        ("F1", 0x70, 0x7A),
        ("F12", 0x7B, 0x6F),
    ]:
        assert w.set_key(name) and w.vk == vk, name
        assert m.set_key(name) and m.code == mac, name
    assert not w.set_key("F25") and not m.set_key("F25")


@pytest.mark.skipif(sys.platform != "win32", reason="Windows key state")
def test_push_to_talk_on_windows():
    """GetAsyncKeyState sees a key pressed while no window of ours has the
    focus (keybd_event injects it as a keyboard would)."""
    import time

    seen: list[bool] = []
    ptt = hotkey.PushToTalk(seen.append)
    assert ptt.start("F8")
    user32 = ctypes.windll.user32
    vk_f8, vk_q, keyup = 0x77, 0x51, 0x0002
    try:
        for vk in (vk_q, vk_f8):  # another key first: nothing
            user32.keybd_event(vk, 0, 0, 0)
            time.sleep(0.15)
            user32.keybd_event(vk, 0, keyup, 0)
            time.sleep(0.15)
    finally:
        ptt.stop()
    assert seen == [True, False]
