"""Push to talk while another window is focused.

While the user is in a call in push-to-talk mode, this polls the state of
the one configured key, 50 times a second, through the operating system:

- X11: `XQueryKeymap` (libX11);
- Windows: `GetAsyncKeyState` (user32);
- macOS: `CGEventSourceKeyState` (ApplicationServices); macOS answers only
  once the user allows OpenArcanum under Privacy & Security → Input Monitoring,
  and reports the key as up until then.

Wayland gives applications no way to see keys pressed in other windows, so
there (and wherever the system call isn't available) push to talk works only
while OpenArcanum is focused, which the page handles itself. A Wayland session
is recognised even when `DISPLAY` is set too (GNOME and KDE run XWayland for
X11 apps): XQueryKeymap through XWayland only sees keys while an X11 window
has focus, so it would silently miss keys pressed in native Wayland apps.
Settings shows why (`unavailable_reason`). Nothing but the one key's up/down
state is read, and only during a call.
"""

from __future__ import annotations

import ctypes
import ctypes.util
import logging
import os
import sys
import threading
from collections.abc import Callable

from openarcanum.i18n import N_, _

log = logging.getLogger(__name__)

POLL_SECONDS = 0.02

# Key names as Settings stores them (settings._ptt_key): Space, A-Z, 0-9, F1-F24.
_MAC_LETTERS = {
    "A": 0x00, "S": 0x01, "D": 0x02, "F": 0x03, "H": 0x04, "G": 0x05, "Z": 0x06, "X": 0x07, "C": 0x08,
    "V": 0x09, "B": 0x0B, "Q": 0x0C, "W": 0x0D, "E": 0x0E, "R": 0x0F, "Y": 0x10, "T": 0x11, "O": 0x1F,
    "U": 0x20, "I": 0x22, "P": 0x23, "L": 0x25, "J": 0x26, "K": 0x28, "N": 0x2D, "M": 0x2E,
}  # fmt: skip
_MAC_DIGITS = {
    "1": 0x12,
    "2": 0x13,
    "3": 0x14,
    "4": 0x15,
    "6": 0x16,
    "5": 0x17,
    "9": 0x19,
    "7": 0x1A,
    "8": 0x1C,
    "0": 0x1D,
}
_MAC_F = [
    0x7A,
    0x78,
    0x63,
    0x76,
    0x60,
    0x61,
    0x62,
    0x64,
    0x65,
    0x6D,
    0x67,
    0x6F,
    0x69,
    0x6B,
    0x71,
    0x6A,
    0x40,
    0x4F,
    0x50,
    0x5A,
]


def _f_number(name: str) -> int:
    if len(name) > 1 and name[0] == "F" and name[1:].isdigit():
        return int(name[1:])
    return 0


class _X11:
    def __init__(self) -> None:
        lib = ctypes.util.find_library("X11")
        if not lib or not os.environ.get("DISPLAY"):
            raise OSError("no X11 display")
        self.x = ctypes.cdll.LoadLibrary(lib)
        self.x.XOpenDisplay.restype = ctypes.c_void_p
        self.x.XOpenDisplay.argtypes = [ctypes.c_char_p]
        self.x.XStringToKeysym.restype = ctypes.c_ulong
        self.x.XStringToKeysym.argtypes = [ctypes.c_char_p]
        self.x.XKeysymToKeycode.restype = ctypes.c_ubyte
        self.x.XKeysymToKeycode.argtypes = [ctypes.c_void_p, ctypes.c_ulong]
        self.x.XQueryKeymap.argtypes = [ctypes.c_void_p, ctypes.c_char_p]
        self.x.XCloseDisplay.argtypes = [ctypes.c_void_p]
        self.d = self.x.XOpenDisplay(None)
        if not self.d:
            raise OSError("can't open the X display")
        self.code = 0

    def set_key(self, name: str) -> bool:
        sym = "space" if name == "Space" else name.lower() if len(name) == 1 else name
        ks = self.x.XStringToKeysym(sym.encode())
        self.code = self.x.XKeysymToKeycode(self.d, ks) if ks else 0
        return self.code != 0

    def down(self) -> bool:
        buf = ctypes.create_string_buffer(32)
        self.x.XQueryKeymap(self.d, buf)
        return bool(buf.raw[self.code // 8] & (1 << (self.code % 8)))

    def close(self) -> None:
        self.x.XCloseDisplay(self.d)


class _Windows:
    def __init__(self) -> None:
        self.u = ctypes.windll.user32  # type: ignore[attr-defined]
        self.vk = 0

    def set_key(self, name: str) -> bool:
        if name == "Space":
            self.vk = 0x20
        elif len(name) == 1 and name.isalnum():
            self.vk = ord(name.upper())
        elif 1 <= (f := _f_number(name)) <= 24:
            self.vk = 0x6F + f
        else:
            self.vk = 0
        return self.vk != 0

    def down(self) -> bool:
        return bool(self.u.GetAsyncKeyState(self.vk) & 0x8000)


class _Mac:
    def __init__(self) -> None:
        self.q = ctypes.cdll.LoadLibrary("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices")
        self.q.CGEventSourceKeyState.restype = ctypes.c_bool
        self.q.CGEventSourceKeyState.argtypes = [ctypes.c_int32, ctypes.c_uint16]
        self.code = -1

    def set_key(self, name: str) -> bool:
        f = _f_number(name)
        if name == "Space":
            self.code = 0x31
        elif name.upper() in _MAC_LETTERS:
            self.code = _MAC_LETTERS[name.upper()]
        elif name in _MAC_DIGITS:
            self.code = _MAC_DIGITS[name]
        elif 1 <= f <= len(_MAC_F):
            self.code = _MAC_F[f - 1]
        else:
            self.code = -1
        return self.code >= 0

    def down(self) -> bool:
        # kCGEventSourceStateHIDSystemState = 1
        return bool(self.q.CGEventSourceKeyState(1, self.code))


WAYLAND_REASON = N_(
    "This is a Wayland session. Wayland doesn't let apps see keys pressed in other windows, "
    "so push to talk works only while OpenArcanum is focused."
)
NO_X11_REASON = N_("There's no X11 display here, so push to talk works only while OpenArcanum is focused.")


def wayland_session() -> bool:
    """A Wayland desktop session, whether or not XWayland also sets DISPLAY."""
    return os.environ.get("XDG_SESSION_TYPE", "").lower() == "wayland" or bool(os.environ.get("WAYLAND_DISPLAY"))


def unavailable_reason() -> str | None:
    """Why push to talk in other windows can't work here (shown in Settings),
    or None where it can: Windows, X11, and macOS once Input Monitoring is
    allowed (which only macOS itself can show)."""
    if sys.platform in ("win32", "darwin"):
        return None
    if wayland_session():
        return _(WAYLAND_REASON)
    if not os.environ.get("DISPLAY") or not ctypes.util.find_library("X11"):
        return _(NO_X11_REASON)
    return None


def backend():
    """The system's key-state reader, or None where there is none (Wayland)."""
    try:
        if sys.platform == "win32":
            return _Windows()
        if sys.platform == "darwin":
            return _Mac()
        if unavailable_reason():
            return None
        return _X11()
    except (OSError, AttributeError) as e:
        log.info("push to talk: no global key state here (%s)", type(e).__name__)
        return None


def _close(b) -> None:
    # Only the X11 reader holds something (its display connection).
    if isinstance(b, _X11):
        b.close()


class PushToTalk:
    """Calls on_change(down) whenever the key goes down or up, from a thread."""

    def __init__(self, on_change: Callable[[bool], None]):
        self.on_change = on_change
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self.key = ""

    @property
    def running(self) -> bool:
        return self._thread is not None and self._thread.is_alive()

    def start(self, key: str) -> bool:
        """Starts watching key; False where the system can't tell (the page
        still handles the key while OpenArcanum is focused)."""
        self.stop()
        b = backend()
        if b is None or not b.set_key(key):
            _close(b)
            return False
        self.key = key
        self._stop.clear()
        self._thread = threading.Thread(target=self._run, args=(b,), daemon=True, name="push-to-talk")
        self._thread.start()
        return True

    def stop(self) -> None:
        self._stop.set()
        if self._thread is not None:
            self._thread.join(timeout=1)
        self._thread = None

    def _run(self, b) -> None:
        was = False
        try:
            while not self._stop.wait(POLL_SECONDS):
                now = b.down()
                if now != was:
                    was = now
                    self.on_change(now)
        except OSError:
            log.warning("push to talk: lost the key state")
        finally:
            if was:
                self.on_change(False)
            _close(b)
