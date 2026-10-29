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
@pytest.mark.parametrize(
    ("key", "keysym"),
    # Named keys too, either side for the modifiers (the right Shift for "Shift").
    [
        ("V", "v"),
        ("Space", "space"),
        ("F8", "F8"),
        ("Control", "Control_L"),
        ("Shift", "Shift_R"),
        ("CapsLock", "Caps_Lock"),
    ],
)
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
    # Named keys (Windows' Control is either side; macOS lists both sides).
    for name, vk, mac in [
        ("Control", 0x11, (0x3B, 0x3E)),
        ("Shift", 0x10, (0x38, 0x3C)),
        ("CapsLock", 0x14, (0x39,)),
        ("ArrowUp", 0x26, (0x7E,)),
    ]:
        assert w.set_key(name) and w.vk == vk, name
        assert m.set_key(name) and m.codes == mac, name
    # The extra mouse buttons: Windows XBUTTON1/2, macOS buttons 3 and 4.
    for name, vk, button in [("Mouse4", 0x05, 3), ("Mouse5", 0x06, 4)]:
        assert w.set_key(name) and w.vk == vk, name
        assert m.set_key(name) and m.button == button, name
    # Never the left, right or middle button (VK_LBUTTON, RBUTTON, MBUTTON).
    for name in ("Mouse1", "Mouse2", "Mouse3"):
        assert not w.set_key(name) and w.vk not in (0x01, 0x02, 0x04), name
    # Pause has no macOS key code: refused there, not watched as key 0 (A).
    assert not m.set_key("Pause")


def test_key_reason_names_keys_that_only_work_focused(monkeypatch):
    monkeypatch.setattr(sys, "platform", "linux")
    monkeypatch.delenv("WAYLAND_DISPLAY", raising=False)
    monkeypatch.delenv("XDG_SESSION_TYPE", raising=False)
    monkeypatch.setenv("DISPLAY", ":99")
    monkeypatch.setattr(hotkey.ctypes.util, "find_library", lambda n: "libX11.so.6")

    class Fake:
        def set_key(self, name):
            return name in ("V", "Control")

    monkeypatch.setattr(hotkey, "backend", lambda: Fake())
    assert hotkey.key_reason("V") is None and hotkey.key_reason("Control") is None
    assert "Banana" in hotkey.key_reason("Banana")
    # X11 has no state for the extra mouse buttons: focused only, and says so.
    assert "mouse" in hotkey.key_reason("Mouse4")
    # Where the whole feature is unavailable, that reason is shown instead.
    monkeypatch.setenv("WAYLAND_DISPLAY", "wayland-0")
    assert hotkey.key_reason("Banana") is None


def test_settings_refuse_the_main_mouse_buttons():
    from openarcanum.settings import SettingError, validate

    for ok in ("Mouse4", "Mouse5", "Control", "Space", "F8"):
        assert validate("ptt_key", ok) == ok
    for bad in ("Mouse1", "Mouse2", "Mouse3", "Mouse0"):
        with pytest.raises(SettingError):
            validate("ptt_key", bad)


@pytest.mark.skipif(sys.platform != "win32", reason="Windows key state")
@pytest.mark.parametrize(("key", "vk"), [("F8", 0x77), ("Control", 0x11), ("CapsLock", 0x14)])
def test_push_to_talk_on_windows(key, vk):
    """GetAsyncKeyState sees a key pressed while no window of ours has the
    focus (keybd_event injects it as a keyboard would)."""
    import time

    seen: list[bool] = []
    ptt = hotkey.PushToTalk(seen.append)
    assert ptt.start(key)
    user32 = ctypes.windll.user32
    vk_q, keyup = 0x51, 0x0002
    try:
        for v in (vk_q, vk):  # another key first: nothing
            user32.keybd_event(v, 0, 0, 0)
            time.sleep(0.15)
            user32.keybd_event(v, 0, keyup, 0)
            time.sleep(0.15)
        if key == "CapsLock":  # pressed once more: Caps Lock back off
            user32.keybd_event(vk, 0, 0, 0)
            user32.keybd_event(vk, 0, keyup, 0)
            time.sleep(0.15)
    finally:
        ptt.stop()
    assert seen[:2] == [True, False]


@pytest.mark.skipif(sys.platform != "win32", reason="Windows key state")
@pytest.mark.parametrize(("key", "xbutton"), [("Mouse4", 1), ("Mouse5", 2)])
def test_extra_mouse_button_push_to_talk_on_windows(key, xbutton):
    """An extra mouse button (mouse_event injects it as a mouse would) is
    seen with no window of ours focused; a left click is not."""
    import time

    seen: list[bool] = []
    ptt = hotkey.PushToTalk(seen.append)
    assert ptt.start(key)
    user32 = ctypes.windll.user32
    leftdown, leftup, xdown, xup = 0x0002, 0x0004, 0x0080, 0x0100
    try:
        user32.mouse_event(leftdown, 0, 0, 0, 0)  # a left click: nothing
        time.sleep(0.15)
        user32.mouse_event(leftup, 0, 0, 0, 0)
        time.sleep(0.15)
        user32.mouse_event(xdown, 0, 0, xbutton, 0)
        time.sleep(0.15)
        user32.mouse_event(xup, 0, 0, xbutton, 0)
        time.sleep(0.15)
    finally:
        ptt.stop()
    assert seen == [True, False]
