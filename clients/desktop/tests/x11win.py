"""The app's top-level X11 windows, seen and closed the way a window manager
does (tests, Linux; libX11 through ctypes, nothing to install). Run in the
app's own session (its DISPLAY and XAUTHORITY: tests/fake_sni.py's ready
file has them):

    python x11win.py list PID    JSON: each window of process PID with its
                                 title, whether it is mapped (shown), and the
                                 sizes of its window icon (_NET_WM_ICON)
    python x11win.py close PID   ask its titled windows to close
                                 (WM_DELETE_WINDOW, what clicking a window's
                                 close button sends); prints how many

Xvfb runs no window manager, so the windows are the root window's children.
"""

from __future__ import annotations

import ctypes
import ctypes.util
import json
import sys
from ctypes import POINTER, Structure, Union, byref, c_char_p, c_int, c_long, c_ubyte, c_uint, c_ulong, c_void_p

X = ctypes.CDLL(ctypes.util.find_library("X11") or "libX11.so.6")
Window = Atom = c_ulong
IS_VIEWABLE = 2
CLIENT_MESSAGE = 33
NO_EVENT_MASK = 0


class XWindowAttributes(Structure):
    _fields_ = [
        ("x", c_int),
        ("y", c_int),
        ("width", c_int),
        ("height", c_int),
        ("border_width", c_int),
        ("depth", c_int),
        ("visual", c_void_p),
        ("root", Window),
        ("class_", c_int),
        ("bit_gravity", c_int),
        ("win_gravity", c_int),
        ("backing_store", c_int),
        ("backing_planes", c_ulong),
        ("backing_pixel", c_ulong),
        ("save_under", c_int),
        ("colormap", c_ulong),
        ("map_installed", c_int),
        ("map_state", c_int),
        ("all_event_masks", c_long),
        ("your_event_mask", c_long),
        ("do_not_propagate_mask", c_long),
        ("override_redirect", c_int),
        ("screen", c_void_p),
    ]


class XClientMessageEvent(Structure):
    _fields_ = [
        ("type", c_int),
        ("serial", c_ulong),
        ("send_event", c_int),
        ("display", c_void_p),
        ("window", Window),
        ("message_type", Atom),
        ("format", c_int),
        ("data", c_long * 5),
    ]


class XEvent(Union):
    _fields_ = [("xclient", XClientMessageEvent), ("pad", c_long * 24)]


X.XOpenDisplay.restype = c_void_p
X.XOpenDisplay.argtypes = [c_char_p]
X.XDefaultRootWindow.restype = Window
X.XDefaultRootWindow.argtypes = [c_void_p]
X.XInternAtom.restype = Atom
X.XInternAtom.argtypes = [c_void_p, c_char_p, c_int]
X.XQueryTree.argtypes = [c_void_p, Window, POINTER(Window), POINTER(Window), POINTER(POINTER(Window)), POINTER(c_uint)]
X.XGetWindowProperty.argtypes = [
    c_void_p,
    Window,
    Atom,
    c_long,
    c_long,
    c_int,
    Atom,
    POINTER(Atom),
    POINTER(c_int),
    POINTER(c_ulong),
    POINTER(c_ulong),
    POINTER(POINTER(c_ubyte)),
]
X.XGetWindowAttributes.argtypes = [c_void_p, Window, POINTER(XWindowAttributes)]
X.XSendEvent.argtypes = [c_void_p, Window, c_int, c_long, POINTER(XEvent)]
X.XFree.argtypes = [c_void_p]
X.XFlush.argtypes = [c_void_p]
X.XCloseDisplay.argtypes = [c_void_p]


class Display:
    def __init__(self):
        self.d = X.XOpenDisplay(None)
        if not self.d:
            raise SystemExit("can't open the display")
        self.root = X.XDefaultRootWindow(self.d)

    def atom(self, name: str) -> int:
        return X.XInternAtom(self.d, name.encode(), 0)

    def prop(self, w: int, name: str, length: int = 1 << 22) -> tuple[int, list[int] | bytes]:
        """(format, items): 32-bit items as ints, 8-bit as bytes."""
        actual, fmt, n, after = Atom(), c_int(), c_ulong(), c_ulong()
        data = POINTER(c_ubyte)()
        X.XGetWindowProperty(
            self.d, w, self.atom(name), 0, length, 0, 0, byref(actual), byref(fmt), byref(n), byref(after), byref(data)
        )
        if not data:
            return 0, []
        try:
            if fmt.value == 32:  # stored as C longs
                return 32, list(ctypes.cast(data, POINTER(c_long))[: n.value])
            return fmt.value, bytes(data[: n.value])
        finally:
            X.XFree(data)

    def children(self) -> list[int]:
        root, parent, kids, n = Window(), Window(), POINTER(Window)(), c_uint()
        X.XQueryTree(self.d, self.root, byref(root), byref(parent), byref(kids), byref(n))
        try:
            return [kids[i] for i in range(n.value)]
        finally:
            if kids:
                X.XFree(kids)

    def windows(self, pid: int) -> list[dict]:
        out = []
        for w in self.children():
            fmt, owner = self.prop(w, "_NET_WM_PID")
            if fmt != 32 or not owner or owner[0] != pid:
                continue
            _f, title = self.prop(w, "_NET_WM_NAME")
            attrs = XWindowAttributes()
            X.XGetWindowAttributes(self.d, w, byref(attrs))
            out.append(
                {
                    "id": w,
                    "title": bytes(title).decode(errors="replace") if title else "",
                    "mapped": attrs.map_state == IS_VIEWABLE,
                    "icon_sizes": self.icon_sizes(w),
                }
            )
        return out

    def icon_sizes(self, w: int) -> list[list[int]]:
        """_NET_WM_ICON: width, height, then width*height pixels, repeated."""
        _fmt, data = self.prop(w, "_NET_WM_ICON")
        sizes, i = [], 0
        while i + 2 <= len(data):
            width, height = data[i], data[i + 1]
            sizes.append([width, height])
            i += 2 + width * height
        return sizes

    def close(self, w: int) -> None:
        ev = XEvent()
        ev.xclient.type = CLIENT_MESSAGE
        ev.xclient.window = w
        ev.xclient.message_type = self.atom("WM_PROTOCOLS")
        ev.xclient.format = 32
        ev.xclient.data[0] = self.atom("WM_DELETE_WINDOW")
        ev.xclient.data[1] = 0  # CurrentTime
        X.XSendEvent(self.d, w, 0, NO_EVENT_MASK, byref(ev))
        X.XFlush(self.d)


def main(argv: list[str]) -> None:
    cmd, pid = argv[0], int(argv[1])
    d = Display()
    wins = d.windows(pid)
    if cmd == "list":
        print(json.dumps(wins))
    elif cmd == "close":
        titled = [w for w in wins if w["title"] and w["mapped"]]
        for w in titled:
            d.close(w["id"])
        print(len(titled))
    X.XCloseDisplay(d.d)


if __name__ == "__main__":
    main(sys.argv[1:])
