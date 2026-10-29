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

# Key names as Settings stores them (settings._ptt_key, the page's
# KeyboardEvent.key): Space, A-Z, 0-9, F1-F24, the named keys below, and the
# two extra mouse buttons (Mouse4 back, Mouse5 forward). The left, right and
# middle buttons are never push to talk: they are how the app is used.
#
# Named keys: (Windows virtual-key, X11 keysyms, macOS key codes). Shift,
# Control and Alt count either side of the keyboard.
NAMED_KEYS: dict[str, tuple[int, tuple[str, ...], tuple[int, ...]]] = {
    "Shift": (0x10, ("Shift_L", "Shift_R"), (0x38, 0x3C)),
    "Control": (0x11, ("Control_L", "Control_R"), (0x3B, 0x3E)),
    "Alt": (0x12, ("Alt_L", "Alt_R"), (0x3A, 0x3D)),
    "CapsLock": (0x14, ("Caps_Lock",), (0x39,)),
    "Tab": (0x09, ("Tab",), (0x30,)),
    "Insert": (0x2D, ("Insert",), (0x72,)),
    "Delete": (0x2E, ("Delete",), (0x75,)),
    "Home": (0x24, ("Home",), (0x73,)),
    "End": (0x23, ("End",), (0x77,)),
    "PageUp": (0x21, ("Prior",), (0x74,)),
    "PageDown": (0x22, ("Next",), (0x79,)),
    "ArrowUp": (0x26, ("Up",), (0x7E,)),
    "ArrowDown": (0x28, ("Down",), (0x7D,)),
    "ArrowLeft": (0x25, ("Left",), (0x7B,)),
    "ArrowRight": (0x27, ("Right",), (0x7C,)),
    "Pause": (0x13, ("Pause",), ()),
    "ScrollLock": (0x91, ("Scroll_Lock",), ()),
}
# The extra mouse buttons: Windows VK_XBUTTON1/2; macOS button numbers.
# X11's pointer state has no bits for them (only XInput2 would): there they
# work while OpenArcanum is focused.
MOUSE_BUTTONS = {"Mouse4": (0x05, 3), "Mouse5": (0x06, 4)}
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
        self.codes: list[int] = []

    def set_key(self, name: str) -> bool:
        if name in NAMED_KEYS:
            syms = NAMED_KEYS[name][1]
        elif name == "Space":
            syms = ("space",)
        elif name in MOUSE_BUTTONS:
            syms = ()
        else:
            syms = (name.lower() if len(name) == 1 else name,)
        codes = []
        for sym in syms:
            ks = self.x.XStringToKeysym(sym.encode())
            code = self.x.XKeysymToKeycode(self.d, ks) if ks else 0
            if code:
                codes.append(code)
        self.codes = codes
        return bool(codes)

    def down(self) -> bool:
        buf = ctypes.create_string_buffer(32)
        self.x.XQueryKeymap(self.d, buf)
        return any(buf.raw[c // 8] & (1 << (c % 8)) for c in self.codes)

    def close(self) -> None:
        self.x.XCloseDisplay(self.d)


class _Windows:
    def __init__(self) -> None:
        self.u = ctypes.windll.user32  # type: ignore[attr-defined]
        self.vk = 0

    def set_key(self, name: str) -> bool:
        if name in NAMED_KEYS:
            self.vk = NAMED_KEYS[name][0]
        elif name in MOUSE_BUTTONS:
            self.vk = MOUSE_BUTTONS[name][0]
        elif name == "Space":
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
        self.q.CGEventSourceButtonState.restype = ctypes.c_bool
        self.q.CGEventSourceButtonState.argtypes = [ctypes.c_int32, ctypes.c_uint32]
        self.code = -1
        self.codes: tuple[int, ...] = ()
        self.button = -1

    def set_key(self, name: str) -> bool:
        f = _f_number(name)
        self.codes, self.button = (), -1
        if name in NAMED_KEYS:
            self.codes = NAMED_KEYS[name][2]
            self.code = self.codes[0] if self.codes else -1
        elif name in MOUSE_BUTTONS:
            self.button = MOUSE_BUTTONS[name][1]
            self.code = -1
            return True
        elif name == "Space":
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
        if self.button >= 0:
            return bool(self.q.CGEventSourceButtonState(1, self.button))
        return any(self.q.CGEventSourceKeyState(1, c) for c in (self.codes or (self.code,)))


WAYLAND_REASON = N_(
    "This is a Wayland session. Wayland doesn't let apps see keys pressed in other windows, "
    "so push to talk works only while OpenArcanum is focused."
)
KEY_REASON = N_(
    "{key} can't be watched in other windows, so it works only while OpenArcanum is focused. Pick another key."
)
MOUSE_X11_REASON = N_("The extra mouse buttons work only while OpenArcanum is focused here.")
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


def key_reason(name: str) -> str | None:
    """Why this key in particular can't be watched in other windows here
    (shown under the key in Settings), or None."""
    if unavailable_reason():
        return None  # the whole feature is unavailable; that reason is shown
    if name in MOUSE_BUTTONS and sys.platform not in ("win32", "darwin"):
        return _(MOUSE_X11_REASON)
    b = backend()
    try:
        ok = b is not None and b.set_key(name)
    except OSError, AttributeError:
        ok = False
    finally:
        _close(b)
    return None if ok else _(KEY_REASON).format(key=name)


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
